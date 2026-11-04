// Package monitor evaluates alert rules against agent metrics and server
// state, delivers notifications (signed webhooks, SMTP e-mail), keeps hourly
// metric rollups and applies retention (docs/adr/0011-alerts-notifications.md).
package monitor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/metrics"
	"github.com/mdreayadpramanik/osriva-panel/pkg/pluginsdk"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/jobs"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/plugins"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const (
	MetricCPU     = "cpu"
	MetricMemory  = "memory"
	MetricDisk    = "disk"
	MetricLoad    = "load"
	MetricOffline = "offline"
	MetricBackup  = "backup"

	rollupRetention = 90 * 24 * time.Hour
	uploadLag       = 45 * time.Second
)

type Service struct {
	store   store.Store
	id      *identity.Service
	keys    *secrets.Keyring
	log     *slog.Logger
	sender  Sender
	queue   *jobs.Queue
	plugins *plugins.Registry
	lessor  *jobs.Lessor
}

func New(st store.Store, id *identity.Service, keys *secrets.Keyring, log *slog.Logger, sender Sender) *Service {
	if log == nil {
		log = slog.Default()
	}
	if sender == nil {
		sender = NewSender()
	}
	return &Service{store: st, id: id, keys: keys, log: log, sender: sender}
}

func channelAAD(id string) []byte { return []byte("osriva/notification-channel/" + id) }

// ---- channels ----

// WebhookConfig is stored as the channel config; the signing secret is stored encrypted.
type WebhookConfig struct {
	URL string `json:"url"`
}

type EmailConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	TLS      string   `json:"tls"` // starttls | tls | none
	Username string   `json:"username,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

// PluginConfig sends notifications through an approved notifier plugin (docs/adr/0016-plugins.md).
type PluginConfig struct {
	Plugin   string          `json:"plugin"`
	Settings json.RawMessage `json:"settings,omitempty"`
}

type ChannelInput struct {
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Config  json.RawMessage `json:"config"`
	Secret  *string         `json:"secret"` // SMTP password; nil keeps the stored one on update
	Enabled bool            `json:"enabled"`
}

func validEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && !strings.ContainsAny(s, "\r\n")
}

func validateChannel(kind string, raw json.RawMessage) (json.RawMessage, error) {
	verr := func(msg string) error { return &identity.ValidationError{Field: "config", Message: msg} }
	switch kind {
	case "webhook":
		var c WebhookConfig
		if json.Unmarshal(raw, &c) != nil {
			return nil, verr("must be {url}")
		}
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(c.URL) > 500 {
			return nil, verr("url must be an http(s) URL without credentials")
		}
		return json.Marshal(c)
	case "email":
		var c EmailConfig
		if json.Unmarshal(raw, &c) != nil {
			return nil, verr("must be {host, port, tls, from, to}")
		}
		if c.Host == "" || len(c.Host) > 253 || strings.ContainsAny(c.Host, " /\r\n") || c.Port < 1 || c.Port > 65535 {
			return nil, verr("invalid SMTP host or port")
		}
		if c.TLS != "starttls" && c.TLS != "tls" && c.TLS != "none" {
			return nil, verr("tls must be starttls, tls or none")
		}
		if strings.ContainsAny(c.Username, "\r\n") || len(c.Username) > 200 {
			return nil, verr("invalid user name")
		}
		if !validEmail(c.From) || len(c.To) == 0 || len(c.To) > 20 {
			return nil, verr("from must be an address and to 1-20 addresses")
		}
		for _, t := range c.To {
			if !validEmail(t) {
				return nil, verr(fmt.Sprintf("invalid recipient %q", t))
			}
		}
		return json.Marshal(c)
	case "plugin":
		var c PluginConfig
		if json.Unmarshal(raw, &c) != nil || !pluginName.MatchString(c.Plugin) {
			return nil, verr("must be {plugin, settings}")
		}
		if !plugins.ValidSettings(c.Settings) {
			return nil, verr("settings must be JSON of at most 4 KiB")
		}
		return json.Marshal(c)
	}
	return nil, &identity.ValidationError{Field: "kind", Message: "must be webhook, email or plugin"}
}

// CreatedChannel carries the generated webhook secret, shown once.
type CreatedChannel struct {
	store.NotificationChannel
	WebhookSecret string
}

func (s *Service) ListChannels(ctx context.Context, actor *identity.Principal) ([]store.NotificationChannel, error) {
	if err := s.id.Require(ctx, actor, rbac.AlertsRead); err != nil {
		return nil, err
	}
	return s.store.ListChannels(ctx, actor.User.OrganizationID)
}

func (s *Service) CreateChannel(ctx context.Context, actor *identity.Principal, in ChannelInput) (*CreatedChannel, error) {
	if err := s.id.Require(ctx, actor, rbac.AlertsWrite); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 || strings.ContainsAny(name, "\r\n\t\x00") {
		return nil, &identity.ValidationError{Field: "name", Message: "must be 1-100 characters"}
	}
	cfg, err := validateChannel(in.Kind, in.Config)
	if err != nil {
		return nil, err
	}
	if err := s.checkPlugin(in.Kind, cfg); err != nil {
		return nil, err
	}
	c := &store.NotificationChannel{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, Name: name, Kind: in.Kind,
		Config: cfg, Enabled: in.Enabled, CreatedBy: &actor.User.ID}
	out := &CreatedChannel{}
	secret := ""
	switch {
	case in.Kind == "webhook":
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		secret = hex.EncodeToString(b)
		out.WebhookSecret = secret
	case in.Secret != nil:
		secret = *in.Secret
	}
	if secret != "" {
		if c.SecretEnc, err = s.keys.Encrypt([]byte(secret), channelAAD(c.ID)); err != nil {
			return nil, err
		}
	}
	if err := s.store.CreateChannel(ctx, c); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "alerts.channel_create", "notification_channel", c.ID, store.OutcomeSuccess,
		map[string]any{"name": c.Name, "kind": c.Kind, "target": channelTarget(c)})
	out.NotificationChannel = *c
	return out, nil
}

func (s *Service) channelInOrg(ctx context.Context, actor *identity.Principal, perm, id string) (*store.NotificationChannel, error) {
	if err := s.id.Require(ctx, actor, perm); err != nil {
		return nil, err
	}
	c, err := s.store.GetChannel(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	return c, nil
}

func (s *Service) UpdateChannel(ctx context.Context, actor *identity.Principal, id string, in ChannelInput) (*store.NotificationChannel, error) {
	c, err := s.channelInOrg(ctx, actor, rbac.AlertsWrite, id)
	if err != nil {
		return nil, err
	}
	cfg, err := validateChannel(c.Kind, in.Config)
	if err != nil {
		return nil, err
	}
	if err := s.checkPlugin(c.Kind, cfg); err != nil {
		return nil, err
	}
	if name := strings.TrimSpace(in.Name); name != "" && len(name) <= 100 {
		c.Name = name
	}
	c.Config, c.Enabled = cfg, in.Enabled
	if (c.Kind == "email" || c.Kind == "plugin") && in.Secret != nil {
		c.SecretEnc = nil
		if *in.Secret != "" {
			if c.SecretEnc, err = s.keys.Encrypt([]byte(*in.Secret), channelAAD(c.ID)); err != nil {
				return nil, err
			}
		}
	}
	if err := s.store.UpdateChannel(ctx, c); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "alerts.channel_update", "notification_channel", c.ID, store.OutcomeSuccess,
		map[string]any{"name": c.Name, "enabled": c.Enabled, "target": channelTarget(c)})
	return c, nil
}

func (s *Service) DeleteChannel(ctx context.Context, actor *identity.Principal, id string) error {
	c, err := s.channelInOrg(ctx, actor, rbac.AlertsWrite, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteChannel(ctx, c.ID); err != nil {
		return err
	}
	s.id.Audit(ctx, actor.User, "alerts.channel_delete", "notification_channel", c.ID, store.OutcomeSuccess, map[string]any{"name": c.Name})
	return nil
}

// TestChannel sends a test notification and records it as an event.
func (s *Service) TestChannel(ctx context.Context, actor *identity.Principal, id string) (*store.AlertEvent, error) {
	c, err := s.channelInOrg(ctx, actor, rbac.AlertsWrite, id)
	if err != nil {
		return nil, err
	}
	ev := &store.AlertEvent{ID: auth.NewID(), OrganizationID: c.OrganizationID, RuleName: "Test notification", ServerName: "-",
		Metric: "test", State: "test"}
	if err := s.store.InsertAlertEvent(ctx, ev); err != nil {
		return nil, err
	}
	s.deliver(ctx, ev, []store.NotificationChannel{*c}, true)
	return ev, nil
}

// ---- rules ----

type RuleInput struct {
	Name            string   `json:"name"`
	ServerID        *string  `json:"server_id"`
	GroupID         *string  `json:"group_id"`
	Metric          string   `json:"metric"`
	Threshold       float64  `json:"threshold"`
	DurationMinutes int      `json:"duration_minutes"`
	ChannelIDs      []string `json:"channel_ids"`
	Enabled         bool     `json:"enabled"`
}

func (s *Service) checkRule(ctx context.Context, actor *identity.Principal, r *store.AlertRule) error {
	verr := func(field, msg string) error { return &identity.ValidationError{Field: field, Message: msg} }
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 100 || strings.ContainsAny(r.Name, "\r\n\t\x00") {
		return verr("name", "must be 1-100 characters")
	}
	switch r.Metric {
	case MetricCPU, MetricMemory, MetricDisk:
		if r.Threshold <= 0 || r.Threshold >= 100 {
			return verr("threshold", "must be a percentage between 0 and 100")
		}
	case MetricLoad:
		if r.Threshold <= 0 || r.Threshold > 1000 {
			return verr("threshold", "must be a positive load average")
		}
	case MetricOffline:
		if r.Threshold < 1 || r.Threshold > 1440 {
			return verr("threshold", "must be 1-1440 minutes without a report")
		}
	case MetricBackup:
		r.Threshold = 0.5 // fires when the newest backup failed
	default:
		return verr("metric", "must be cpu, memory, disk, load, offline or backup")
	}
	if math.IsNaN(r.Threshold) || math.IsInf(r.Threshold, 0) {
		return verr("threshold", "must be a number")
	}
	if r.DurationMinutes < 1 || r.DurationMinutes > 1440 {
		return verr("duration_minutes", "must be 1-1440")
	}
	if r.ServerID != nil && r.GroupID != nil {
		return verr("group_id", "choose a server or a group, not both")
	}
	if r.ServerID != nil {
		x, err := s.store.GetServer(ctx, *r.ServerID)
		if err != nil || x.OrganizationID != actor.User.OrganizationID {
			return verr("server_id", "unknown server")
		}
	}
	if r.GroupID != nil {
		g, err := s.store.GetServerGroup(ctx, *r.GroupID)
		if err != nil || g.OrganizationID != actor.User.OrganizationID {
			return verr("group_id", "unknown server group")
		}
	}
	if r.ChannelIDs == nil {
		r.ChannelIDs = []string{}
	}
	slices.Sort(r.ChannelIDs)
	r.ChannelIDs = slices.Compact(r.ChannelIDs)
	for _, id := range r.ChannelIDs {
		c, err := s.store.GetChannel(ctx, id)
		if err != nil || c.OrganizationID != actor.User.OrganizationID {
			return verr("channel_ids", "unknown channel")
		}
	}
	return nil
}

func (s *Service) ListRules(ctx context.Context, actor *identity.Principal) ([]store.AlertRule, error) {
	if err := s.id.Require(ctx, actor, rbac.AlertsRead); err != nil {
		return nil, err
	}
	return s.store.ListAlertRules(ctx, actor.User.OrganizationID)
}

func (s *Service) CreateRule(ctx context.Context, actor *identity.Principal, in RuleInput) (*store.AlertRule, error) {
	if err := s.id.Require(ctx, actor, rbac.AlertsWrite); err != nil {
		return nil, err
	}
	r := &store.AlertRule{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, Name: in.Name, ServerID: in.ServerID, GroupID: in.GroupID, Metric: in.Metric,
		Threshold: in.Threshold, DurationMinutes: in.DurationMinutes, ChannelIDs: in.ChannelIDs, Enabled: in.Enabled, CreatedBy: &actor.User.ID}
	if err := s.checkRule(ctx, actor, r); err != nil {
		return nil, err
	}
	if err := s.store.CreateAlertRule(ctx, r); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "alerts.rule_create", "alert_rule", r.ID, store.OutcomeSuccess,
		map[string]any{"name": r.Name, "metric": r.Metric, "threshold": r.Threshold, "duration_minutes": r.DurationMinutes})
	return r, nil
}

func (s *Service) ruleInOrg(ctx context.Context, actor *identity.Principal, id string) (*store.AlertRule, error) {
	if err := s.id.Require(ctx, actor, rbac.AlertsWrite); err != nil {
		return nil, err
	}
	r, err := s.store.GetAlertRule(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	return r, nil
}

func (s *Service) UpdateRule(ctx context.Context, actor *identity.Principal, id string, in RuleInput) (*store.AlertRule, error) {
	r, err := s.ruleInOrg(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	r.Name, r.ServerID, r.GroupID, r.Metric, r.Threshold, r.DurationMinutes, r.ChannelIDs, r.Enabled =
		in.Name, in.ServerID, in.GroupID, in.Metric, in.Threshold, in.DurationMinutes, in.ChannelIDs, in.Enabled
	if err := s.checkRule(ctx, actor, r); err != nil {
		return nil, err
	}
	if err := s.store.UpdateAlertRule(ctx, r); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "alerts.rule_update", "alert_rule", r.ID, store.OutcomeSuccess,
		map[string]any{"name": r.Name, "metric": r.Metric, "threshold": r.Threshold, "enabled": r.Enabled})
	return r, nil
}

func (s *Service) DeleteRule(ctx context.Context, actor *identity.Principal, id string) error {
	r, err := s.ruleInOrg(ctx, actor, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteAlertRule(ctx, r.ID); err != nil {
		return err
	}
	s.id.Audit(ctx, actor.User, "alerts.rule_delete", "alert_rule", r.ID, store.OutcomeSuccess, map[string]any{"name": r.Name})
	return nil
}

func (s *Service) ListEvents(ctx context.Context, actor *identity.Principal, limit int) ([]store.AlertEvent, error) {
	if err := s.id.Require(ctx, actor, rbac.AlertsRead); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.store.ListAlertEvents(ctx, actor.User.OrganizationID, limit)
}

// ---- evaluation ----

// Evaluate checks every enabled rule once; called by the janitor.
func (s *Service) Evaluate(ctx context.Context, now time.Time) {
	rules, err := s.store.ListAlertRules(ctx, "")
	if err != nil {
		s.log.Warn("alert rules", "err", err)
		return
	}
	servers := map[string][]store.Server{}
	for i := range rules {
		r := &rules[i]
		if !r.Enabled {
			continue
		}
		var targets []store.Server
		if r.ServerID != nil {
			x, err := s.store.GetServer(ctx, *r.ServerID)
			if err != nil {
				continue
			}
			targets = []store.Server{*x}
		} else {
			list, ok := servers[r.OrganizationID]
			if !ok {
				list, _ = s.store.ListServers(ctx, r.OrganizationID)
				servers[r.OrganizationID] = list
			}
			targets = list
			if r.GroupID != nil {
				g, err := s.store.GetServerGroup(ctx, *r.GroupID)
				if err != nil {
					continue
				}
				targets = slices.DeleteFunc(slices.Clone(list), func(x store.Server) bool { return !slices.Contains(g.ServerIDs, x.ID) })
			}
		}
		for j := range targets {
			x := &targets[j]
			if x.Status != store.ServerActive {
				continue
			}
			value, known := s.measure(ctx, r, x, now)
			if !known {
				continue
			}
			s.transition(ctx, r, x, value, value > r.Threshold, now)
		}
	}
}

// measure returns the rule's current value for a server; known is false when
// there is not enough data to decide (the state is then left unchanged).
func (s *Service) measure(ctx context.Context, r *store.AlertRule, x *store.Server, now time.Time) (float64, bool) {
	switch r.Metric {
	case MetricOffline:
		if x.LastSeenAt == nil {
			return 0, false
		}
		return now.Sub(*x.LastSeenAt).Minutes(), true
	case MetricBackup:
		list, err := s.store.ListBackups(ctx, x.ID, 1)
		if err != nil || len(list) == 0 || list[0].SHA256 != nil || list[0].TaskID == nil {
			return 0, len(list) > 0 && err == nil
		}
		t, err := s.store.GetTask(ctx, *list[0].TaskID)
		if err != nil {
			return 0, false
		}
		switch t.Status {
		case store.TaskFailed, store.TaskExpired:
			return 1, true
		case store.TaskSucceeded:
			return 0, true
		}
		return 0, false // still running
	}
	window := time.Duration(r.DurationMinutes) * time.Minute
	// Agents upload every 30 s, so the newest samples can be that old: look a little further back.
	rows, err := s.store.ListMetrics(ctx, x.ID, now.Add(-window-uploadLag), 0)
	if err != nil || len(rows) < 2 {
		return 0, false
	}
	samples := make([]metrics.Sample, 0, len(rows))
	for _, row := range rows {
		var m metrics.Sample
		if json.Unmarshal(row.Data, &m) == nil {
			samples = append(samples, m)
		}
	}
	// Require the samples to cover most of the window, so a single spike right
	// after the agent starts does not count as "for N minutes".
	if len(samples) < 2 || samples[len(samples)-1].TS.Sub(samples[0].TS) < window*6/10 {
		return 0, false
	}
	var vals []float64
	for _, m := range samples {
		switch r.Metric {
		case MetricCPU:
			if m.CPUPercent != nil {
				vals = append(vals, *m.CPUPercent)
			}
		case MetricMemory:
			if m.MemUsedBytes != nil && m.MemTotalBytes != nil && *m.MemTotalBytes > 0 {
				vals = append(vals, float64(*m.MemUsedBytes)*100/float64(*m.MemTotalBytes))
			}
		case MetricLoad:
			if m.Load1 != nil {
				vals = append(vals, *m.Load1)
			}
		case MetricDisk:
			worst := -1.0
			for _, d := range m.Disks {
				if d.TotalBytes > 0 {
					worst = math.Max(worst, float64(d.UsedBytes)*100/float64(d.TotalBytes))
				}
			}
			if worst >= 0 {
				vals = append(vals, worst)
			}
		}
	}
	if len(vals) < 2 {
		return 0, false
	}
	if r.Metric == MetricDisk {
		return slices.Min(vals), true // the fullest disk stayed above the threshold for the whole window
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals)), true
}

func (s *Service) transition(ctx context.Context, r *store.AlertRule, x *store.Server, value float64, firing bool, now time.Time) {
	prev, err := s.store.GetAlertState(ctx, r.ID, x.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return
	}
	wasFiring := prev != nil && prev.Firing
	st := &store.AlertState{RuleID: r.ID, ServerID: x.ID, Firing: firing, Since: now, Value: value, UpdatedAt: now}
	if prev != nil && prev.Firing == firing {
		st.Since = prev.Since
	}
	if err := s.store.PutAlertState(ctx, st); err != nil {
		return
	}
	if firing == wasFiring || (prev == nil && !firing) {
		return
	}
	state := "resolved"
	if firing {
		state = "firing"
	}
	ev := &store.AlertEvent{ID: auth.NewID(), OrganizationID: r.OrganizationID, RuleID: &r.ID, RuleName: r.Name, ServerID: &x.ID,
		ServerName: x.Name, Metric: r.Metric, State: state, Value: math.Round(value*10) / 10, Threshold: r.Threshold}
	var channels []store.NotificationChannel
	for _, id := range r.ChannelIDs {
		if c, err := s.store.GetChannel(ctx, id); err == nil && c.Enabled {
			channels = append(channels, *c)
		}
	}
	if s.queue != nil {
		ev.Pending = len(channels)
	}
	if err := s.store.InsertAlertEvent(ctx, ev); err != nil {
		s.log.Warn("alert event", "err", err)
		return
	}
	s.id.Audit(ctx, nil, "alerts."+state, "alert_rule", r.ID, store.OutcomeSuccess,
		map[string]any{"rule": r.Name, "server": x.Name, "metric": r.Metric, "value": ev.Value, "threshold": r.Threshold})
	if s.queue == nil {
		s.deliver(ctx, ev, channels, false)
		return
	}
	for i := range channels {
		c := &channels[i]
		if _, err := s.queue.Enqueue(ctx, notifyJob, notifyPayload{EventID: ev.ID, ChannelID: c.ID}, 0); err != nil {
			_ = s.store.RecordAlertDelivery(ctx, ev.ID, false, c.Name+": could not be queued: "+err.Error())
		}
	}
}

// ---- queued delivery ----

const notifyJob = "notification.deliver"

type notifyPayload struct {
	EventID   string `json:"event_id"`
	ChannelID string `json:"channel_id"`
}

// UseQueue delivers alert notifications through the durable job queue, with
// retries. Test notifications stay synchronous.
func (s *Service) UseQueue(q *jobs.Queue) {
	s.queue = q
	q.Register(notifyJob, notifyHandler{s})
}

// UseLessor makes Run evaluate only in the API process holding the "monitor" lease.
func (s *Service) UseLessor(l *jobs.Lessor) { s.lessor = l }

type notifyHandler struct{ s *Service }

func (h notifyHandler) Run(ctx context.Context, j jobs.Job) error {
	s := h.s
	var p notifyPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return jobs.Permanent(fmt.Errorf("invalid payload: %w", err))
	}
	ev, err := s.store.GetAlertEvent(ctx, p.EventID)
	if errors.Is(err, store.ErrNotFound) {
		return jobs.Permanent(errors.New("the alert event no longer exists"))
	}
	if err != nil {
		return err
	}
	c, err := s.store.GetChannel(ctx, p.ChannelID)
	if errors.Is(err, store.ErrNotFound) {
		return jobs.Permanent(errors.New("the channel was deleted"))
	}
	if err != nil {
		return err
	}
	if !c.Enabled {
		return jobs.Permanent(fmt.Errorf("%s: the channel was disabled", c.Name))
	}
	secret := ""
	if len(c.SecretEnc) > 0 {
		plain, err := s.keys.Decrypt(c.SecretEnc, channelAAD(c.ID))
		if err != nil {
			return jobs.Permanent(fmt.Errorf("%s: cannot decrypt the channel secret", c.Name))
		}
		secret = string(plain)
	}
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.send(sctx, c, secret, ev); err != nil {
		return fmt.Errorf("%s: %w", c.Name, err)
	}
	if err := s.store.RecordAlertDelivery(ctx, ev.ID, true, ""); err != nil {
		s.log.Warn("record delivery", "event", ev.ID, "err", err)
	}
	return nil
}

func (h notifyHandler) GiveUp(ctx context.Context, j jobs.Job, err error) {
	var p notifyPayload
	if json.Unmarshal(j.Payload, &p) != nil {
		return
	}
	msg := err.Error()
	if !jobs.IsPermanent(err) {
		msg = fmt.Sprintf("%s (gave up after %d attempts)", msg, j.Attempt)
	}
	if err := h.s.store.RecordAlertDelivery(ctx, p.EventID, false, msg); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.s.log.Warn("record delivery", "event", p.EventID, "err", err)
	}
}

// deliver sends ev to every channel and records the outcome on the event.
func (s *Service) deliver(ctx context.Context, ev *store.AlertEvent, channels []store.NotificationChannel, includeDisabled bool) {
	delivered, failed := 0, 0
	var errs []string
	for i := range channels {
		c := &channels[i]
		if !c.Enabled && !includeDisabled {
			continue
		}
		secret := ""
		if len(c.SecretEnc) > 0 {
			plain, err := s.keys.Decrypt(c.SecretEnc, channelAAD(c.ID))
			if err != nil {
				failed++
				errs = append(errs, c.Name+": cannot decrypt the channel secret")
				continue
			}
			secret = string(plain)
		}
		sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.send(sctx, c, secret, ev)
		cancel()
		if err != nil {
			failed++
			errs = append(errs, c.Name+": "+err.Error())
			continue
		}
		delivered++
	}
	msg := strings.Join(errs, "; ")
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	ev.Delivered, ev.Failed, ev.DeliveryError = delivered, failed, msg
	_ = s.store.SetAlertEventDelivery(ctx, ev.ID, delivered, failed, msg)
}

// channelTarget describes where a channel delivers without secrets: webhook
// URLs often carry a token in their path, so only the host is recorded.
func channelTarget(c *store.NotificationChannel) string {
	switch c.Kind {
	case "webhook":
		var w WebhookConfig
		_ = json.Unmarshal(c.Config, &w)
		if u, err := url.Parse(w.URL); err == nil {
			return u.Scheme + "://" + u.Host
		}
	case "email":
		var e EmailConfig
		_ = json.Unmarshal(c.Config, &e)
		return fmt.Sprintf("smtp %s:%d, %d recipient(s)", e.Host, e.Port, len(e.To))
	case "plugin":
		var p PluginConfig
		_ = json.Unmarshal(c.Config, &p)
		return "plugin " + p.Plugin // settings may hold tokens
	}
	return ""
}

// MaskedConfig returns the channel config for readers without alerts.write:
// the webhook path and the SMTP user name are hidden.
func MaskedConfig(c *store.NotificationChannel) json.RawMessage {
	switch c.Kind {
	case "webhook":
		out, _ := json.Marshal(WebhookConfig{URL: channelTarget(c) + "/…"})
		return out
	case "email":
		var e EmailConfig
		_ = json.Unmarshal(c.Config, &e)
		if e.Username != "" {
			e.Username = "…"
		}
		out, _ := json.Marshal(e)
		return out
	case "plugin":
		var p PluginConfig
		_ = json.Unmarshal(c.Config, &p)
		out, _ := json.Marshal(PluginConfig{Plugin: p.Plugin})
		return out
	}
	return json.RawMessage("{}")
}

// ---- plugin channels ----

var pluginName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// UsePlugins enables notification channels of kind plugin.
func (s *Service) UsePlugins(r *plugins.Registry) { s.plugins = r }

func (s *Service) checkPlugin(kind string, cfg json.RawMessage) error {
	if kind != "plugin" {
		return nil
	}
	var c PluginConfig
	_ = json.Unmarshal(cfg, &c)
	if !s.plugins.Enabled() {
		return &identity.PolicyError{Message: plugins.ErrDisabled.Error()}
	}
	if !s.plugins.HasNotifier(c.Plugin) {
		return &identity.ValidationError{Field: "config", Message: "plugin " + c.Plugin + " is not an installed, valid notifier"}
	}
	return nil
}

// send delivers one event; plugin channels run through the plugin registry.
func (s *Service) send(ctx context.Context, c *store.NotificationChannel, secret string, ev *store.AlertEvent) error {
	if c.Kind != "plugin" {
		return s.sender.Send(ctx, c, secret, ev)
	}
	var pc PluginConfig
	if err := json.Unmarshal(c.Config, &pc); err != nil {
		return jobs.Permanent(err)
	}
	if s.plugins == nil {
		return jobs.Permanent(plugins.ErrDisabled)
	}
	err := s.plugins.Notify(ctx, pc.Plugin, pc.Settings, secret, pluginEvent(ev))
	var ce *plugins.CallError
	if errors.As(err, &ce) && ce.Permanent {
		return jobs.Permanent(err)
	}
	return err
}

func pluginEvent(ev *store.AlertEvent) pluginsdk.Event {
	p := pluginsdk.Event{Event: "alert." + ev.State, ID: ev.ID, Rule: ev.RuleName, Server: ev.ServerName, Metric: ev.Metric,
		Value: ev.Value, Threshold: ev.Threshold, Message: Describe(ev), At: ev.CreatedAt.UTC().Format(time.RFC3339)}
	if ev.ServerID != nil {
		p.ServerID = *ev.ServerID
	}
	return p
}
