package memstore

import (
	"bytes"
	"context"
	"sort"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func (s *Store) EnsureBackupKey(_ context.Context, k *store.BackupKey) (*store.BackupKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[k.ServerID]; !ok {
		return nil, store.ErrNotFound
	}
	if cur, ok := s.backupKeys[k.ServerID]; ok {
		c := *cur
		c.IdentityEnc = bytes.Clone(cur.IdentityEnc)
		return &c, nil
	}
	c := *k
	c.IdentityEnc = bytes.Clone(k.IdentityEnc)
	s.backupKeys[k.ServerID] = &c
	out := c
	return &out, nil
}

func (s *Store) CreateBackup(_ context.Context, b *store.Backup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[b.ServerID]; !ok {
		return store.ErrNotFound
	}
	if _, dup := s.backups[b.ID]; dup {
		return store.ErrConflict
	}
	b.CreatedAt = time.Now().UTC()
	c := *b
	s.backups[b.ID] = &c
	return nil
}

func (s *Store) GetBackup(_ context.Context, id string) (*store.Backup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.backups[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *b
	if c.PolicyID != nil {
		if _, ok := s.policies[*c.PolicyID]; !ok {
			c.PolicyID = nil // ON DELETE SET NULL
		}
	}
	return &c, nil
}

func (s *Store) ListBackups(ctx context.Context, serverID string, limit int) ([]store.Backup, error) {
	s.mu.RLock()
	ids := []string{}
	for id, b := range s.backups {
		if b.ServerID == serverID {
			ids = append(ids, id)
		}
	}
	s.mu.RUnlock()
	out := []store.Backup{}
	for _, id := range ids {
		b, _ := s.GetBackup(ctx, id)
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) SetBackupResult(_ context.Context, id string, size int64, sha, plainSHA string, files int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.backups[id]
	if !ok {
		return store.ErrNotFound
	}
	b.Size, b.SHA256, b.PlainSHA256, b.Files = &size, &sha, &plainSHA, &files
	return nil
}

func (s *Store) DeleteBackup(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.backups[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.backups, id)
	return nil
}

func (s *Store) CreateBackupPolicy(_ context.Context, p *store.BackupPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[p.ServerID]; !ok {
		return store.ErrNotFound
	}
	for _, o := range s.policies {
		if o.ID == p.ID || (o.ServerID == p.ServerID && o.Kind == p.Kind && o.Target == p.Target) {
			return store.ErrConflict
		}
	}
	p.CreatedAt = time.Now().UTC()
	c := *p
	s.policies[p.ID] = &c
	return nil
}

func (s *Store) GetBackupPolicy(_ context.Context, id string) (*store.BackupPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.policies[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *p
	return &c, nil
}

func (s *Store) ListBackupPolicies(_ context.Context, serverID string) ([]store.BackupPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.BackupPolicy{}
	for _, p := range s.policies {
		if serverID == "" || p.ServerID == serverID {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out, nil
}

func (s *Store) MarkBackupPolicyRun(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.policies[id]
	if !ok {
		return store.ErrNotFound
	}
	t := at
	p.LastRunAt = &t
	return nil
}

func (s *Store) DeleteBackupPolicy(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.policies[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.policies, id)
	return nil
}
