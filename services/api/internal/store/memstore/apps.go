package memstore

import (
	"bytes"
	"context"
	"sort"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func copyApp(a *store.App) *store.App {
	c := *a
	c.EnvEnc = bytes.Clone(a.EnvEnc)
	return &c
}

// appConflict reports a name or host port already used on the server; s.mu must be held.
func (s *Store) appConflict(a *store.App) bool {
	for _, x := range s.apps {
		if x.ID != a.ID && x.ServerID == a.ServerID && (x.Name == a.Name || x.HostPort == a.HostPort) {
			return true
		}
	}
	return false
}

func (s *Store) CreateApp(_ context.Context, a *store.App) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[a.ServerID]; !ok {
		return store.ErrNotFound
	}
	if _, dup := s.apps[a.ID]; dup || s.appConflict(a) {
		return store.ErrConflict
	}
	now := time.Now().UTC()
	a.CreatedAt, a.UpdatedAt = now, now
	s.apps[a.ID] = copyApp(a)
	return nil
}

func (s *Store) GetApp(_ context.Context, id string) (*store.App, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.apps[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyApp(a), nil
}

func (s *Store) ListApps(_ context.Context, serverID string) ([]store.App, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.App{}
	for _, a := range s.apps {
		if a.ServerID == serverID {
			out = append(out, *copyApp(a))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) UpdateApp(_ context.Context, a *store.App) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.apps[a.ID]
	if !ok {
		return store.ErrNotFound
	}
	if s.appConflict(a) {
		return store.ErrConflict
	}
	// Server, name and creation data are immutable.
	a.ServerID, a.Name, a.CreatedBy, a.CreatedAt = old.ServerID, old.Name, old.CreatedBy, old.CreatedAt
	a.UpdatedAt = time.Now().UTC()
	s.apps[a.ID] = copyApp(a)
	return nil
}

func (s *Store) DeleteApp(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.apps, id)
	for k, d := range s.deployments {
		if d.AppID == id {
			delete(s.deployments, k)
		}
	}
	return nil
}

func (s *Store) CreateDeployment(_ context.Context, d *store.Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[d.AppID]; !ok {
		return store.ErrNotFound
	}
	if _, dup := s.deployments[d.ID]; dup {
		return store.ErrConflict
	}
	if d.TaskID != nil {
		if _, ok := s.tasks[*d.TaskID]; !ok {
			return store.ErrNotFound
		}
	}
	d.CreatedAt = time.Now().UTC()
	c := *d
	s.deployments[d.ID] = &c
	return nil
}

func (s *Store) GetDeployment(_ context.Context, id string) (*store.Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.deployments[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *d
	return &c, nil
}

func (s *Store) ListDeployments(_ context.Context, appID string, limit int) ([]store.Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Deployment{}
	for _, d := range s.deployments {
		if d.AppID == appID {
			out = append(out, *d)
		}
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
