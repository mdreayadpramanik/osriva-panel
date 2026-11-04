package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// Encrypted backups (docs/adr/0010-backups.md). Each server has one age key
// pair; the agent only receives the recipient. The identity is stored
// encrypted with the master key and sent to the agent only to verify or restore.

func backupKeyAAD(serverID string) []byte { return []byte("osriva/backup-key/" + serverID) }

// backupKey returns the server's recipient and identity, creating them on first use.
func (s *Service) backupKey(ctx context.Context, serverID string) (string, string, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	enc, err := s.keys.Encrypt([]byte(id.String()), backupKeyAAD(serverID))
	if err != nil {
		return "", "", err
	}
	k, err := s.store.EnsureBackupKey(ctx, &store.BackupKey{ServerID: serverID, Recipient: id.Recipient().String(), IdentityEnc: enc})
	if err != nil {
		return "", "", err
	}
	plain, err := s.keys.Decrypt(k.IdentityEnc, backupKeyAAD(serverID))
	if err != nil {
		return "", "", fmt.Errorf("backup key: %w", err)
	}
	return k.Recipient, string(plain), nil
}

type BackupView struct {
	store.Backup
	Status store.TaskStatus
	Error  string
}

// syncBackup copies size and checksums from a finished create task into the row.
func (s *Service) syncBackup(ctx context.Context, b *store.Backup) BackupView {
	v := BackupView{Backup: *b, Status: store.TaskExpired, Error: "the task record is gone"}
	if b.SHA256 != nil {
		v.Status, v.Error = store.TaskSucceeded, ""
		return v
	}
	if b.TaskID == nil {
		return v
	}
	t, err := s.store.GetTask(ctx, *b.TaskID)
	if err != nil {
		return v
	}
	v.Status, v.Error = t.Status, t.Error
	if t.Status == store.TaskSucceeded {
		var r agenttask.BackupCreateResult
		if json.Unmarshal(t.Result, &r) == nil && r.SHA256 != "" {
			if s.store.SetBackupResult(ctx, b.ID, r.Size, r.SHA256, r.PlainSHA256, r.Files) == nil {
				v.Size, v.SHA256, v.PlainSHA256, v.Files = &r.Size, &r.SHA256, &r.PlainSHA256, &r.Files
			}
		}
	}
	return v
}

type BackupsView struct {
	Backups  []BackupView
	Policies []store.BackupPolicy
}

func (s *Service) ListBackups(ctx context.Context, actor *identity.Principal, serverID string) (*BackupsView, error) {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersRead, serverID, "")
	if err != nil {
		return nil, err
	}
	list, err := s.store.ListBackups(ctx, x.ID, 200)
	if err != nil {
		return nil, err
	}
	v := &BackupsView{Backups: make([]BackupView, len(list))}
	for i := range list {
		v.Backups[i] = s.syncBackup(ctx, &list[i])
	}
	if v.Policies, err = s.store.ListBackupPolicies(ctx, x.ID); err != nil {
		return nil, err
	}
	return v, nil
}

func validBackupTarget(kind, target string) error {
	raw, _ := json.Marshal(agenttask.BackupCreateParams{ID: "check", Kind: kind, Target: target, Recipient: "age1" + strings.Repeat("q", 58)})
	if err := agenttask.Validate(agenttask.BackupCreate, raw); err != nil {
		return validationErr("backup", err)
	}
	return nil
}

// newBackup queues a backup; actor is nil for scheduled backups.
func (s *Service) newBackup(ctx context.Context, actor *identity.Principal, x *store.Server, kind, target string, policyID *string) (*BackupView, error) {
	if err := validBackupTarget(kind, target); err != nil {
		return nil, err
	}
	recipient, _, err := s.backupKey(ctx, x.ID)
	if err != nil {
		return nil, err
	}
	b := &store.Backup{ID: auth.NewID(), ServerID: x.ID, Kind: kind, Target: target, PolicyID: policyID}
	raw, _ := json.Marshal(agenttask.BackupCreateParams{ID: b.ID, Kind: kind, Target: target, Recipient: recipient})
	var t *store.Task
	if actor != nil {
		b.CreatedBy = &actor.User.ID
		t, err = s.QueueTask(ctx, actor, x.ID, agenttask.BackupCreate, raw)
	} else {
		t, err = s.queueSystemTask(ctx, x, agenttask.BackupCreate, raw)
	}
	if err != nil {
		return nil, err
	}
	b.TaskID = &t.ID
	if err := s.store.CreateBackup(ctx, b); err != nil {
		return nil, err
	}
	v := s.syncBackup(ctx, b)
	return &v, nil
}

