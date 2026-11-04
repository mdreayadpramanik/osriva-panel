package memstore

import (
	"bytes"
	"context"
	"slices"
	"sort"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func copyClient(c *store.APIClient) store.APIClient {
	x := *c
	x.SecretEnc = bytes.Clone(c.SecretEnc)
	x.Scopes, x.AllowedCIDRs = slices.Clone(c.Scopes), slices.Clone(c.AllowedCIDRs)
	if x.AllowedCIDRs == nil {
		x.AllowedCIDRs = []string{}
	}
	x.LastUsedAt = timePtr(c.LastUsedAt)
	return x
}

func (s *Store) CreateAPIClient(_ context.Context, c *store.APIClient) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[c.CreatedBy]; !ok {
		return store.ErrNotFound
	}
	for _, x := range s.clients {
		if x.KeyID == c.KeyID || (x.OrganizationID == c.OrganizationID && x.Name == c.Name) {
			return store.ErrConflict
		}
	}
	c.CreatedAt = now()
	x := copyClient(c)
	s.clients[c.ID] = &x
	return nil
}

func (s *Store) GetAPIClient(_ context.Context, id string) (*store.APIClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.clients[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	x := copyClient(c)
	return &x, nil
}

func (s *Store) GetAPIClientByKey(_ context.Context, keyID string) (*store.APIClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.clients {
		if c.KeyID == keyID {
			x := copyClient(c)
			return &x, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) ListAPIClients(_ context.Context, orgID string) ([]store.APIClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.APIClient{}
	for _, c := range s.clients {
		if c.OrganizationID == orgID {
			out = append(out, copyClient(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) SetAPIClientEnabled(_ context.Context, id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[id]
	if !ok {
		return store.ErrNotFound
	}
	c.Enabled = enabled
	return nil
}

func (s *Store) TouchAPIClient(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[id]
	if !ok {
		return store.ErrNotFound
	}
	c.LastUsedAt = &at
	return nil
}

func (s *Store) DeleteAPIClient(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[id]; !ok {
		return store.ErrNotFound
	}
	for _, x := range s.services {
		if x.ClientID == id {
			return store.ErrConflict
		}
	}
	delete(s.clients, id)
	for k := range s.nonces {
		if k[0] == id {
			delete(s.nonces, k)
		}
	}
	for k := range s.idem {
		if k[0] == id {
			delete(s.idem, k)
		}
	}
	return nil
}

func (s *Store) UseNonce(_ context.Context, clientID, nonce string, expiresAt, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{clientID, nonce}
	if exp, ok := s.nonces[k]; ok && exp.After(now) {
		return store.ErrConflict
	}
	s.nonces[k] = expiresAt
	return nil
}

func (s *Store) PruneNonces(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, exp := range s.nonces {
		if exp.Before(before) {
			delete(s.nonces, k)
			n++
		}
	}
	return n, nil
}

func copyIdem(r *store.IdempotencyRecord) *store.IdempotencyRecord {
	x := *r
	x.Response = bytes.Clone(r.Response)
	return &x
}

func (s *Store) BeginIdempotent(_ context.Context, rec *store.IdempotencyRecord) (*store.IdempotencyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{rec.ClientID, rec.Key}
	if old, ok := s.idem[k]; ok {
		return copyIdem(old), nil
	}
	rec.CreatedAt, rec.StatusCode, rec.Response = now(), 0, nil
	s.idem[k] = copyIdem(rec)
	return nil, nil
}

func (s *Store) FinishIdempotent(_ context.Context, clientID, key string, status int, response []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.idem[[2]string{clientID, key}]
	if !ok {
		return store.ErrNotFound
	}
	r.StatusCode, r.Response = status, bytes.Clone(response)
	return nil
}

func (s *Store) DeleteIdempotent(_ context.Context, clientID, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.idem, [2]string{clientID, key})
	return nil
}

func (s *Store) PruneIdempotency(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, r := range s.idem {
		if r.CreatedAt.Before(before) {
			delete(s.idem, k)
			n++
		}
	}
	return n, nil
}

func (s *Store) CreateServicePlan(_ context.Context, p *store.ServicePlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.templates[p.TemplateID]; !ok {
		return store.ErrNotFound
	}
	if _, ok := s.pools[p.PoolID]; !ok {
		return store.ErrNotFound
	}
	if p.NodeID != nil {
		if _, ok := s.nodes[*p.NodeID]; !ok {
			return store.ErrNotFound
		}
	}
	for _, x := range s.plans {
		if x.OrganizationID == p.OrganizationID && x.Name == p.Name {
			return store.ErrConflict
		}
	}
	p.CreatedAt = now()
	x := *p
	s.plans[p.ID] = &x
	return nil
}

func (s *Store) GetServicePlan(_ context.Context, id string) (*store.ServicePlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.plans[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	x := *p
	return &x, nil
}

func (s *Store) ListServicePlans(_ context.Context, orgID string) ([]store.ServicePlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.ServicePlan{}
	for _, p := range s.plans {
		if p.OrganizationID == orgID {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) DeleteServicePlan(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.plans[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.plans, id)
	for _, x := range s.services {
		if x.PlanID != nil && *x.PlanID == id {
			x.PlanID = nil
		}
	}
	return nil
}

func (s *Store) CreateProvisionedService(_ context.Context, ps *store.ProvisionedService) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[ps.ClientID]; !ok {
		return store.ErrNotFound
	}
	for _, x := range s.services {
		if x.ClientID == ps.ClientID && x.ExternalID == ps.ExternalID {
			return store.ErrConflict
		}
	}
	ps.CreatedAt, ps.UpdatedAt = now(), now()
	x := *ps
	s.services[ps.ID] = &x
	return nil
}

func (s *Store) GetProvisionedService(_ context.Context, id string) (*store.ProvisionedService, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	x, ok := s.services[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *x
	return &c, nil
}

func (s *Store) GetProvisionedServiceByExternal(_ context.Context, clientID, externalID string) (*store.ProvisionedService, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, x := range s.services {
		if x.ClientID == clientID && x.ExternalID == externalID {
			c := *x
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) ListProvisionedServices(_ context.Context, orgID string) ([]store.ProvisionedService, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.ProvisionedService{}
	for _, x := range s.services {
		if x.OrganizationID == orgID {
			out = append(out, *x)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt) || out[i].CreatedAt.Equal(out[j].CreatedAt) && out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Store) ListPendingServices(_ context.Context, limit int) ([]store.ProvisionedService, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.ProvisionedService{}
	for _, x := range s.services {
		if x.Pending != "" {
			out = append(out, *x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) UpdateProvisionedService(_ context.Context, ps *store.ProvisionedService) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.services[ps.ID]
	if !ok {
		return store.ErrNotFound
	}
	x.Status, x.Pending, x.LastError, x.VPSID, x.UpdatedAt = ps.Status, ps.Pending, ps.LastError, ps.VPSID, now()
	ps.UpdatedAt = x.UpdatedAt
	return nil
}

func (s *Store) planUsesLocked(match func(*store.ServicePlan) bool) bool {
	for _, p := range s.plans {
		if match(p) {
			return true
		}
	}
	return false
}

func (s *Store) DeleteProvisionedService(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.services[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.services, id)
	return nil
}
