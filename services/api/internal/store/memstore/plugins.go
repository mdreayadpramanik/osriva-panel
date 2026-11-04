package memstore

import (
	"context"
	"sort"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func (s *Store) ListPluginApprovals(context.Context) ([]store.PluginApproval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.PluginApproval{}
	for _, a := range s.pluginApprovals {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) GetPluginApproval(_ context.Context, name string) (*store.PluginApproval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.pluginApprovals[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *a
	return &c, nil
}

func (s *Store) SetPluginApproval(_ context.Context, a *store.PluginApproval) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.ApprovedAt = now()
	c := *a
	s.pluginApprovals[a.Name] = &c
	return nil
}
