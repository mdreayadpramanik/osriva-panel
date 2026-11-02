package memstore

import (
	"bytes"
	"context"
	"sort"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func (s *Store) GetAgentCA(context.Context) (string, []byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.caCert == "" {
		return "", nil, store.ErrNotFound
	}
	return s.caCert, bytes.Clone(s.caKey), nil
}

func (s *Store) InsertAgentCA(_ context.Context, certPEM string, keyEnc []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.caCert != "" {
		return store.ErrConflict
	}
	s.caCert, s.caKey = certPEM, bytes.Clone(keyEnc)
	return nil
}

func timePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

func copyServer(x *store.Server) *store.Server {
	c := *x
	c.EnrollmentTokenHash = bytes.Clone(x.EnrollmentTokenHash)
	c.EnrollmentExpiresAt = timePtr(x.EnrollmentExpiresAt)
	c.LastSeenAt = timePtr(x.LastSeenAt)
	c.EnrolledAt = timePtr(x.EnrolledAt)
	return &c
}

func (s *Store) CreateServer(_ context.Context, x *store.Server) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.servers {
		if e.ID == x.ID || (e.OrganizationID == x.OrganizationID && e.Name == x.Name) {
			return store.ErrConflict
		}
		if x.EnrollmentTokenHash != nil && bytes.Equal(e.EnrollmentTokenHash, x.EnrollmentTokenHash) {
			return store.ErrConflict
		}
	}
	if x.Status == "" {
		x.Status = store.ServerPending
	}
	now := time.Now().UTC()
	x.CreatedAt, x.UpdatedAt = now, now
	s.servers[x.ID] = copyServer(x)
	return nil
}

func (s *Store) GetServer(_ context.Context, id string) (*store.Server, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if x, ok := s.servers[id]; ok {
		return copyServer(x), nil
	}
	return nil, store.ErrNotFound
}

func (s *Store) ListServers(_ context.Context, orgID string) ([]store.Server, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Server{}
	for _, x := range s.servers {
		if x.OrganizationID == orgID {
			out = append(out, *copyServer(x))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) SetEnrollmentToken(_ context.Context, id string, hash []byte, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.servers[id]
	if !ok {
		return store.ErrNotFound
	}
	if x.Status != store.ServerPending {
		return store.ErrConflict
	}
	x.EnrollmentTokenHash, x.EnrollmentExpiresAt = bytes.Clone(hash), &expiresAt
	x.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *Store) findPendingLocked(hash []byte, now time.Time) *store.Server {
	for _, x := range s.servers {
		if x.Status == store.ServerPending && x.EnrollmentTokenHash != nil && bytes.Equal(x.EnrollmentTokenHash, hash) &&
			x.EnrollmentExpiresAt != nil && now.Before(*x.EnrollmentExpiresAt) {
			return x
		}
	}
	return nil
}

func (s *Store) FindPendingEnrollment(_ context.Context, hash []byte, now time.Time) (*store.Server, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if x := s.findPendingLocked(hash, now); x != nil {
		return copyServer(x), nil
	}
	return nil, store.ErrNotFound
}

func (s *Store) CompleteEnrollment(_ context.Context, serverID string, hash []byte, now time.Time, cert *store.AgentCertificate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.findPendingLocked(hash, now)
	if x == nil || x.ID != serverID {
		return store.ErrNotFound
	}
	if _, dup := s.certs[cert.Serial]; dup {
		return store.ErrConflict
	}
	x.Status, x.EnrollmentTokenHash, x.EnrollmentExpiresAt = store.ServerActive, nil, nil
	x.EnrolledAt, x.UpdatedAt = &now, now
	c := *cert
	c.IssuedAt = now
	s.certs[c.Serial] = &c
	return nil
}

func (s *Store) AddAgentCertificate(_ context.Context, cert *store.AgentCertificate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[cert.ServerID]; !ok {
		return store.ErrNotFound
	}
	if _, dup := s.certs[cert.Serial]; dup {
		return store.ErrConflict
	}
	c := *cert
	if c.IssuedAt.IsZero() {
		c.IssuedAt = time.Now().UTC()
	}
	s.certs[c.Serial] = &c
	return nil
}

func (s *Store) GetAgentCertificate(_ context.Context, serial string) (*store.AgentCertificate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.certs[serial]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *c
	cp.RevokedAt = timePtr(c.RevokedAt)
	return &cp, nil
}

func (s *Store) RevokeServer(_ context.Context, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.servers[id]
	if !ok {
		return store.ErrNotFound
	}
	x.Status, x.EnrollmentTokenHash, x.EnrollmentExpiresAt, x.UpdatedAt = store.ServerRevoked, nil, nil, now
	for _, c := range s.certs {
		if c.ServerID == id && c.RevokedAt == nil {
			t := now
			c.RevokedAt = &t
		}
	}
	return nil
}

func (s *Store) RecordReport(_ context.Context, serverID string, facts store.ServerFacts, ip string, now time.Time, inv *store.ServerInventory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.servers[serverID]
	if !ok {
		return store.ErrNotFound
	}
	x.Facts, x.LastSeenIP, x.LastSeenAt, x.UpdatedAt = facts, ip, &now, now
	if inv != nil {
		c := *inv
		c.ServerID, c.ReceivedAt = serverID, now
		c.Data, c.Capabilities = bytes.Clone(inv.Data), bytes.Clone(inv.Capabilities)
		if len(c.Capabilities) == 0 {
			c.Capabilities = []byte("[]") // match the PostgreSQL column default
		}
		s.inventory[serverID] = &c
	}
	return nil
}

func (s *Store) GetServerInventory(_ context.Context, serverID string) (*store.ServerInventory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inv, ok := s.inventory[serverID]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := *inv
	c.Data, c.Capabilities = bytes.Clone(inv.Data), bytes.Clone(inv.Capabilities)
	return &c, nil
}