func (s *Service) CreateBackup(ctx context.Context, actor *identity.Principal, serverID, kind, target string) (*BackupView, error) {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, "")
	if err != nil {
		return nil, err
	}
	return s.newBackup(ctx, actor, x, kind, target, nil)
}

func (s *Service) backupInOrg(ctx context.Context, actor *identity.Principal, serverID, id string) (*store.Server, *BackupView, error) {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, "")
	if err != nil {
		return nil, nil, err
	}
	b, err := s.store.GetBackup(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if b.ServerID != x.ID {
		return nil, nil, store.ErrNotFound
	}
	v := s.syncBackup(ctx, b)
	return x, &v, nil
}

func (s *Service) VerifyBackup(ctx context.Context, actor *identity.Principal, serverID, id string) (*store.Task, error) {
	x, b, err := s.backupInOrg(ctx, actor, serverID, id)
	if err != nil {
		return nil, err
	}
	if b.SHA256 == nil {
		return nil, &identity.PolicyError{Message: "this backup has not completed"}
	}
	_, key, err := s.backupKey(ctx, x.ID)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(agenttask.BackupVerifyParams{ID: b.ID, SHA256: *b.SHA256, BackupKey: key})
	return s.QueueTask(ctx, actor, x.ID, agenttask.BackupVerify, raw)
}

// RestoreBackup replaces the live data with the backup (re-authentication required).
func (s *Service) RestoreBackup(ctx context.Context, actor *identity.Principal, serverID, id string) (*store.Task, error) {
	x, b, err := s.backupInOrg(ctx, actor, serverID, id)
	if err != nil {
		return nil, err
	}
	if b.SHA256 == nil {
		return nil, &identity.PolicyError{Message: "this backup has not completed"}
	}
	_, key, err := s.backupKey(ctx, x.ID)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(agenttask.BackupRestoreParams{ID: b.ID, Kind: b.Kind, Target: b.Target, SHA256: *b.SHA256, BackupKey: key})
	t, err := s.QueueTask(ctx, actor, x.ID, agenttask.BackupRestore, raw)
	if err != nil {
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "backups.restore", "backup", b.ID, store.OutcomeSuccess, map[string]any{"server": x.Name,
		"kind": b.Kind, "target": b.Target, "task_id": t.ID})
	return t, nil
}

func (s *Service) DeleteBackup(ctx context.Context, actor *identity.Principal, serverID, id string) (*store.Task, error) {
	x, b, err := s.backupInOrg(ctx, actor, serverID, id)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(agenttask.BackupRefParams{ID: b.ID})
	t, err := s.QueueTask(ctx, actor, x.ID, agenttask.BackupDelete, raw)
	if err != nil {
		return nil, err
	}
	return t, s.store.DeleteBackup(ctx, b.ID)
}

// ExportRecoveryKey returns the server's backup identity so that the owner can
// keep it outside OSRIVA (backups cannot be decrypted without it).
func (s *Service) ExportRecoveryKey(ctx context.Context, actor *identity.Principal, serverID string) (string, error) {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, "")
	if err != nil {
		return "", err
	}
	if err := s.id.RequireRecentReauth(actor); err != nil {
		return "", err
	}
	_, key, err := s.backupKey(ctx, x.ID)
	if err != nil {
		return "", err
	}
	s.id.Audit(ctx, actor.User, "backups.export_key", "server", x.ID, store.OutcomeSuccess, map[string]any{"server": x.Name})
	return key, nil
}

func (s *Service) CreateBackupPolicy(ctx context.Context, actor *identity.Principal, serverID, kind, target string, intervalHours, keep int) (*store.BackupPolicy, error) {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, "")
	if err != nil {
		return nil, err
	}
	if err := validBackupTarget(kind, target); err != nil {
		return nil, err
	}
	if intervalHours < 1 || intervalHours > 720 || keep < 1 || keep > 100 {
		return nil, &identity.ValidationError{Field: "policy", Message: "interval must be 1-720 hours and keep 1-100 backups"}
	}
	p := &store.BackupPolicy{ID: auth.NewID(), ServerID: x.ID, Kind: kind, Target: target, IntervalHours: intervalHours, Keep: keep, CreatedBy: &actor.User.ID}
	if err := s.store.CreateBackupPolicy(ctx, p); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, &identity.ValidationError{Field: "policy", Message: "this target already has a schedule"}
		}
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "backups.policy_create", "server", x.ID, store.OutcomeSuccess, map[string]any{"kind": kind, "target": target,
		"interval_hours": intervalHours, "keep": keep})
	return p, nil
}

