package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/metrics"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/monitor"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

type hookCall struct {
	body              []byte
	ts, sig, eventHdr string
}

// fakeSMTP accepts mail without TLS or auth and records the DATA sections.
func fakeSMTP(t *testing.T) (string, func() []string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var mails []string
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				say := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
				say("220 fake ESMTP")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						say("250 fake")
					case strings.HasPrefix(cmd, "DATA"):
						say("354 go on")
						var b strings.Builder
						for {
							l, err := r.ReadString('\n')
							if err != nil || l == ".\r\n" {
								break
							}
							b.WriteString(l)
						}
						mu.Lock()
						mails = append(mails, b.String())
						mu.Unlock()
						say("250 queued")
					case strings.HasPrefix(cmd, "QUIT"):
						say("221 bye")
						return
					default:
						say("250 ok")
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), mails...)
	}
}

func TestAlertsChannelsRulesAndRetention(t *testing.T) {
	e := newAgentEnv(t)
	res, _ := e.enroll(e.addServer("alert-1"))
	id := res.Response.ServerID
	ctx := context.Background()
	keys, _ := secrets.NewKeyring(bytes.Repeat([]byte{9}, 32))
	mon := monitor.New(e.store(), e.svc, keys, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	auditor := e.principal("auditor-al@example.com", rbac.RoleAuditor)

	var mu sync.Mutex
	var calls []hookCall
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, hookCall{b, r.Header.Get("X-Osriva-Timestamp"), r.Header.Get("X-Osriva-Signature"), r.Header.Get("X-Osriva-Event")})
		mu.Unlock()
	}))
	defer hook.Close()
	smtpAddr, mails := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(smtpAddr)

	if _, err := mon.CreateChannel(ctx, auditor, monitor.ChannelInput{Name: "x", Kind: "webhook", Config: json.RawMessage(`{"url":"https://a.example.com"}`)}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor created a channel: %v", err)
	}
	var ve *identity.ValidationError
	for _, bad := range []monitor.ChannelInput{
		{Name: "a", Kind: "webhook", Config: json.RawMessage(`{"url":"ftp://x"}`)},
		{Name: "a", Kind: "webhook", Config: json.RawMessage(`{"url":"https://u:p@x.example.com"}`)},
		{Name: "a", Kind: "email", Config: json.RawMessage(`{"host":"smtp.example.com","port":25,"tls":"none","from":"bad","to":["a@b.com"]}`)},
		{Name: "a", Kind: "email", Config: json.RawMessage(`{"host":"smtp.example.com","port":25,"tls":"maybe","from":"a@b.com","to":["a@b.com"]}`)},
		{Name: "a\r\nBcc: x", Kind: "webhook", Config: json.RawMessage(`{"url":"https://a.example.com"}`)},
		{Name: "a", Kind: "sms", Config: json.RawMessage(`{}`)},
	} {
		if _, err := mon.CreateChannel(ctx, e.owner, bad); !errors.As(err, &ve) {
			t.Errorf("accepted %+v: %v", bad, err)
		}
	}
	wh, err := mon.CreateChannel(ctx, e.owner, monitor.ChannelInput{Name: "ops hook", Kind: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"` + hook.URL + `/hooks/T0KEN-in-path"}`)})
	if err != nil || len(wh.WebhookSecret) != 64 {
		t.Fatalf("webhook channel: %+v %v", wh, err)
	}
	em, err := mon.CreateChannel(ctx, e.owner, monitor.ChannelInput{Name: "ops mail", Kind: "email", Enabled: true,
		Config: json.RawMessage(`{"host":"` + host + `","port":` + port + `,"tls":"none","from":"osriva@example.com","to":["ops@example.com"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(monitor.MaskedConfig(&wh.NotificationChannel)), "T0KEN") {
		t.Fatal("readers must not see the webhook path")
	}
	audit, _ := e.store().ListAudit(ctx, store.AuditFilter{OrganizationID: store.DefaultOrganizationID, Limit: 500})
	for _, a := range audit {
		if raw, _ := json.Marshal(a.Metadata); strings.Contains(string(raw), "T0KEN") || strings.Contains(string(raw), wh.WebhookSecret) {
			t.Fatalf("secret in audit: %s", raw)
		}
	}

	// test notification: signed with the channel secret
	ev, err := mon.TestChannel(ctx, e.owner, wh.ID)
	if err != nil || ev.Delivered != 1 || ev.Failed != 0 {
		t.Fatalf("test webhook: %+v %v", ev, err)
	}
	mu.Lock()
	c := calls[0]
	mu.Unlock()
	if c.eventHdr != "alert.test" || c.sig != monitor.Sign(wh.WebhookSecret, c.ts, c.body) || monitor.Sign("wrong", c.ts, c.body) == c.sig {
		t.Fatalf("signature: %+v", c)
	}
	if ev, _ := mon.TestChannel(ctx, e.owner, em.ID); ev.Delivered != 1 || len(mails()) != 1 || !strings.Contains(mails()[0], "Subject: [OSRIVA] Test notification") {
		t.Fatalf("test mail: %+v %q", ev, mails())
	}

	// cpu above 50% for the whole minute → one firing event, then resolved
	if _, err := mon.CreateRule(ctx, e.owner, monitor.RuleInput{Name: "cpu", Metric: "cpu", Threshold: 150, DurationMinutes: 1}); !errors.As(err, &ve) {
		t.Fatalf("threshold above 100%%: %v", err)
	}
	rule, err := mon.CreateRule(ctx, e.owner, monitor.RuleInput{Name: "cpu high", ServerID: &id, Metric: "cpu", Threshold: 50,
		DurationMinutes: 1, ChannelIDs: []string{wh.ID, em.ID}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	insert := func(cpu float64, from time.Time) {
		var s []store.MetricSample
		for i := 0; i <= 7; i++ {
			ts := from.Add(time.Duration(i) * 10 * time.Second)
			d, _ := json.Marshal(metrics.Sample{TS: ts, CPUPercent: &cpu})
			s = append(s, store.MetricSample{ServerID: id, TS: ts, Data: d})
		}
		if err := e.store().InsertMetrics(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	insert(92, now.Add(-70*time.Second))
	mon.Evaluate(ctx, now)
	mon.Evaluate(ctx, now.Add(time.Second)) // no duplicate
	evs, _ := mon.ListEvents(ctx, auditor, 10)
	if len(evs) != 3 || evs[0].State != "firing" || evs[0].Delivered != 2 || evs[0].Value < 90 {
		t.Fatalf("firing: %+v", evs)
	}
	if m := mails(); len(m) != 2 || !strings.Contains(m[1], "FIRING: cpu high on alert-1") {
		t.Fatalf("firing mail: %q", m)
	}
	later := now.Add(2 * time.Minute)
	insert(5, later.Add(-70*time.Second))
	mon.Evaluate(ctx, later)
	if evs, _ = mon.ListEvents(ctx, auditor, 10); evs[0].State != "resolved" {
		t.Fatalf("resolved: %+v", evs[0])
	}

	// offline: the agent stops reporting
	if err := e.store().RecordReport(ctx, id, store.ServerFacts{}, "127.0.0.1", time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := mon.CreateRule(ctx, e.owner, monitor.RuleInput{Name: "offline", Metric: "offline", Threshold: 5, DurationMinutes: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	mon.Evaluate(ctx, time.Now().Add(10*time.Minute))
	if evs, _ = mon.ListEvents(ctx, auditor, 10); evs[0].Metric != "offline" || evs[0].State != "firing" || evs[0].Value < 9 {
		t.Fatalf("offline: %+v", evs[0])
	}

	// failed newest backup
	task := &store.Task{ID: auth.NewID(), ServerID: id, Type: "backup.create", Params: json.RawMessage(`{}`), ExpiresAt: now.Add(time.Hour)}
	_ = e.store().CreateTask(ctx, task)
	_, _ = e.store().ClaimTasks(ctx, id, now, 10)
	_ = e.store().CompleteTask(ctx, id, task.ID, store.TaskFailed, nil, "disk full", now)
	_ = e.store().CreateBackup(ctx, &store.Backup{ID: auth.NewID(), ServerID: id, Kind: "volume", Target: "data", TaskID: &task.ID})
	if _, err := mon.CreateRule(ctx, e.owner, monitor.RuleInput{Name: "backups", Metric: "backup", DurationMinutes: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	mon.Evaluate(ctx, now)
	found := false
	evs, _ = mon.ListEvents(ctx, auditor, 20)
	for _, x := range evs {
		found = found || (x.Metric == "backup" && x.State == "firing")
	}
	if !found {
		t.Fatalf("backup failure alert missing: %+v", evs)
	}

	// deleting a channel detaches it from rules
	if err := mon.DeleteChannel(ctx, e.owner, em.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.store().GetAlertRule(ctx, rule.ID); len(r.ChannelIDs) != 1 {
		t.Fatalf("channels after delete: %v", r.ChannelIDs)
	}

	// hourly rollups from raw samples of completed hours
	hour := now.Truncate(time.Hour).Add(-2 * time.Hour) // clear of the samples inserted above
	var raw []store.MetricSample
	for i := 0; i < 6; i++ {
		cpu := float64(10 * (i + 1))
		ts := hour.Add(time.Duration(i) * 5 * time.Minute)
		d, _ := json.Marshal(metrics.Sample{TS: ts, CPUPercent: &cpu})
		raw = append(raw, store.MetricSample{ServerID: id, TS: ts, Data: d})
	}
	_ = e.store().InsertMetrics(ctx, raw)
	mon.RunRollups(ctx, now)
	ro, err := e.fleet.MetricRollups(ctx, auditor, id, 7*24*time.Hour)
	if err != nil || len(ro) == 0 {
		t.Fatalf("rollups: %v %v", ro, err)
	}
	var r monitor.Rollup
	for _, x := range ro {
		var y monitor.Rollup
		if json.Unmarshal(x, &y) == nil && y.Hour.Equal(hour) {
			r = y
		}
	}
	if r.Samples != 6 || r.CPUAvg == nil || *r.CPUAvg != 35 || *r.CPUMax != 60 {
		t.Fatalf("rollup for %s: %+v", hour, r)
	}

	// audit retention: minimum 30 days, owner + re-auth only
	if err := e.svc.SetAuditRetention(ctx, auditor, 60); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor changed retention: %v", err)
	}
	if err := e.svc.SetAuditRetention(ctx, e.owner, 7); !errors.As(err, &ve) {
		t.Fatalf("7 days accepted: %v", err)
	}
	if err := e.svc.SetAuditRetention(ctx, e.owner, 30); err != nil {
		t.Fatal(err)
	}
	mon.RunAuditRetention(ctx, time.Now().Add(31*24*time.Hour))
	left, _ := e.store().ListAudit(ctx, store.AuditFilter{OrganizationID: store.DefaultOrganizationID, Limit: 10})
	if len(left) != 1 || left[0].Action != "audit.pruned" {
		t.Fatalf("after retention: %+v", left)
	}
}
