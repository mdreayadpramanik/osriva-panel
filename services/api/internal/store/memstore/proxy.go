package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func (s *Store) GetProxySettings(_ context.Context, serverID string) (*store.ProxySettings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proxies[serverID]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *p
	return &c, nil
}

func (s *Store) SaveProxySettings(_ context.Context, p *store.ProxySettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[p.ServerID]; !ok {
		return store.ErrNotFound
	}
	p.UpdatedAt = time.Now().UTC()
	c := *p
	s.proxies[p.ServerID] = &c
	return nil
}

// domainTaken reports a domain used by another site; s.mu must be held.
func (s *Store) domainTaken(x *store.Site) bool {
	for _, o := range s.sites {
		if o.ID != x.ID && o.Domain == x.Domain {
			return true
		}
	}
	return false
}

func (s *Store) CreateSite(_ context.Context, x *store.Site) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[x.ServerID]; !ok {
		return store.ErrNotFound
	}
	if _, dup := s.sites[x.ID]; dup || s.domainTaken(x) {
		return store.ErrConflict
	}
	now := time.Now().UTC()
	x.CreatedAt, x.UpdatedAt = now, now
	c := *x
	s.sites[x.ID] = &c
	return nil
}

func (s *Store) GetSite(_ context.Context, id string) (*store.Site, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	x, ok := s.sites[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *x
	return &c, nil
}

func (s *Store) ListSites(_ context.Context, serverID string) ([]store.Site, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Site{}
	for _, x := range s.sites {
		if x.ServerID == serverID {
			out = append(out, *x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out, nil
}

func (s *Store) UpdateSite(_ context.Context, x *store.Site) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.sites[x.ID]
	if !ok {
		return store.ErrNotFound
	}
	if s.domainTaken(x) {
		return store.ErrConflict
	}
	x.ServerID, x.CreatedBy, x.CreatedAt, x.UpdatedAt = old.ServerID, old.CreatedBy, old.CreatedAt, time.Now().UTC()
	c := *x
	s.sites[x.ID] = &c
	return nil
}

func (s *Store) DeleteSite(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sites[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.sites, id)
	return nil
}
