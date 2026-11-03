package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func (s *Store) CreateCMS(_ context.Context, c *store.CMSInstance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[c.ServerID]; !ok {
		return store.ErrNotFound
	}
	if c.InstallTaskID != nil {
		if _, ok := s.tasks[*c.InstallTaskID]; !ok {
			return store.ErrNotFound
		}
	}
	for _, o := range s.cms {
		if o.ID == c.ID || (o.ServerID == c.ServerID && (o.Name == c.Name || o.HostPort == c.HostPort)) {
			return store.ErrConflict
		}
	}
	c.CreatedAt = time.Now().UTC()
	x := *c
	s.cms[c.ID] = &x
	return nil
}

func (s *Store) GetCMS(_ context.Context, id string) (*store.CMSInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.cms[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	x := *c
	if x.SiteID != nil {
		if _, ok := s.sites[*x.SiteID]; !ok {
			x.SiteID = nil // ON DELETE SET NULL
		}
	}
	return &x, nil
}

func (s *Store) ListCMS(ctx context.Context, serverID string) ([]store.CMSInstance, error) {
	s.mu.RLock()
	ids := []string{}
	for id, c := range s.cms {
		if c.ServerID == serverID {
			ids = append(ids, id)
		}
	}
	s.mu.RUnlock()
	out := []store.CMSInstance{}
	for _, id := range ids {
		c, _ := s.GetCMS(ctx, id)
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) DeleteCMS(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cms[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.cms, id)
	return nil
}
