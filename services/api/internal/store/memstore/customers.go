package memstore

import (
	"bytes"
	"context"
	"sort"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

type custRecord struct {
	c         store.Customer
	setupHash []byte
}

func copyCustomer(c *store.Customer) *store.Customer {
	x := *c
	x.SetupExpiresAt, x.LastLoginAt = timePtr(c.SetupExpiresAt), timePtr(c.LastLoginAt)
	return &x
}

func (s *Store) CreateCustomer(_ context.Context, c *store.Customer, setupHash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.customers {
		if r.c.Email == c.Email || r.c.ID == c.ID {
			return store.ErrConflict
		}
	}
	c.CreatedAt = now()
	s.customers[c.ID] = &custRecord{c: *copyCustomer(c), setupHash: bytes.Clone(setupHash)}
	return nil
}

func (s *Store) GetCustomer(_ context.Context, id string) (*store.Customer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.customers[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyCustomer(&r.c), nil
}

func (s *Store) GetCustomerByEmail(_ context.Context, email string) (*store.Customer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.customers {
		if r.c.Email == email {
			return copyCustomer(&r.c), nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) ListCustomers(_ context.Context, orgID string) ([]store.Customer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Customer{}
	for _, r := range s.customers {
		if r.c.OrganizationID == orgID {
			out = append(out, *copyCustomer(&r.c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out, nil
}

func (s *Store) SetCustomerStatus(_ context.Context, id string, status store.CustomerStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.customers[id]
	if !ok {
		return store.ErrNotFound
	}
	r.c.Status = status
	return nil
}

func (s *Store) SetCustomerSetupToken(_ context.Context, id string, hash []byte, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.customers[id]
	if !ok {
		return store.ErrNotFound
	}
	r.setupHash, r.c.SetupExpiresAt = bytes.Clone(hash), &expiresAt
	return nil
}

func (s *Store) ConsumeCustomerSetup(_ context.Context, hash []byte, passwordHash string, now time.Time) (*store.Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.customers {
		if len(r.setupHash) > 0 && bytes.Equal(r.setupHash, hash) && r.c.SetupExpiresAt != nil && now.Before(*r.c.SetupExpiresAt) {
			r.setupHash, r.c.SetupExpiresAt, r.c.PasswordHash = nil, nil, passwordHash
			return copyCustomer(&r.c), nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) SetCustomerPassword(_ context.Context, id, passwordHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.customers[id]
	if !ok {
		return store.ErrNotFound
	}
	r.c.PasswordHash = passwordHash
	return nil
}

func (s *Store) TouchCustomerLogin(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.customers[id]
	if !ok {
		return store.ErrNotFound
	}
	r.c.LastLoginAt = &at
	return nil
}

func copyCustSess(x *store.CustomerSession) *store.CustomerSession {
	c := *x
	c.TokenHash = bytes.Clone(x.TokenHash)
	c.RevokedAt = timePtr(x.RevokedAt)
	return &c
}

func (s *Store) CreateCustomerSession(_ context.Context, x *store.CustomerSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.customers[x.CustomerID]; !ok {
		return store.ErrNotFound
	}
	for _, o := range s.custSess {
		if bytes.Equal(o.TokenHash, x.TokenHash) {
			return store.ErrConflict
		}
	}
	if x.CreatedAt.IsZero() {
		x.CreatedAt = now()
	}
	if x.LastSeenAt.IsZero() {
		x.LastSeenAt = x.CreatedAt
	}
	s.custSess[x.ID] = copyCustSess(x)
	return nil
}

func (s *Store) GetCustomerSessionByTokenHash(_ context.Context, hash []byte) (*store.CustomerSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, x := range s.custSess {
		if bytes.Equal(x.TokenHash, hash) {
			return copyCustSess(x), nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) TouchCustomerSession(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.custSess[id]
	if !ok {
		return store.ErrNotFound
	}
	x.LastSeenAt = at
	return nil
}

func (s *Store) RevokeCustomerSession(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.custSess[id]
	if !ok {
		return store.ErrNotFound
	}
	if x.RevokedAt == nil {
		x.RevokedAt = &at
	}
	return nil
}

func (s *Store) RevokeCustomerSessions(_ context.Context, customerID, keepID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.custSess {
		if x.CustomerID == customerID && x.ID != keepID && x.RevokedAt == nil {
			t := at
			x.RevokedAt = &t
		}
	}
	return nil
}

func (s *Store) SetVPSCustomer(_ context.Context, vpsID string, customerID *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.vps[vpsID]
	if !ok {
		return store.ErrNotFound
	}
	if customerID != nil {
		if _, ok := s.customers[*customerID]; !ok {
			return store.ErrNotFound
		}
	}
	v.CustomerID = customerID
	return nil
}

func (s *Store) ListCustomerVPS(_ context.Context, customerID string) ([]store.VPS, error) {
	return s.listVPS(func(v *store.VPS) bool { return v.CustomerID != nil && *v.CustomerID == customerID }), nil
}

func (s *Store) GetCustomerVPS(_ context.Context, customerID, vpsID string) (*store.VPS, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.vps[vpsID]
	if !ok || v.CustomerID == nil || *v.CustomerID != customerID {
		return nil, store.ErrNotFound
	}
	c := copyVPS(v)
	return &c, nil
}

func (s *Store) GetProvisionedServiceByVPS(_ context.Context, vpsID string) (*store.ProvisionedService, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, x := range s.services {
		if x.VPSID != nil && *x.VPSID == vpsID {
			c := *x
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}
