package memstore

import (
	"bytes"
	"context"
	"slices"
	"sort"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func copyGroup(g *store.ServerGroup) *store.ServerGroup {
	c := *g
	c.ServerIDs = slices.Clone(g.ServerIDs)
	if c.ServerIDs == nil {
		c.ServerIDs = []string{}
	}
	return &c
}

func (s *Store) groupNameTakenLocked(orgID, name, exceptID string) bool {
	for _, g := range s.groups {
		if g.OrganizationID == orgID && g.Name == name && g.ID != exceptID {
			return true
		}
	}
	return false
}

func (s *Store) CreateServerGroup(_ context.Context, g *store.ServerGroup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.groups[g.ID]; dup || s.groupNameTakenLocked(g.OrganizationID, g.Name, "") {
		return store.ErrConflict
	}
	g.CreatedAt = time.Now().UTC()
	g.ServerIDs = []string{}
	s.groups[g.ID] = copyGroup(g)
	return nil
}

func (s *Store) GetServerGroup(_ context.Context, id string) (*store.ServerGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.groups[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyGroup(g), nil
}

func (s *Store) ListServerGroups(_ context.Context, orgID string) ([]store.ServerGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.ServerGroup{}
	for _, g := range s.groups {
		if g.OrganizationID == orgID {
			out = append(out, *copyGroup(g))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) UpdateServerGroup(_ context.Context, g *store.ServerGroup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.groups[g.ID]
	if !ok {
		return store.ErrNotFound
	}
	if s.groupNameTakenLocked(old.OrganizationID, g.Name, g.ID) {
		return store.ErrConflict
	}
	old.Name, old.Description = g.Name, g.Description
	*g = *copyGroup(old)
	return nil
}

func (s *Store) DeleteServerGroup(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groups[id]; !ok {
		return store.ErrNotFound
	}
	for _, r := range s.rules {
		if r.GroupID != nil && *r.GroupID == id {
			return store.ErrConflict
		}
	}
	delete(s.groups, id)
	for _, b := range s.batches {
		if b.GroupID != nil && *b.GroupID == id {
			b.GroupID = nil
		}
	}
	return nil
}

func (s *Store) SetServerGroupMembers(_ context.Context, groupID string, serverIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupID]
	if !ok {
		return store.ErrNotFound
	}
	ids := slices.Clone(serverIDs)
	for _, id := range ids {
		if _, ok := s.servers[id]; !ok {
			return store.ErrNotFound
		}
	}
	sort.Strings(ids)
	g.ServerIDs = slices.Compact(ids)
	return nil
}

func copyBatch(b *store.TaskBatch) *store.TaskBatch {
	c := *b
	c.Params, c.Skipped = bytes.Clone(b.Params), bytes.Clone(b.Skipped)
	return &c
}

func (s *Store) CreateTaskBatch(_ context.Context, b *store.TaskBatch, tasks []*store.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.batches[b.ID]; dup {
		return store.ErrConflict
	}
	for _, t := range tasks {
		if _, ok := s.servers[t.ServerID]; !ok {
			return store.ErrNotFound
		}
		if _, dup := s.tasks[t.ID]; dup {
			return store.ErrConflict
		}
	}
	if len(b.Skipped) == 0 {
		b.Skipped = []byte("[]")
	}
	b.CreatedAt = time.Now().UTC()
	s.batches[b.ID] = copyBatch(b)
	for _, t := range tasks {
		t.BatchID = &b.ID
		if t.Status == "" {
			t.Status = store.TaskQueued
		}
		if len(t.Params) == 0 {
			t.Params = []byte("{}")
		}
		if t.CreatedAt.IsZero() {
			t.CreatedAt = b.CreatedAt
		}
		if t.RunTimeout == 0 {
			t.RunTimeout = 5 * time.Minute
		}
		s.tasks[t.ID] = copyTask(t)
	}
	return nil
}

func (s *Store) GetTaskBatch(_ context.Context, id string) (*store.TaskBatch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.batches[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyBatch(b), nil
}

func (s *Store) ListTaskBatches(_ context.Context, orgID string, limit int) ([]store.TaskBatch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.TaskBatch{}
	for _, b := range s.batches {
		if b.OrganizationID == orgID {
			out = append(out, *copyBatch(b))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) ListBatchTasks(_ context.Context, batchID string) ([]store.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Task{}
	for _, t := range s.tasks {
		if t.BatchID != nil && *t.BatchID == batchID {
			out = append(out, *copyTask(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServerID < out[j].ServerID })
	return out, nil
}
