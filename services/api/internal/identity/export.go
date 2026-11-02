package identity

import (
	"context"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// Audit records an event on behalf of another service (e.g. fleet). actor is
// nil for events caused by agents or the system.
func (s *Service) Audit(ctx context.Context, actor *store.User, action, targetType, targetID string, outcome store.AuditOutcome, metadata map[string]any) {
	s.audit(ctx, auditEvent{actor: actor, action: action, targetType: targetType, targetID: targetID, outcome: outcome, metadata: metadata})
}

// Now is the service clock (overridable in tests).
func (s *Service) Now() time.Time { return s.now() }