func (s *Service) DeleteBackupPolicy(ctx context.Context, actor *identity.Principal, serverID, id string) error {
	x, _, err := s.appInOrg(ctx, actor, rbac.ServersOperate, serverID, "")
	if err != nil {
		return err
	}
	p, err := s.store.GetBackupPolicy(ctx, id)
	if err != nil {
		return err
	}
	if p.ServerID != x.ID {
		return store.ErrNotFound
	}
	return s.store.DeleteBackupPolicy(ctx, id)
}

// RunBackupSchedule starts due scheduled backups and prunes old ones. Called by the janitor.
func (s *Service) RunBackupSchedule(ctx context.Context, now time.Time) {
	policies, err := s.store.ListBackupPolicies(ctx, "")
	if err != nil {
		s.log.Warn("backup schedule", "err", err)
		return
	}
	for i := range policies {
		p := &policies[i]
		x, err := s.store.GetServer(ctx, p.ServerID)
		if err != nil || x.Status != store.ServerActive {
			continue
		}
		if p.LastRunAt == nil || !now.Before(p.LastRunAt.Add(time.Duration(p.IntervalHours)*time.Hour)) {
			if _, err := s.newBackup(ctx, nil, x, p.Kind, p.Target, &p.ID); err != nil {
				s.log.Warn("scheduled backup", "policy", p.ID, "err", err)
			}
			_ = s.store.MarkBackupPolicyRun(ctx, p.ID, now)
		}
		s.pruneBackups(ctx, x, p)
	}
}

// pruneBackups keeps the newest p.Keep successful backups of a policy.
func (s *Service) pruneBackups(ctx context.Context, x *store.Server, p *store.BackupPolicy) {
	list, err := s.store.ListBackups(ctx, x.ID, 0)
	if err != nil {
		return
	}
	kept := 0
	for i := range list {
		b := &list[i]
		if b.PolicyID == nil || *b.PolicyID != p.ID {
			continue
		}
		if v := s.syncBackup(ctx, b); v.Status != store.TaskSucceeded {
			continue
		}
		if kept++; kept <= p.Keep {
			continue
		}
		raw, _ := json.Marshal(agenttask.BackupRefParams{ID: b.ID})
		if _, err := s.queueSystemTask(ctx, x, agenttask.BackupDelete, raw); err == nil {
			_ = s.store.DeleteBackup(ctx, b.ID)
		}
	}
}

// queueSystemTask queues a task on behalf of OSRIVA itself (schedules), audited without an actor.
func (s *Service) queueSystemTask(ctx context.Context, x *store.Server, typ string, params json.RawMessage) (*store.Task, error) {
	if err := agenttask.Validate(typ, params); err != nil {
		return nil, err
	}
	now := s.id.Now()
	t := &store.Task{ID: auth.NewID(), ServerID: x.ID, Type: typ, Params: params, CreatedAt: now,
		ExpiresAt: now.Add(taskQueueTTL), RunTimeout: agenttask.RunTimeout(typ)}
	if err := s.store.CreateTask(ctx, t); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, nil, "servers.task", "server", x.ID, store.OutcomeSuccess,
		map[string]any{"task_id": t.ID, "type": typ, "params": RedactedParams(params), "server": x.Name, "scheduled": true})
	s.notify.wake(x.ID)
	return t, nil
}

// QueueSystemTask queues a validated task on a server for a caller that is not
// an operator (scheduled jobs, the Customer Panel); the caller checks ownership.
func (s *Service) QueueSystemTask(ctx context.Context, serverID, typ string, params json.RawMessage) (*store.Task, error) {
	x, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if x.Status != store.ServerActive {
		return nil, &identity.PolicyError{Message: "this server's agent is not enrolled or has been revoked"}
	}
	return s.queueSystemTask(ctx, x, typ, params)
}
