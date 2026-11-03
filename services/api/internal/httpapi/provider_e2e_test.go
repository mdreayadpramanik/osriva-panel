package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agentclient"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/provider"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const providerKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb6m1lDjp0dmKt8/a2QvYgDphMwHsUeyGnUjjZyyrnD ci"

// answer polls the agent's next task, checks its type and replies.
func answer(t *testing.T, c *agentclient.Client, typ string, reply func(p json.RawMessage) agentproto.TaskResult) json.RawMessage {
	t.Helper()
	tasks, err := c.PollTasks(context.Background())
	if err != nil || len(tasks) != 1 || tasks[0].Type != typ {
		t.Fatalf("expected one %s task: %+v %v", typ, tasks, err)
	}
	if err := c.SendResult(context.Background(), tasks[0].ID, reply(tasks[0].Params)); err != nil {
		t.Fatal(err)
	}
	return tasks[0].Params
}

func ok(state string) func(json.RawMessage) agentproto.TaskResult {
	return func(p json.RawMessage) agentproto.TaskResult {
		var ref agenttask.VMRefParams
		_ = json.Unmarshal(p, &ref)
		b, _ := json.Marshal(agenttask.VMResult{ID: ref.ID, State: state, Deleted: state == "deleted"})
		return agentproto.TaskResult{Status: "succeeded", Result: b}
	}
}

