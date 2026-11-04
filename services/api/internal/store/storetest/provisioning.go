package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func testProvisioning(t *testing.T, s store.Store) {
	org := store.DefaultOrganizationID
	now := time.Now().UTC().Truncate(time.Second)
	u := newUser(t, s, "billing-owner@example.com", rbac.RoleOwner)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	c := &store.APIClient{ID: auth.NewID(), OrganizationID: org, Name: "whmcs", KeyID: "osk_abc", SecretEnc: []byte{1, 2},
		Scopes: []string{"provisioning.read", "provisioning.write"}, AllowedCIDRs: []string{"203.0.113.0/24"}, WebhookURL: "https://billing.example.com/hook",
		Enabled: true, CreatedBy: u.ID}
	must(s.CreateAPIClient(ctx(), c))
	if err := s.CreateAPIClient(ctx(), &store.APIClient{ID: auth.NewID(), OrganizationID: org, Name: "other", KeyID: "osk_abc", SecretEnc: []byte{1},
		Scopes: []string{"provisioning.read"}, CreatedBy: u.ID}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate key id: %v", err)
	}
	got, err := s.GetAPIClientByKey(ctx(), "osk_abc")
	if err != nil || got.ID != c.ID || len(got.Scopes) != 2 || got.AllowedCIDRs[0] != "203.0.113.0/24" || got.SecretEnc[1] != 2 || got.LastUsedAt != nil {
		t.Fatalf("client: %+v %v", got, err)
	}
	must(s.SetAPIClientEnabled(ctx(), c.ID, false))
	must(s.TouchAPIClient(ctx(), c.ID, now))
	if got, _ := s.GetAPIClient(ctx(), c.ID); got.Enabled || got.LastUsedAt == nil {
		t.Fatalf("enabled/touched: %+v", got)
	}

	// Nonces: once while valid.
	must(s.UseNonce(ctx(), c.ID, "n-1", now.Add(10*time.Minute), now))
	if err := s.UseNonce(ctx(), c.ID, "n-1", now.Add(10*time.Minute), now.Add(time.Minute)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("replayed nonce: %v", err)
	}
	must(s.UseNonce(ctx(), c.ID, "n-1", now.Add(30*time.Minute), now.Add(11*time.Minute))) // expired: usable again
	if n, err := s.PruneNonces(ctx(), now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("PruneNonces: %d %v", n, err)
	}

	// Idempotency.
	rec := &store.IdempotencyRecord{ClientID: c.ID, Key: "key-0001", RequestHash: "h1"}
	if old, err := s.BeginIdempotent(ctx(), rec); err != nil || old != nil {
		t.Fatalf("first begin: %+v %v", old, err)
	}
	old, err := s.BeginIdempotent(ctx(), &store.IdempotencyRecord{ClientID: c.ID, Key: "key-0001", RequestHash: "h2"})
	if err != nil || old == nil || old.StatusCode != 0 || old.RequestHash != "h1" {
		t.Fatalf("in progress: %+v %v", old, err)
	}
	must(s.FinishIdempotent(ctx(), c.ID, "key-0001", 202, []byte(`{"id":"x"}`)))
	if old, _ := s.BeginIdempotent(ctx(), rec); old == nil || old.StatusCode != 202 || string(old.Response) != `{"id":"x"}` {
		t.Fatalf("finished: %+v", old)
	}
	must(s.DeleteIdempotent(ctx(), c.ID, "key-0001"))
	if old, _ := s.BeginIdempotent(ctx(), rec); old != nil {
		t.Fatalf("after delete: %+v", old)
	}
	if n, _ := s.PruneIdempotency(ctx(), time.Now().Add(time.Hour)); n != 1 {
		t.Fatalf("PruneIdempotency: %d", n)
	}

	// Plans need a template and a pool.
	x, _ := newServer(t, s, "plan-node", now)
	dc := &store.Datacenter{ID: auth.NewID(), OrganizationID: org, Name: "dc-p"}
	must(s.CreateDatacenter(ctx(), dc))
	node := &store.Node{ID: auth.NewID(), OrganizationID: org, ServerID: x.ID, DatacenterID: dc.ID, Hypervisor: "libvirt", Network: "default",
		CPUOvercommit: 4, MemoryOvercommit: 1, Enabled: true}
	must(s.CreateNode(ctx(), node))
	pool := &store.IPPool{ID: auth.NewID(), OrganizationID: org, Name: "p", CIDR: "10.0.0.0/24", Gateway: "10.0.0.1", First: "10.0.0.10", Last: "10.0.0.20"}
	must(s.CreateIPPool(ctx(), pool))
	tpl := &store.VMTemplate{ID: auth.NewID(), OrganizationID: org, Name: "t", ImageURL: "https://x/y.img",
		ImageSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", MinDiskGB: 5, DefaultUser: "ubuntu"}
	must(s.CreateVMTemplate(ctx(), tpl))
	plan := &store.ServicePlan{ID: auth.NewID(), OrganizationID: org, Name: "vps-s", Kind: "vps", TemplateID: tpl.ID, PoolID: pool.ID, NodeID: &node.ID,
		VCPUs: 1, MemoryMB: 1024, DiskGB: 10, Enabled: true}
	must(s.CreateServicePlan(ctx(), plan))
	if p, _ := s.GetServicePlan(ctx(), plan.ID); p.NodeID == nil || p.MemoryMB != 1024 {
		t.Fatalf("plan: %+v", p)
	}
	for name, del := range map[string]func() error{
		"template": func() error { return s.DeleteVMTemplate(ctx(), tpl.ID) },
		"pool":     func() error { return s.DeleteIPPool(ctx(), pool.ID) },
		"node":     func() error { return s.DeleteNode(ctx(), node.ID) },
	} {
		if err := del(); !errors.Is(err, store.ErrConflict) {
			t.Errorf("deleting a %s used by a plan: %v", name, err)
		}
	}

	ps := &store.ProvisionedService{ID: auth.NewID(), OrganizationID: org, ClientID: c.ID, PlanID: &plan.ID, ExternalID: "whmcs-42", Status: "provisioning", Pending: "create"}
	must(s.CreateProvisionedService(ctx(), ps))
	dup := *ps
	dup.ID = auth.NewID()
	if err := s.CreateProvisionedService(ctx(), &dup); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("same external id twice: %v", err)
	}
	if got, err := s.GetProvisionedServiceByExternal(ctx(), c.ID, "whmcs-42"); err != nil || got.ID != ps.ID {
		t.Fatalf("by external id: %+v %v", got, err)
	}
	if list, _ := s.ListPendingServices(ctx(), 10); len(list) != 1 {
		t.Fatalf("pending: %d", len(list))
	}
	ps.Status, ps.Pending = "active", ""
	must(s.UpdateProvisionedService(ctx(), ps))
	if list, _ := s.ListPendingServices(ctx(), 10); len(list) != 0 {
		t.Fatalf("pending after update: %d", len(list))
	}
	if list, _ := s.ListProvisionedServices(ctx(), org); len(list) != 1 || list[0].Status != "active" {
		t.Fatalf("services: %+v", list)
	}
	tmp := &store.ProvisionedService{ID: auth.NewID(), OrganizationID: org, ClientID: c.ID, ExternalID: "refused-1", Status: "provisioning", Pending: "create"}
	must(s.CreateProvisionedService(ctx(), tmp))
	must(s.DeleteProvisionedService(ctx(), tmp.ID))
	if _, err := s.GetProvisionedServiceByExternal(ctx(), c.ID, "refused-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a forgotten service frees its external id: %v", err)
	}
	if err := s.DeleteAPIClient(ctx(), c.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("deleting a client with services: %v", err)
	}
	must(s.DeleteServicePlan(ctx(), plan.ID))
	if got, _ := s.GetProvisionedService(ctx(), ps.ID); got.PlanID != nil {
		t.Fatal("a deleted plan is detached from its services")
	}
}
