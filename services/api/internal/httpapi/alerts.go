package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/monitor"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

var errMonitorDisabled = errors.New("alerts are not available")

type channelDTO struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Config    json.RawMessage `json:"config"`
	HasSecret bool            `json:"has_secret"`
	Enabled   bool            `json:"enabled"`
	CreatedAt string          `json:"created_at"`
}

type ruleDTO struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	ServerID        *string  `json:"server_id"`
	Metric          string   `json:"metric"`
	Threshold       float64  `json:"threshold"`
	DurationMinutes int      `json:"duration_minutes"`
	ChannelIDs      []string `json:"channel_ids"`
	Enabled         bool     `json:"enabled"`
	CreatedAt       string   `json:"created_at"`
}

type alertEventDTO struct {
	ID            string  `json:"id"`
	RuleID        *string `json:"rule_id"`
	RuleName      string  `json:"rule_name"`
	ServerID      *string `json:"server_id"`
	ServerName    string  `json:"server_name"`
	Metric        string  `json:"metric"`
	State         string  `json:"state"`
	Value         float64 `json:"value"`
	Threshold     float64 `json:"threshold"`
	Message       string  `json:"message"`
	Delivered     int     `json:"delivered"`
	Failed        int     `json:"failed"`
	DeliveryError string  `json:"delivery_error"`
	CreatedAt     string  `json:"created_at"`
}

func (s *Server) toChannelDTO(r *http.Request, c *store.NotificationChannel) channelDTO {
	cfg := c.Config
	if !principalFrom(r.Context()).Can(rbac.AlertsWrite) {
		cfg = monitor.MaskedConfig(c) // webhook paths and SMTP user names stay with the managers
	}
	return channelDTO{ID: c.ID, Name: c.Name, Kind: c.Kind, Config: cfg, HasSecret: len(c.SecretEnc) > 0, Enabled: c.Enabled, CreatedAt: ts(c.CreatedAt)}
}

func toRuleDTO(r *store.AlertRule) ruleDTO {
	return ruleDTO{ID: r.ID, Name: r.Name, ServerID: r.ServerID, Metric: r.Metric, Threshold: r.Threshold, DurationMinutes: r.DurationMinutes,
		ChannelIDs: r.ChannelIDs, Enabled: r.Enabled, CreatedAt: ts(r.CreatedAt)}
}

func toAlertEventDTO(e *store.AlertEvent) alertEventDTO {
	return alertEventDTO{ID: e.ID, RuleID: e.RuleID, RuleName: e.RuleName, ServerID: e.ServerID, ServerName: e.ServerName, Metric: e.Metric,
		State: e.State, Value: e.Value, Threshold: e.Threshold, Message: monitor.Describe(e), Delivered: e.Delivered, Failed: e.Failed,
		DeliveryError: e.DeliveryError, CreatedAt: ts(e.CreatedAt)}
}

func (s *Server) monitorOr503(w http.ResponseWriter, r *http.Request) bool {
	if s.monitor == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "unavailable", errMonitorDisabled.Error(), "")
		return false
	}
	return true
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	if !s.monitorOr503(w, r) {
		return
	}
	list, err := s.monitor.ListChannels(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]channelDTO, len(list))
	for i := range list {
		out[i] = s.toChannelDTO(r, &list[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	if !s.monitorOr503(w, r) {
		return
	}
	var in monitor.ChannelInput
	if !s.decode(w, r, &in) {
		return
	}
	c, err := s.monitor.CreateChannel(r.Context(), principalFrom(r.Context()), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	body := map[string]any{"channel": s.toChannelDTO(r, &c.NotificationChannel)}
	if c.WebhookSecret != "" {
		body["webhook_secret"] = c.WebhookSecret // shown once; used by the receiver to check signatures
	}
	writeJSON(w, http.StatusCreated, body)
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if !s.monitorOr503(w, r) {
		return
	}
	var in monitor.ChannelInput
	if !s.decode(w, r, &in) {
		return
	}
	c, err := s.monitor.UpdateChannel(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "channelId"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toChannelDTO(r, c))
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	if !s.monitorOr503(w, r) {
		return
	}
	if err := s.monitor.DeleteChannel(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "channelId")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	if !s.monitorOr503(w, r) {
		return
	}
	ev, err := s.monitor.TestChannel(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "channelId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAlertEventDTO(ev))
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	if !s.monitorOr503(w, r) {
		return
	}
	list, err := s.monitor.ListRules(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]ruleDTO, len(list))
	for i := range list {
		out[i] = toRuleDTO(&list[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	if !s.monitorOr503(w, r) {
		return
	}
	var in monitor.RuleInput
	if !s.decode(w, r, &in) {
		return
	}
	rule, err := s.monitor.CreateRule(r.Context(), principalFrom(r.Context()), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRuleDTO(rule))
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	if !s.monitorOr503(w, r) {
		return
	}
	var in monitor.RuleInput
	if !s.decode(w, r, &in) {
		return
	}
	rule, err := s.monitor.UpdateRule(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "ruleId"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRuleDTO(rule))
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	if !s.monitorOr503(w, r) {
		return
	}
	if err := s.monitor.DeleteRule(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "ruleId")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListAlertEvents(w http.ResponseWriter, r *http.Request) {
	if !s.monitorOr503(w, r) {
		return
	}
	list, err := s.monitor.ListEvents(r.Context(), principalFrom(r.Context()), queryInt(r, "limit"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]alertEventDTO, len(list))
	for i := range list {
		out[i] = toAlertEventDTO(&list[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

func (s *Server) handleSetAuditRetention(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Days int `json:"days"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	p := principalFrom(r.Context())
	if err := s.id.SetAuditRetention(r.Context(), p, in.Days); err != nil {
		s.fail(w, r, err)
		return
	}
	st, err := s.id.GetSettings(r.Context(), p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
