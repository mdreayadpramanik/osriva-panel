package monitor

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/jobs"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// Sender delivers one event to one channel.
type Sender interface {
	Send(ctx context.Context, c *store.NotificationChannel, secret string, ev *store.AlertEvent) error
}

type sender struct{ hc *http.Client }

func NewSender() Sender {
	return &sender{hc: &http.Client{
		Timeout: 10 * time.Second,
		// A redirect could send the signed payload somewhere else.
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are not followed") },
	}}
}

// WebhookPayload is the JSON body of every webhook.
type WebhookPayload struct {
	Event     string  `json:"event"` // alert.firing | alert.resolved | alert.test
	ID        string  `json:"id"`
	Rule      string  `json:"rule"`
	Server    string  `json:"server"`
	ServerID  string  `json:"server_id,omitempty"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Message   string  `json:"message"`
	At        string  `json:"at"`
}

var units = map[string]string{MetricCPU: "%", MetricMemory: "%", MetricDisk: "%", MetricOffline: " min"}

// Describe is the one-line human text of an event.
func Describe(ev *store.AlertEvent) string {
	switch ev.State {
	case "test":
		return "[OSRIVA] Test notification from OSRIVA Panel."
	case "resolved":
		return fmt.Sprintf("[OSRIVA] RESOLVED: %s on %s (%s %.1f%s)", ev.RuleName, ev.ServerName, ev.Metric, ev.Value, units[ev.Metric])
	}
	if ev.Metric == MetricBackup {
		return fmt.Sprintf("[OSRIVA] FIRING: %s on %s (the newest backup failed)", ev.RuleName, ev.ServerName)
	}
	return fmt.Sprintf("[OSRIVA] FIRING: %s on %s (%s %.1f%s > %.1f%s)", ev.RuleName, ev.ServerName, ev.Metric, ev.Value,
		units[ev.Metric], ev.Threshold, units[ev.Metric])
}

// Sign returns the X-Osriva-Signature value for a timestamp and body:
// "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + body)).
func Sign(secret, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(timestamp + "."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func (s *sender) Send(ctx context.Context, c *store.NotificationChannel, secret string, ev *store.AlertEvent) error {
	switch c.Kind {
	case "webhook":
		return s.webhook(ctx, c, secret, ev)
	case "email":
		return smtpPermanent(s.email(ctx, c, secret, ev))
	}
	return jobs.Permanent(fmt.Errorf("unknown channel kind %q", c.Kind))
}

func (s *sender) webhook(ctx context.Context, c *store.NotificationChannel, secret string, ev *store.AlertEvent) error {
	var cfg WebhookConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return err
	}
	p := WebhookPayload{Event: "alert." + ev.State, ID: ev.ID, Rule: ev.RuleName, Server: ev.ServerName, Metric: ev.Metric,
		Value: ev.Value, Threshold: ev.Threshold, Message: Describe(ev), At: ev.CreatedAt.UTC().Format(time.RFC3339)}
	if ev.ServerID != nil {
		p.ServerID = *ev.ServerID
	}
	body, _ := json.Marshal(p)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OSRIVA-Panel")
	req.Header.Set("X-Osriva-Event", p.Event)
	req.Header.Set("X-Osriva-Delivery", ev.ID)
	req.Header.Set("X-Osriva-Timestamp", ts)
	req.Header.Set("X-Osriva-Signature", Sign(secret, ts, body))
	resp, err := s.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("webhook: %v", ue.Err)
		}
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		err := fmt.Errorf("webhook answered HTTP %d", resp.StatusCode)
		// Other client errors will not change on a retry.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			return jobs.Permanent(err)
		}
		return err
	}
	return nil
}

func (s *sender) email(ctx context.Context, c *store.NotificationChannel, password string, ev *store.AlertEvent) error {
	var cfg EmailConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return err
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	deadline, _ := ctx.Deadline()
	dialer := &net.Dialer{Timeout: 10 * time.Second, Deadline: deadline}
	var conn net.Conn
	var err error
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	if cfg.TLS == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if !deadline.IsZero() {
		_ = conn.SetDeadline(deadline)
	}
	cl, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp: %w", err)
	}
	defer cl.Close()
	if err := cl.Hello("osriva-panel"); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if cfg.TLS == "starttls" {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("smtp: the server does not offer STARTTLS")
		}
		if err := cl.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp: %w", err)
		}
	}
	if cfg.Username != "" {
		// PLAIN auth: net/smtp refuses it on an unencrypted connection (except to localhost).
		if err := cl.Auth(smtp.PlainAuth("", cfg.Username, password, cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := cl.Mail(cfg.From); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	for _, to := range cfg.To {
		if err := cl.Rcpt(to); err != nil {
			return fmt.Errorf("smtp: recipient %s: %w", to, err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	subject := strings.NewReplacer("\r", " ", "\n", " ").Replace(Describe(ev))
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@osriva>\r\nMIME-Version: 1.0\r\n",
		cfg.From, strings.Join(cfg.To, ", "), subject, time.Now().UTC().Format(time.RFC1123Z), auth.NewID())
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\nAuto-Submitted: auto-generated\r\n\r\n")
	fmt.Fprintf(&b, "%s\r\n\r\nRule: %s\r\nServer: %s\r\nMetric: %s\r\nValue: %.1f\r\nThreshold: %.1f\r\nState: %s\r\nTime: %s\r\n",
		subject, ev.RuleName, ev.ServerName, ev.Metric, ev.Value, ev.Threshold, ev.State, ev.CreatedAt.UTC().Format(time.RFC3339))
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	return cl.Quit()
}

// smtpPermanent marks SMTP 5xx replies (bad recipient, rejected sender, auth
// refused) as permanent; 4xx replies and network errors are retried.
func smtpPermanent(err error) error {
	var te *textproto.Error
	if errors.As(err, &te) && te.Code >= 500 {
		return jobs.Permanent(err)
	}
	return err
}
