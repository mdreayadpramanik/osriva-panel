package memstore

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func now() time.Time { return time.Now().UTC() }

// ---- data centers and racks ----

func (s *Store) CreateDatacenter(_ context.Context, d *store.Datacenter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.dcs {
		if x.OrganizationID == d.OrganizationID && x.Name == d.Name {
			return store.ErrConflict
		}
	}
	d.CreatedAt = now()
	c := *d
	s.dcs[d.ID] = &c
	return nil
}

func (s *Store) GetDatacenter(_ context.Context, id string) (*store.Datacenter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.dcs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *d
	return &c, nil
}

func (s *Store) ListDatacenters(_ context.Context, orgID string) ([]store.Datacenter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Datacenter{}
	for _, d := range s.dcs {
		if d.OrganizationID == orgID {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) DeleteDatacenter(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.dcs[id]; !ok {
		return store.ErrNotFound
	}
	for _, r := range s.racks {
		if r.DatacenterID == id {
			return store.ErrConflict
		}
	}
	for _, n := range s.nodes {
		if n.DatacenterID == id {
			return store.ErrConflict
		}
	}
	for _, p := range s.pools {
		if p.DatacenterID != nil && *p.DatacenterID == id {
			return store.ErrConflict
		}
	}
	delete(s.dcs, id)
	return nil
}

func (s *Store) CreateRack(_ context.Context, r *store.Rack) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.dcs[r.DatacenterID]; !ok {
		return store.ErrNotFound
	}
	for _, x := range s.racks {
		if x.DatacenterID == r.DatacenterID && x.Name == r.Name {
			return store.ErrConflict
		}
	}
	r.CreatedAt = now()
	c := *r
	s.racks[r.ID] = &c
	return nil
}

func (s *Store) GetRack(_ context.Context, id string) (*store.Rack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.racks[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *r
	return &c, nil
}

func (s *Store) ListRacks(_ context.Context, datacenterID string) ([]store.Rack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Rack{}
	for _, r := range s.racks {
		if r.DatacenterID == datacenterID {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) DeleteRack(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.racks[id]; !ok {
		return store.ErrNotFound
	}
	for _, n := range s.nodes {
		if n.RackID != nil && *n.RackID == id {
			return store.ErrConflict
		}
	}
	delete(s.racks, id)
	return nil
}

// ---- nodes ----

func (s *Store) CreateNode(_ context.Context, n *store.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[n.ServerID]; !ok {
		return store.ErrNotFound
	}
	if _, ok := s.dcs[n.DatacenterID]; !ok {
		return store.ErrNotFound
	}
	if n.RackID != nil {
		if _, ok := s.racks[*n.RackID]; !ok {
			return store.ErrNotFound
		}
	}
	for _, x := range s.nodes {
		if x.ServerID == n.ServerID {
			return store.ErrConflict
		}
	}
	n.CreatedAt = now()
	c := *n
	s.nodes[n.ID] = &c
	return nil
}

func (s *Store) GetNode(_ context.Context, id string) (*store.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *n
	return &c, nil
}

func (s *Store) ListNodes(_ context.Context, orgID string) ([]store.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Node{}
	for _, n := range s.nodes {
		if n.OrganizationID == orgID {
			out = append(out, *n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt) || out[i].CreatedAt.Equal(out[j].CreatedAt) && out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Store) DeleteNode(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[id]; !ok {
		return store.ErrNotFound
	}
	if s.planUsesLocked(func(p *store.ServicePlan) bool { return p.NodeID != nil && *p.NodeID == id }) {
		return store.ErrConflict
	}
	for _, v := range s.vps {
		if v.NodeID == id {
			return store.ErrConflict
		}
	}
	delete(s.nodes, id)
	return nil
}

// ---- IP pools ----

func copyPool(p *store.IPPool) store.IPPool {
	c := *p
	c.DNS = slices.Clone(p.DNS)
	if c.DNS == nil {
		c.DNS = []string{}
	}
	return c
}

func (s *Store) CreateIPPool(_ context.Context, p *store.IPPool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.DatacenterID != nil {
		if _, ok := s.dcs[*p.DatacenterID]; !ok {
			return store.ErrNotFound
		}
	}
	for _, x := range s.pools {
		if x.OrganizationID == p.OrganizationID && x.Name == p.Name {
			return store.ErrConflict
		}
	}
	p.CreatedAt = now()
	c := copyPool(p)
	s.pools[p.ID] = &c
	return nil
}

func (s *Store) GetIPPool(_ context.Context, id string) (*store.IPPool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pools[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := copyPool(p)
	return &c, nil
}

func (s *Store) ListIPPools(_ context.Context, orgID string) ([]store.IPPool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.IPPool{}
	for _, p := range s.pools {
		if p.OrganizationID == orgID {
			out = append(out, copyPool(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) DeleteIPPool(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pools[id]; !ok {
		return store.ErrNotFound
	}
	if s.planUsesLocked(func(p *store.ServicePlan) bool { return p.PoolID == id }) {
		return store.ErrConflict
	}
	for _, v := range s.vps {
		if v.PoolID == id {
			return store.ErrConflict
		}
	}
	delete(s.pools, id)
	return nil
}

func (s *Store) ListPoolAddresses(_ context.Context, poolID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []string{}
	for _, v := range s.vps {
		if v.PoolID == poolID {
			out = append(out, v.IPAddress)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ---- templates ----

func (s *Store) CreateVMTemplate(_ context.Context, t *store.VMTemplate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.templates {
		if x.OrganizationID == t.OrganizationID && x.Name == t.Name {
			return store.ErrConflict
		}
	}
	t.CreatedAt = now()
	c := *t
	s.templates[t.ID] = &c
	return nil
}

func (s *Store) GetVMTemplate(_ context.Context, id string) (*store.VMTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.templates[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *t
	return &c, nil
}

func (s *Store) ListVMTemplates(_ context.Context, orgID string) ([]store.VMTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.VMTemplate{}
	for _, t := range s.templates {
		if t.OrganizationID == orgID {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) DeleteVMTemplate(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.templates[id]; !ok {
		return store.ErrNotFound
	}
	if s.planUsesLocked(func(p *store.ServicePlan) bool { return p.TemplateID == id }) {
		return store.ErrConflict
	}
	delete(s.templates, id)
	for _, v := range s.vps {
		if v.TemplateID != nil && *v.TemplateID == id {
			v.TemplateID = nil
		}
	}
	return nil
}

// ---- VPS ----

func copyVPS(v *store.VPS) store.VPS {
	c := *v
	c.SSHKeys = slices.Clone(v.SSHKeys)
	if c.SSHKeys == nil {
		c.SSHKeys = []string{}
	}
	return c
}

func (s *Store) CreateVPS(_ context.Context, v *store.VPS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[v.NodeID]; !ok {
		return store.ErrNotFound
	}
	if _, ok := s.pools[v.PoolID]; !ok {
		return store.ErrNotFound
	}
	for _, x := range s.vps {
		if (x.OrganizationID == v.OrganizationID && x.Name == v.Name) || (x.PoolID == v.PoolID && x.IPAddress == v.IPAddress) || x.ID == v.ID {
			return store.ErrConflict
		}
	}
	v.CreatedAt, v.UpdatedAt = now(), now()
	c := copyVPS(v)
	s.vps[v.ID] = &c
	return nil
}

func (s *Store) GetVPS(_ context.Context, id string) (*store.VPS, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.vps[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := copyVPS(v)
	return &c, nil
}

func (s *Store) listVPS(keep func(*store.VPS) bool) []store.VPS {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.VPS{}
	for _, v := range s.vps {
		if keep(v) {
			out = append(out, copyVPS(v))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Store) ListVPS(_ context.Context, orgID string) ([]store.VPS, error) {
	return s.listVPS(func(v *store.VPS) bool { return v.OrganizationID == orgID }), nil
}

func (s *Store) ListNodeVPS(_ context.Context, nodeID string) ([]store.VPS, error) {
	return s.listVPS(func(v *store.VPS) bool { return v.NodeID == nodeID }), nil
}

func (s *Store) SetVPSState(_ context.Context, id, state string, pendingTaskID *string, pendingOp, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.vps[id]
	if !ok {
		return store.ErrNotFound
	}
	v.State, v.PendingTaskID, v.PendingOp, v.LastError, v.UpdatedAt = state, pendingTaskID, pendingOp, lastError, now()
	return nil
}

func (s *Store) DeleteVPS(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.vps[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.vps, id)
	return nil
}