func TestProviderVPSLifecycle(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	res, agent := e.enroll(e.addServer("kvm-1"))
	sid := res.Response.ServerID
	inv, _ := json.Marshal(inventory.Inventory{SchemaVersion: inventory.SchemaVersion, OS: inventory.OSInfo{Family: "linux"},
		CPU: inventory.CPUInfo{LogicalCores: 2}, Memory: inventory.Memory{TotalBytes: 4 << 30}})
	if err := e.store().RecordReport(ctx, sid, store.ServerFacts{OSFamily: "linux"}, "127.0.0.1", time.Now(), &store.ServerInventory{CollectedAt: time.Now(), Data: inv, Capabilities: []byte("[]")}); err != nil {
		t.Fatal(err)
	}
	p := provider.New(e.store(), e.svc, e.fleet, nil)
	auditor := e.principal("auditor-pv@example.com", rbac.RoleAuditor)
	var pe *identity.PolicyError
	var ve *identity.ValidationError

	if _, err := p.CreateDatacenter(ctx, e.owner, provider.DatacenterInput{Name: "fra1"}); !errors.As(err, &pe) {
		t.Fatalf("Provider Mode off must refuse changes: %v", err)
	}
	if err := e.svc.SetPanelMode(ctx, e.owner, identity.ModeProvider); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CreateDatacenter(ctx, auditor, provider.DatacenterInput{Name: "fra1"}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor created a data center: %v", err)
	}
	dc, err := p.CreateDatacenter(ctx, e.owner, provider.DatacenterInput{Name: "fra1", Location: "Frankfurt"})
	if err != nil {
		t.Fatal(err)
	}
	rack, err := p.CreateRack(ctx, e.owner, provider.RackInput{DatacenterID: dc.ID, Name: "A01", Units: 42})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.CreateNode(ctx, e.owner, provider.NodeInput{ServerID: sid, DatacenterID: dc.ID, Network: "br0;x"}); !errors.As(err, &ve) {
		t.Fatalf("bad network name: %v", err)
	}
	node, err := p.CreateNode(ctx, e.owner, provider.NodeInput{ServerID: sid, DatacenterID: dc.ID, RackID: &rack.ID, CPUOvercommit: 2})
	if err != nil || node.VCPUs != 4 || node.MemoryMB != 4096 || node.Node.Network != "default" {
		t.Fatalf("node: %+v %v", node, err)
	}
	for _, bad := range []provider.PoolInput{
		{Name: "x", CIDR: "192.168.122.5/24", Gateway: "192.168.122.1", First: "192.168.122.10", Last: "192.168.122.20"},  // not a network address
		{Name: "x", CIDR: "192.168.122.0/24", Gateway: "10.0.0.1", First: "192.168.122.10", Last: "192.168.122.20"},       // gateway outside
		{Name: "x", CIDR: "192.168.122.0/24", Gateway: "192.168.122.1", First: "192.168.122.20", Last: "192.168.122.10"},  // reversed
		{Name: "x", CIDR: "192.168.122.0/24", Gateway: "192.168.122.1", First: "192.168.122.10", Last: "192.168.122.255"}, // broadcast
		{Name: "x", CIDR: "fd00::/64", Gateway: "fd00::1", First: "fd00::10", Last: "fd00::20"},
	} {
		if _, err := p.CreatePool(ctx, e.owner, bad); !errors.As(err, &ve) {
			t.Errorf("pool accepted: %+v %v", bad, err)
		}
	}
	pool, err := p.CreatePool(ctx, e.owner, provider.PoolInput{Name: "lan", DatacenterID: &dc.ID, CIDR: "192.168.122.0/24",
		Gateway: "192.168.122.1", First: "192.168.122.1", Last: "192.168.122.3", DNS: []string{"192.168.122.1"}})
	if err != nil || pool.Size != 2 {
		t.Fatalf("pool (the gateway is not usable): %+v %v", pool, err)
	}
	if _, err := p.CreateTemplate(ctx, e.owner, provider.TemplateInput{Name: "u", ImageURL: "http://x/y.img", ImageSHA256: "ab", DefaultUser: "ubuntu"}); !errors.As(err, &ve) {
		t.Fatalf("template over http: %v", err)
	}
	tpl, err := p.CreateTemplate(ctx, e.owner, provider.TemplateInput{Name: "ubuntu-24.04", ImageURL: "https://images.example.com/u.img",
		ImageSHA256: "0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef", MinDiskGB: 5, DefaultUser: "ubuntu"})
	if err != nil {
		t.Fatal(err)
	}

	in := provider.VPSInput{Name: "web-1", NodeID: node.Node.ID, TemplateID: tpl.ID, PoolID: pool.Pool.ID, VCPUs: 2, MemoryMB: 1024, DiskGB: 10,
		SSHKeys: []string{providerKey}}
	if _, err := p.CreateVPS(ctx, e.owner, func() provider.VPSInput { x := in; x.SSHKeys = nil; return x }()); !errors.As(err, &ve) {
		t.Fatalf("VPS without SSH keys: %v", err)
	}
	if _, err := p.CreateVPS(ctx, e.owner, func() provider.VPSInput { x := in; x.DiskGB = 2; return x }()); !errors.As(err, &ve) {
		t.Fatalf("disk below the template minimum: %v", err)
	}
	if _, err := p.CreateVPS(ctx, e.owner, func() provider.VPSInput { x := in; x.VCPUs = 5; return x }()); !errors.As(err, &pe) {
		t.Fatalf("over the node's vCPU capacity: %v", err)
	}
	v, err := p.CreateVPS(ctx, e.owner, in)
	if err != nil || v.State != "creating" || v.IPAddress != "192.168.122.2" || v.PendingTaskID == nil {
		t.Fatalf("create: %+v %v", v, err)
	}
	params := answer(t, agent, agenttask.VMCreate, ok("running"))
	var cp agenttask.VMCreateParams
	_ = json.Unmarshal(params, &cp)
	if cp.ID != v.ID || cp.Address != "192.168.122.2/24" || cp.Gateway != "192.168.122.1" || cp.Network != "default" || cp.User != "ubuntu" ||
		cp.ImageSHA256 != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" || cp.SSHKeys[0] != providerKey {
		t.Fatalf("create params: %+v", cp)
	}
	if got, _ := p.GetVPS(ctx, auditor, v.ID); got.State != "running" || got.PendingTaskID != nil {
		t.Fatalf("after create: %+v", got)
	}

	// The second VPS takes the next address; the pool is then exhausted.
	v2, err := p.CreateVPS(ctx, e.owner, func() provider.VPSInput { x := in; x.Name = "web-2"; return x }())
	if err != nil || v2.IPAddress != "192.168.122.3" {
		t.Fatalf("second VPS: %+v %v", v2, err)
	}
	if _, err := p.CreateVPS(ctx, e.owner, func() provider.VPSInput { x := in; x.Name = "web-3"; x.VCPUs = 0; return x }()); err == nil {
		t.Fatal("zero vCPUs accepted")
	}
	if _, err := p.CreateVPS(ctx, e.owner, func() provider.VPSInput { x := in; x.Name = "web-3"; x.VCPUs = 0; x.VCPUs = 1; return x }()); !errors.As(err, &pe) {
		t.Fatalf("pool exhausted: %v", err)
	}
	// The second create fails on the node: the VPS shows the error and keeps its address until deleted.
	answer(t, agent, agenttask.VMCreate, func(json.RawMessage) agentproto.TaskResult {
		return agentproto.TaskResult{Status: "failed", Error: "image checksum mismatch"}
	})
	if got, _ := p.GetVPS(ctx, e.owner, v2.ID); got.State != "error" || got.LastError != "create failed: image checksum mismatch" {
		t.Fatalf("failed create: %+v", got)
	}
	if _, err := p.Action(ctx, e.owner, v2.ID, agenttask.VMStart); !errors.As(err, &pe) {
		t.Fatalf("starting a failed VPS: %v", err)
	}

	// Actions: shutdown, then a second action while one is pending is refused.
	if _, err := p.Action(ctx, e.owner, v.ID, agenttask.VMShutdown); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Action(ctx, e.owner, v.ID, agenttask.VMStart); !errors.As(err, &pe) {
		t.Fatalf("concurrent action: %v", err)
	}
	answer(t, agent, agenttask.VMAction, ok("stopped"))
	if got, _ := p.GetVPS(ctx, e.owner, v.ID); got.State != "stopped" {
		t.Fatalf("after shutdown: %+v", got)
	}
	stale := *e.owner
	sess := *e.owner.Session
	old := time.Now().Add(-time.Hour)
	sess.ReauthenticatedAt = &old
	stale.Session = &sess
	if _, err := p.Action(ctx, &stale, v.ID, agenttask.VMStop); !errors.Is(err, identity.ErrReauthRequired) {
		t.Fatalf("force stop without re-auth: %v", err)
	}
	if _, err := p.Delete(ctx, &stale, v.ID, "web-1"); !errors.Is(err, identity.ErrReauthRequired) {
		t.Fatalf("delete without re-auth: %v", err)
	}
	if _, err := p.Delete(ctx, e.owner, v.ID, "web"); !errors.As(err, &ve) {
		t.Fatalf("delete without the typed name: %v", err)
	}
	// A failed delete restores the previous state.
	if _, err := p.Delete(ctx, e.owner, v.ID, "web-1"); err != nil {
		t.Fatal(err)
	}
	answer(t, agent, agenttask.VMDelete, func(json.RawMessage) agentproto.TaskResult {
		return agentproto.TaskResult{Status: "failed", Error: "libvirtd is not running"}
	})
	if got, _ := p.GetVPS(ctx, e.owner, v.ID); got.State != "stopped" || got.LastError == "" {
		t.Fatalf("after a failed delete: %+v", got)
	}
	for _, x := range []*store.VPS{v, v2} {
		if _, err := p.Delete(ctx, e.owner, x.ID, x.Name); err != nil {
			t.Fatal(err)
		}
		answer(t, agent, agenttask.VMDelete, ok("deleted"))
	}
	if list, _ := p.ListVPS(ctx, e.owner); len(list) != 0 {
		t.Fatalf("VPS left after delete: %+v", list)
	}
	pools, _ := p.ListPools(ctx, e.owner)
	if len(pools) != 1 || len(pools[0].Allocated) != 0 {
		t.Fatalf("addresses released: %+v", pools)
	}
	if err := p.DeleteNode(ctx, e.owner, node.Node.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteDatacenter(ctx, e.owner, dc.ID); !errors.As(err, &pe) {
		t.Fatalf("data center with a rack and a pool: %v", err)
	}
	audit, _ := e.store().ListAudit(ctx, store.AuditFilter{OrganizationID: store.DefaultOrganizationID, ActionPrefix: "provider.", Limit: 100})
	actions := map[string]int{}
	for _, a := range audit {
		actions[a.Action]++
	}
	if actions["provider.vps_create"] != 2 || actions["provider.vps_delete"] != 3 || actions["provider.vps_deleted"] != 2 {
		t.Fatalf("audit: %v", actions)
	}
}
