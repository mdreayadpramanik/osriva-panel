package identity

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// Panel operating modes. Provider Mode features arrive in Phase 10; the
// setting exists now so the dashboard can shape navigation around it.
const (
	ModePersonal = "personal"
	ModeProvider = "provider"

	settingPanelMode      = "panel.mode"
	settingAuditRetention = "audit.retention_days"

	DefaultAuditRetentionDays = 365
	MinAuditRetentionDays     = 30
	MaxAuditRetentionDays     = 3650
)

// PanelMode returns the configured mode (personal by default). Readable by
// every authenticated user because it only shapes navigation.
func (s *Service) PanelMode(ctx context.Context) (string, error) {
	raw, err := s.store.GetSetting(ctx, settingPanelMode)
	if errors.Is(err, store.ErrNotFound) {
		return ModePersonal, nil
	}
	if err != nil {
		return "", err
	}
	var mode string
	if err := json.Unmarshal(raw, &mode); err != nil || (mode != ModePersonal && mode != ModeProvider) {
		s.log.Warn("invalid panel.mode setting, falling back to personal", "value", string(raw))
		return ModePersonal, nil
	}
	return mode, nil
}

type Settings struct {
	PanelMode          string `json:"panel_mode"`
	AuditRetentionDays int    `json:"audit_retention_days"`
}

func (s *Service) GetSettings(ctx context.Context, actor *Principal) (*Settings, error) {
	if err := s.Require(ctx, actor, rbac.SettingsRead); err != nil {
		return nil, err
	}
	mode, err := s.PanelMode(ctx)
	if err != nil {
		return nil, err
	}
	days, err := s.AuditRetentionDays(ctx)
	if err != nil {
		return nil, err
	}
	return &Settings{PanelMode: mode, AuditRetentionDays: days}, nil
}

func (s *Service) SetPanelMode(ctx context.Context, actor *Principal, mode string) error {
	if err := s.Require(ctx, actor, rbac.SettingsWrite); err != nil {
		return err
	}
	if mode != ModePersonal && mode != ModeProvider {
		return &ValidationError{Field: "mode", Message: "must be personal or provider"}
	}
	if err := s.RequireRecentReauth(actor); err != nil {
		return err
	}
	prev, err := s.PanelMode(ctx)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(mode)
	if err := s.store.PutSetting(ctx, settingPanelMode, raw, &actor.User.ID); err != nil {
		return err
	}
	s.audit(ctx, auditEvent{actor: actor.User, action: "settings.panel_mode", outcome: store.OutcomeSuccess,
		metadata: map[string]any{"from": prev, "to": mode}})
	return nil
}

// AuditRetentionDays is how long audit entries are kept (365 days by default).
func (s *Service) AuditRetentionDays(ctx context.Context) (int, error) {
	raw, err := s.store.GetSetting(ctx, settingAuditRetention)
	if errors.Is(err, store.ErrNotFound) {
		return DefaultAuditRetentionDays, nil
	}
	if err != nil {
		return 0, err
	}
	var days int
	if json.Unmarshal(raw, &days) != nil || days < MinAuditRetentionDays || days > MaxAuditRetentionDays {
		return DefaultAuditRetentionDays, nil
	}
	return days, nil
}

// SetAuditRetention changes the retention; shortening it deletes entries at
// the next cleanup, so it needs settings.write and a recent re-authentication.
func (s *Service) SetAuditRetention(ctx context.Context, actor *Principal, days int) error {
	if err := s.Require(ctx, actor, rbac.SettingsWrite); err != nil {
		return err
	}
	if days < MinAuditRetentionDays || days > MaxAuditRetentionDays {
		return &ValidationError{Field: "days", Message: "must be between 30 and 3650"}
	}
	if err := s.RequireRecentReauth(actor); err != nil {
		return err
	}
	prev, err := s.AuditRetentionDays(ctx)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(days)
	if err := s.store.PutSetting(ctx, settingAuditRetention, raw, &actor.User.ID); err != nil {
		return err
	}
	s.audit(ctx, auditEvent{actor: actor.User, action: "settings.audit_retention", outcome: store.OutcomeSuccess,
		metadata: map[string]any{"from": prev, "to": days}})
	return nil
}
