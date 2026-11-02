package identity

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/osriva/osriva-panel/services/api/internal/rbac"
	"github.com/osriva/osriva-panel/services/api/internal/store"
)

// Panel operating modes. Provider Mode features arrive in Phase 10; the
// setting exists now so the dashboard can shape navigation around it.
const (
	ModePersonal = "personal"
	ModeProvider = "provider"

	settingPanelMode = "panel.mode"
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
	PanelMode string `json:"panel_mode"`
}

func (s *Service) GetSettings(ctx context.Context, actor *Principal) (*Settings, error) {
	if err := s.Require(ctx, actor, rbac.SettingsRead); err != nil {
		return nil, err
	}
	mode, err := s.PanelMode(ctx)
	if err != nil {
		return nil, err
	}
	return &Settings{PanelMode: mode}, nil
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
