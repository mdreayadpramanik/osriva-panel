package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func testCustomers(t *testing.T, s store.Store) {
	org := store.DefaultOrganizationID
	now := time.Now().UTC().Truncate(time.Second)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	setupA := auth.HashToken("setup-a")
	exp := now.Add(72 * time.Hour)
	a := &store.Customer{ID: auth.NewID(), OrganizationID: org, Email: "alice@example.com", Name: "Alice", Status: store.CustomerActive, SetupExpiresAt: &exp}
	must(s.CreateCustomer(ctx(), a, setupA))
	b := &store.Customer{ID: auth.NewID(), OrganizationID: org, Email: "bob@example.com", Name: "Bob", Status: store.CustomerActive}
	must(s.CreateCustomer(ctx(), b, nil))
	if err := s.CreateCustomer(ctx(), &store.Customer{ID: auth.NewID(), OrganizationID: org, Email: "alice@example.com", Name: "x", Status: store.CustomerActive}, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate e-mail: %v", err)
	}
	if got, err := s.GetCustomerByEmail(ctx(), "alice@example.com"); err != nil || got.ID != a.ID || got.PasswordHash != "" || got.SetupExpiresAt == nil {
		t.Fatalf("by e-mail: %+v %v", got, err)
	}

	// Setup token: unknown, expired, then once only.
	if _, err := s.ConsumeCustomerSetup(ctx(), auth.HashToken("wrong"), "h", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
	if _, err := s.ConsumeCustomerSetup(ctx(), setupA, "h", now.Add(73*time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired token: %v", err)
	}
	got, err := s.ConsumeCustomerSetup(ctx(), setupA, "argon2-hash", now)
	if err != nil || got.ID != a.ID || got.PasswordHash != "argon2-hash" || got.SetupExpiresAt != nil {
		t.Fatalf("consume: %+v %v", got, err)
	}
	if _, err := s.ConsumeCustomerSetup(ctx(), setupA, "again", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a setup token works once: %v", err)
	}
	must(s.SetCustomerSetupToken(ctx(), b.ID, auth.HashToken("setup-b"), now.Add(time.Hour)))
	if _, err := s.ConsumeCustomerSetup(ctx(), auth.HashToken("setup-b"), "hb", now); err != nil {
		t.Fatalf("new setup token: %v", err)
	}
	must(s.SetCustomerStatus(ctx(), b.ID, store.CustomerDisabled))
	must(s.TouchCustomerLogin(ctx(), a.ID, now))
	if list, _ := s.ListCustomers(ctx(), org); len(list) != 2 || list[0].Email != "alice@example.com" || list[0].LastLoginAt == nil || list[1].Status != store.CustomerDisabled {
		t.Fatalf("list: %+v", list)
	}

	// Sessions.
	s1 := &store.CustomerSession{ID: auth.NewID(), CustomerID: a.ID, TokenHash: auth.HashToken("sess-1"), ExpiresAt: now.Add(12 * time.Hour)}
	s2 := &store.CustomerSession{ID: auth.NewID(), CustomerID: a.ID, TokenHash: auth.HashToken("sess-2"), ExpiresAt: now.Add(12 * time.Hour)}
	must(s.CreateCustomerSession(ctx(), s1))
	must(s.CreateCustomerSession(ctx(), s2))
	if x, err := s.GetCustomerSessionByTokenHash(ctx(), auth.HashToken("sess-1")); err != nil || x.CustomerID != a.ID || x.RevokedAt != nil {
		t.Fatalf("session: %+v %v", x, err)
	}
	must(s.RevokeCustomerSessions(ctx(), a.ID, s1.ID, now))
	if x, _ := s.GetCustomerSessionByTokenHash(ctx(), auth.HashToken("sess-2")); x.RevokedAt == nil {
		t.Fatal("other sessions revoked")
	}
	if x, _ := s.GetCustomerSessionByTokenHash(ctx(), auth.HashToken("sess-1")); x.RevokedAt != nil {
		t.Fatal("the kept session stays")
	}
	must(s.RevokeCustomerSession(ctx(), s1.ID, now))

	// Ownership: a customer reaches only their own VPS.
	x, _ := newServer(t, s, "cust-node", now)
	dc := &store.Datacenter{ID: auth.NewID(), OrganizationID: org, Name: "dc-c"}
	must(s.CreateDatacenter(ctx(), dc))
	node := &store.Node{ID: auth.NewID(), OrganizationID: org, ServerID: x.ID, DatacenterID: dc.ID, Hypervisor: "libvirt", Network: "default",
		CPUOvercommit: 4, MemoryOvercommit: 1, Enabled: true}
	must(s.CreateNode(ctx(), node))
	pool := &store.IPPool{ID: auth.NewID(), OrganizationID: org, Name: "pc", CIDR: "10.1.0.0/24", Gateway: "10.1.0.1", First: "10.1.0.10", Last: "10.1.0.20"}
	must(s.CreateIPPool(ctx(), pool))
	mk := func(name, ip string, owner *string) *store.VPS {
		v := &store.VPS{ID: auth.NewID(), OrganizationID: org, NodeID: node.ID, PoolID: pool.ID, Name: name, VCPUs: 1, MemoryMB: 512, DiskGB: 5,
			IPAddress: ip, State: "running", CustomerID: owner}
		must(s.CreateVPS(ctx(), v))
		return v
	}
	va := mk("alice-1", "10.1.0.10", &a.ID)
	vb := mk("bob-1", "10.1.0.11", nil)
	must(s.SetVPSCustomer(ctx(), vb.ID, &b.ID))
	vn := mk("nobody", "10.1.0.12", nil)
	if list, _ := s.ListCustomerVPS(ctx(), a.ID); len(list) != 1 || list[0].ID != va.ID || list[0].CustomerID == nil {
		t.Fatalf("alice's VPS: %+v", list)
	}
	for _, id := range []string{vb.ID, vn.ID, auth.NewID()} {
		if _, err := s.GetCustomerVPS(ctx(), a.ID, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("alice reached VPS %s: %v", id, err)
		}
	}
	if got, err := s.GetCustomerVPS(ctx(), a.ID, va.ID); err != nil || got.Name != "alice-1" {
		t.Fatalf("own VPS: %+v %v", got, err)
	}
	if err := s.SetVPSCustomer(ctx(), va.ID, strPtr(auth.NewID())); err == nil {
		t.Fatal("assigning to an unknown customer must fail")
	}
	must(s.SetVPSCustomer(ctx(), va.ID, nil))
	if list, _ := s.ListCustomerVPS(ctx(), a.ID); len(list) != 0 {
		t.Fatalf("unassigned: %d", len(list))
	}
	if _, err := s.GetProvisionedServiceByVPS(ctx(), vn.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no service for this VPS: %v", err)
	}
}
