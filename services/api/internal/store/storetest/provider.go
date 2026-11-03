package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func testProvider(t *testing.T, s store.Store) {
	org := store.DefaultOrganizationID
	now := time.Now().UTC()
	x, _ := newServer(t, s, "kvm-1", now)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	dc := &store.Datacenter{ID: auth.NewID(), OrganizationID: org, Name: "fra1", Location: "Frankfurt"}
	must(s.CreateDatacenter(ctx(), dc))
	if err := s.CreateDatacenter(ctx(), &store.Datacenter{ID: auth.NewID(), OrganizationID: org, Name: "fra1"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate data center: %v", err)
	}
	rack := &store.Rack{ID: auth.NewID(), DatacenterID: dc.ID, Name: "A01", Units: 42}
	must(s.CreateRack(ctx(), rack))
	if racks, _ := s.ListRacks(ctx(), dc.ID); len(racks) != 1 || racks[0].Units != 42 {
		t.Fatalf("racks: %+v", racks)
	}

	node := &store.Node{ID: auth.NewID(), OrganizationID: org, ServerID: x.ID, DatacenterID: dc.ID, RackID: &rack.ID, Hypervisor: "libvirt",
		Network: "default", CPUOvercommit: 4, MemoryOvercommit: 1, Enabled: true}
	must(s.CreateNode(ctx(), node))
	if err := s.CreateNode(ctx(), &store.Node{ID: auth.NewID(), OrganizationID: org, ServerID: x.ID, DatacenterID: dc.ID, Hypervisor: "libvirt",
		Network: "default", CPUOvercommit: 1, MemoryOvercommit: 1}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a server is one node: %v", err)
	}
	if got, _ := s.GetNode(ctx(), node.ID); got.RackID == nil || got.CPUOvercommit != 4 || got.Network != "default" {
		t.Fatalf("node: %+v", got)
	}

	pool := &store.IPPool{ID: auth.NewID(), OrganizationID: org, DatacenterID: &dc.ID, Name: "lan", CIDR: "192.168.122.0/24",
		Gateway: "192.168.122.1", First: "192.168.122.100", Last: "192.168.122.150", DNS: []string{"192.168.122.1"}}
	must(s.CreateIPPool(ctx(), pool))
	if got, _ := s.GetIPPool(ctx(), pool.ID); got.CIDR != "192.168.122.0/24" || got.Gateway != "192.168.122.1" || got.First != "192.168.122.100" ||
		len(got.DNS) != 1 {
		t.Fatalf("pool: %+v", got)
	}
	tpl := &store.VMTemplate{ID: auth.NewID(), OrganizationID: org, Name: "ubuntu-24.04", ImageURL: "https://example.com/u.img",
		ImageSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", MinDiskGB: 5, DefaultUser: "ubuntu"}
	must(s.CreateVMTemplate(ctx(), tpl))

	task := &store.Task{ID: auth.NewID(), ServerID: x.ID, Type: "vm.create", ExpiresAt: now.Add(time.Minute)}
	must(s.CreateTask(ctx(), task))
	v := &store.VPS{ID: auth.NewID(), OrganizationID: org, NodeID: node.ID, TemplateID: &tpl.ID, PoolID: pool.ID, Name: "web-1", VCPUs: 2,
		MemoryMB: 2048, DiskGB: 20, IPAddress: "192.168.122.100", SSHKeys: []string{"ssh-ed25519 AAAA x"}, State: "creating",
		PendingTaskID: &task.ID, PendingOp: "create"}
	must(s.CreateVPS(ctx(), v))
	same := *v
	same.ID, same.Name = auth.NewID(), "web-2"
	if err := s.CreateVPS(ctx(), &same); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("one address, two VPS: %v", err)
	}
	if _, err := s.GetVPS(ctx(), same.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a failed allocation must not leave a VPS: %v", err)
	}
	dup := *v
	dup.ID, dup.IPAddress = auth.NewID(), "192.168.122.101"
	if err := s.CreateVPS(ctx(), &dup); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if addrs, _ := s.ListPoolAddresses(ctx(), pool.ID); len(addrs) != 1 || addrs[0] != "192.168.122.100" {
		t.Fatalf("allocated: %v", addrs)
	}
	got, err := s.GetVPS(ctx(), v.ID)
	if err != nil || got.IPAddress != "192.168.122.100" || got.PendingTaskID == nil || len(got.SSHKeys) != 1 || got.State != "creating" {
		t.Fatalf("vps: %+v %v", got, err)
	}
	must(s.SetVPSState(ctx(), v.ID, "running", nil, "", ""))
	if got, _ := s.GetVPS(ctx(), v.ID); got.State != "running" || got.PendingTaskID != nil {
		t.Fatalf("state: %+v", got)
	}
	if list, _ := s.ListNodeVPS(ctx(), node.ID); len(list) != 1 {
		t.Fatalf("node vps: %d", len(list))
	}

	// Everything still referenced refuses deletion.
	for name, del := range map[string]func() error{
		"pool": func() error { return s.DeleteIPPool(ctx(), pool.ID) },
		"node": func() error { return s.DeleteNode(ctx(), node.ID) },
		"rack": func() error { return s.DeleteRack(ctx(), rack.ID) },
		"dc":   func() error { return s.DeleteDatacenter(ctx(), dc.ID) },
	} {
		if err := del(); !errors.Is(err, store.ErrConflict) {
			t.Errorf("deleting a %s in use: %v", name, err)
		}
	}
	must(s.DeleteVMTemplate(ctx(), tpl.ID))
	if got, _ := s.GetVPS(ctx(), v.ID); got.TemplateID != nil {
		t.Fatal("a deleted template is detached from its VPS")
	}
	must(s.DeleteVPS(ctx(), v.ID))
	if addrs, _ := s.ListPoolAddresses(ctx(), pool.ID); len(addrs) != 0 {
		t.Fatalf("address not released: %v", addrs)
	}
	must(s.DeleteIPPool(ctx(), pool.ID))
	must(s.DeleteNode(ctx(), node.ID))
	must(s.DeleteRack(ctx(), rack.ID))
	must(s.DeleteDatacenter(ctx(), dc.ID))
	if list, _ := s.ListDatacenters(ctx(), org); len(list) != 0 {
		t.Fatalf("data centers left: %+v", list)
	}
}
