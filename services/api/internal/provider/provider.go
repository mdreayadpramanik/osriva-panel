// Package provider implements Provider Mode: data centers, racks, hypervisor
// nodes, IP pools, VM templates and the VPS lifecycle on libvirt/KVM nodes
// (docs/adr/0013-provider-mode-libvirt.md). There is deliberately no billing.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const (
	StateCreating = "creating"
	StateRunning  = "running"
	StateStopped  = "stopped"
	StateError    = "error"
	StateDeleting = "deleting"
)

type Service struct {
	store store.Store
	id    *identity.Service
	fleet *fleet.Service
	log   *slog.Logger
}

func New(st store.Store, id *identity.Service, fl *fleet.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: st, id: id, fleet: fl, log: log}
}

func verr(field, msg string) error { return &identity.ValidationError{Field: field, Message: msg} }

func validName(field, s string) error {
	if s == "" || len(s) > 100 || strings.ContainsAny(s, "\r\n\t\x00") {
		return verr(field, "must be 1-100 characters on one line")
	}
	return nil
}

// requireWrite checks provider.write and that the panel runs in Provider Mode.
func (s *Service) requireWrite(ctx context.Context, actor *identity.Principal) error {
	if err := s.id.Require(ctx, actor, rbac.ProviderWrite); err != nil {
		return err
	}
	mode, err := s.id.PanelMode(ctx)
	if err != nil {
		return err
	}
	if mode != identity.ModeProvider {
		return &identity.PolicyError{Message: "Provider Mode is off: switch the panel to Hosting Provider Mode in Settings first"}
	}
	return nil
}

func (s *Service) requireRead(ctx context.Context, actor *identity.Principal) error {
	return s.id.Require(ctx, actor, rbac.ProviderRead)
}

func inUse(err error, what string) error {
	if errors.Is(err, store.ErrConflict) {
		return &identity.PolicyError{Message: what + " is still in use: remove what refers to it first"}
	}
	return err
}

func (s *Service) audit(ctx context.Context, actor *identity.Principal, action, kind, id string, md map[string]any) {
	s.id.Audit(ctx, actor.User, action, kind, id, store.OutcomeSuccess, md)
}

// ---- data centers and racks ----

type DatacenterInput struct {
	Name     string `json:"name"`
	Location string `json:"location"`
}

func (s *Service) ListDatacenters(ctx context.Context, actor *identity.Principal) ([]store.Datacenter, error) {
	if err := s.requireRead(ctx, actor); err != nil {
		return nil, err
	}
	return s.store.ListDatacenters(ctx, actor.User.OrganizationID)
}

func (s *Service) dcInOrg(ctx context.Context, actor *identity.Principal, id string) (*store.Datacenter, error) {
	d, err := s.store.GetDatacenter(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	return d, nil
}

func (s *Service) CreateDatacenter(ctx context.Context, actor *identity.Principal, in DatacenterInput) (*store.Datacenter, error) {
	if err := s.requireWrite(ctx, actor); err != nil {
		return nil, err
	}
	in.Name, in.Location = strings.TrimSpace(in.Name), strings.TrimSpace(in.Location)
	if err := validName("name", in.Name); err != nil {
		return nil, err
	}
	if len(in.Location) > 200 || strings.ContainsAny(in.Location, "\r\n\x00") {
		return nil, verr("location", "must be at most 200 characters on one line")
	}
	d := &store.Datacenter{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, Name: in.Name, Location: in.Location}
	if err := s.store.CreateDatacenter(ctx, d); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, verr("name", "is already used by another data center")
		}
		return nil, err
	}
	s.audit(ctx, actor, "provider.datacenter_create", "datacenter", d.ID, map[string]any{"name": d.Name})
	return d, nil
}

func (s *Service) DeleteDatacenter(ctx context.Context, actor *identity.Principal, id string) error {
	if err := s.requireWrite(ctx, actor); err != nil {
		return err
	}
	d, err := s.dcInOrg(ctx, actor, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteDatacenter(ctx, d.ID); err != nil {
		return inUse(err, "the data center")
	}
	s.audit(ctx, actor, "provider.datacenter_delete", "datacenter", d.ID, map[string]any{"name": d.Name})
	return nil
}

type RackInput struct {
	DatacenterID string `json:"datacenter_id"`
	Name         string `json:"name"`
	Units        int    `json:"units"`
}

func (s *Service) ListRacks(ctx context.Context, actor *identity.Principal, datacenterID string) ([]store.Rack, error) {
	if err := s.requireRead(ctx, actor); err != nil {
		return nil, err
	}
	if _, err := s.dcInOrg(ctx, actor, datacenterID); err != nil {
		return nil, err
	}
	return s.store.ListRacks(ctx, datacenterID)
}

func (s *Service) CreateRack(ctx context.Context, actor *identity.Principal, in RackInput) (*store.Rack, error) {
	if err := s.requireWrite(ctx, actor); err != nil {
		return nil, err
	}
	if _, err := s.dcInOrg(ctx, actor, in.DatacenterID); err != nil {
		return nil, verr("datacenter_id", "unknown data center")
	}
	in.Name = strings.TrimSpace(in.Name)
	if err := validName("name", in.Name); err != nil {
		return nil, err
	}
	if in.Units < 1 || in.Units > 100 {
		return nil, verr("units", "must be 1-100")
	}
	r := &store.Rack{ID: auth.NewID(), DatacenterID: in.DatacenterID, Name: in.Name, Units: in.Units}
	if err := s.store.CreateRack(ctx, r); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, verr("name", "is already used by another rack in this data center")
		}
		return nil, err
	}
	s.audit(ctx, actor, "provider.rack_create", "rack", r.ID, map[string]any{"name": r.Name, "datacenter_id": r.DatacenterID})
	return r, nil
}

func (s *Service) rackInOrg(ctx context.Context, actor *identity.Principal, id string) (*store.Rack, error) {
	r, err := s.store.GetRack(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.dcInOrg(ctx, actor, r.DatacenterID); err != nil {
		return nil, store.ErrNotFound
	}
	return r, nil
}

func (s *Service) DeleteRack(ctx context.Context, actor *identity.Principal, id string) error {
	if err := s.requireWrite(ctx, actor); err != nil {
		return err
	}
	r, err := s.rackInOrg(ctx, actor, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteRack(ctx, r.ID); err != nil {
		return inUse(err, "the rack")
	}
	s.audit(ctx, actor, "provider.rack_delete", "rack", r.ID, map[string]any{"name": r.Name})
	return nil
}

// ---- nodes ----

type NodeInput struct {
	ServerID         string  `json:"server_id"`
	DatacenterID     string  `json:"datacenter_id"`
	RackID           *string `json:"rack_id"`
	Network          string  `json:"network"`
	CPUOvercommit    float64 `json:"cpu_overcommit"`
	MemoryOvercommit float64 `json:"memory_overcommit"`
}

// NodeView is a node with its server and capacity.
type NodeView struct {
	Node       store.Node
	ServerName string
	Connection string
	// Capacity from the server's last inventory report, times the overcommit ratios.
	VCPUs, MemoryMB         int
	UsedVCPUs, UsedMemoryMB int
	VPSCount                int
}

func (s *Service) nodeView(ctx context.Context, actor *identity.Principal, n *store.Node) (*NodeView, error) {
	v := &NodeView{Node: *n}
	if d, err := s.fleet.Get(ctx, actor, n.ServerID); err == nil {
		v.ServerName, v.Connection = d.Server.Name, string(d.Connection)
		if d.Inventory != nil {
			v.VCPUs = int(float64(d.Inventory.CPU.LogicalCores) * n.CPUOvercommit)
			v.MemoryMB = int(float64(d.Inventory.Memory.TotalBytes>>20) * n.MemoryOvercommit)
		}
	}
	list, err := s.store.ListNodeVPS(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	for _, x := range list {
		v.UsedVCPUs += x.VCPUs
		v.UsedMemoryMB += x.MemoryMB
	}
	v.VPSCount = len(list)
	return v, nil
}

func (s *Service) nodeInOrg(ctx context.Context, actor *identity.Principal, id string) (*store.Node, error) {
	n, err := s.store.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	if n.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	return n, nil
}

func (s *Service) ListNodes(ctx context.Context, actor *identity.Principal) ([]NodeView, error) {
	if err := s.requireRead(ctx, actor); err != nil {
		return nil, err
	}
	nodes, err := s.store.ListNodes(ctx, actor.User.OrganizationID)
	if err != nil {
		return nil, err
	}
	out := make([]NodeView, 0, len(nodes))
	for i := range nodes {
		v, err := s.nodeView(ctx, actor, &nodes[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

func (s *Service) CreateNode(ctx context.Context, actor *identity.Principal, in NodeInput) (*NodeView, error) {
	if err := s.requireWrite(ctx, actor); err != nil {
		return nil, err
	}
	d, err := s.fleet.Get(ctx, actor, in.ServerID)
	if err != nil {
		return nil, verr("server_id", "unknown server")
	}
	if d.Server.Status != store.ServerActive {
		return nil, verr("server_id", "the server's agent must be enrolled")
	}
	if d.Inventory != nil && d.Inventory.OS.Family != "linux" {
		return nil, verr("server_id", "libvirt/KVM nodes must run Linux")
	}
	if _, err := s.dcInOrg(ctx, actor, in.DatacenterID); err != nil {
		return nil, verr("datacenter_id", "unknown data center")
	}
	if in.RackID != nil {
		r, err := s.rackInOrg(ctx, actor, *in.RackID)
		if err != nil || r.DatacenterID != in.DatacenterID {
			return nil, verr("rack_id", "unknown rack in this data center")
		}
	}
	if in.Network == "" {
		in.Network = "default"
	}
	if !agenttask.ValidVMNetwork(in.Network) {
		return nil, verr("network", "invalid libvirt network name")
	}
	if in.CPUOvercommit == 0 {
		in.CPUOvercommit = 4
	}
	if in.MemoryOvercommit == 0 {
		in.MemoryOvercommit = 1
	}
	if in.CPUOvercommit < 1 || in.CPUOvercommit > 16 {
		return nil, verr("cpu_overcommit", "must be 1-16")
	}
	if in.MemoryOvercommit < 1 || in.MemoryOvercommit > 4 {
		return nil, verr("memory_overcommit", "must be 1-4")
	}
	n := &store.Node{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, ServerID: in.ServerID, DatacenterID: in.DatacenterID,
		RackID: in.RackID, Hypervisor: "libvirt", Network: in.Network, CPUOvercommit: in.CPUOvercommit, MemoryOvercommit: in.MemoryOvercommit, Enabled: true}
	if err := s.store.CreateNode(ctx, n); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, verr("server_id", "this server is already a node")
		}
		return nil, err
	}
	s.audit(ctx, actor, "provider.node_create", "node", n.ID, map[string]any{"server": d.Server.Name, "network": n.Network})
	return s.nodeView(ctx, actor, n)
}

func (s *Service) DeleteNode(ctx context.Context, actor *identity.Principal, id string) error {
	if err := s.requireWrite(ctx, actor); err != nil {
		return err
	}
	n, err := s.nodeInOrg(ctx, actor, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteNode(ctx, n.ID); err != nil {
		return inUse(err, "the node (it has VPS)")
	}
	s.audit(ctx, actor, "provider.node_delete", "node", n.ID, map[string]any{"server_id": n.ServerID})
	return nil
}

// ---- IP pools ----

type PoolInput struct {
	Name         string   `json:"name"`
	DatacenterID *string  `json:"datacenter_id"`
	CIDR         string   `json:"cidr"`
	Gateway      string   `json:"gateway"`
	First        string   `json:"first"`
	Last         string   `json:"last"`
	DNS          []string `json:"dns"`
}

type PoolView struct {
	Pool      store.IPPool
	Size      int
	Allocated []string
}

func checkPool(in *PoolInput) (netip.Prefix, error) {
	pfx, err := netip.ParsePrefix(strings.TrimSpace(in.CIDR))
	if err != nil || !pfx.Addr().Is4() || pfx.Bits() < 8 || pfx.Bits() > 30 || pfx.Masked() != pfx {
		return pfx, verr("cidr", "must be an IPv4 network such as 192.0.2.0/24 (/8-/30)")
	}
	in.CIDR = pfx.String()
	parse := func(field, v string) (netip.Addr, error) {
		a, err := netip.ParseAddr(strings.TrimSpace(v))
		if err != nil || !a.Is4() || !pfx.Contains(a) || a == pfx.Addr() || a == lastAddr(pfx) {
			return a, verr(field, "must be a host address inside "+pfx.String())
		}
		return a, nil
	}
	gw, err := parse("gateway", in.Gateway)
	if err != nil {
		return pfx, err
	}
	first, err := parse("first", in.First)
	if err != nil {
		return pfx, err
	}
	last, err := parse("last", in.Last)
	if err != nil {
		return pfx, err
	}
	if last.Less(first) {
		return pfx, verr("last", "must not be before first")
	}
	if count(first, last) > 65536 {
		return pfx, verr("last", "a pool holds at most 65536 addresses")
	}
	in.Gateway, in.First, in.Last = gw.String(), first.String(), last.String()
	if len(in.DNS) > 3 {
		return pfx, verr("dns", "at most 3 servers")
	}
	for i, d := range in.DNS {
		a, err := netip.ParseAddr(strings.TrimSpace(d))
		if err != nil || !a.Is4() {
			return pfx, verr("dns", "must be IPv4 addresses")
		}
		in.DNS[i] = a.String()
	}
	return pfx, nil
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]) | host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func count(first, last netip.Addr) int {
	n := 1
	for a := first; a != last && n <= 65537; a = a.Next() {
		n++
	}
	return n
}

func (s *Service) poolInOrg(ctx context.Context, actor *identity.Principal, id string) (*store.IPPool, error) {
	p, err := s.store.GetIPPool(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	return p, nil
}

func (s *Service) poolView(ctx context.Context, p *store.IPPool) (*PoolView, error) {
	used, err := s.store.ListPoolAddresses(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	first, _ := netip.ParseAddr(p.First)
	last, _ := netip.ParseAddr(p.Last)
	size := count(first, last)
	if gw, _ := netip.ParseAddr(p.Gateway); !gw.Less(first) && !last.Less(gw) {
		size--
	}
	return &PoolView{Pool: *p, Size: size, Allocated: used}, nil
}

func (s *Service) ListPools(ctx context.Context, actor *identity.Principal) ([]PoolView, error) {
	if err := s.requireRead(ctx, actor); err != nil {
		return nil, err
	}
	list, err := s.store.ListIPPools(ctx, actor.User.OrganizationID)
	if err != nil {
		return nil, err
	}
	out := make([]PoolView, 0, len(list))
	for i := range list {
		v, err := s.poolView(ctx, &list[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

func (s *Service) CreatePool(ctx context.Context, actor *identity.Principal, in PoolInput) (*PoolView, error) {
	if err := s.requireWrite(ctx, actor); err != nil {
		return nil, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if err := validName("name", in.Name); err != nil {
		return nil, err
	}
	if in.DatacenterID != nil {
		if _, err := s.dcInOrg(ctx, actor, *in.DatacenterID); err != nil {
			return nil, verr("datacenter_id", "unknown data center")
		}
	}
	if _, err := checkPool(&in); err != nil {
		return nil, err
	}
	p := &store.IPPool{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, DatacenterID: in.DatacenterID, Name: in.Name,
		CIDR: in.CIDR, Gateway: in.Gateway, First: in.First, Last: in.Last, DNS: in.DNS}
	if p.DNS == nil {
		p.DNS = []string{}
	}
	if err := s.store.CreateIPPool(ctx, p); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, verr("name", "is already used by another pool")
		}
		return nil, err
	}
	s.audit(ctx, actor, "provider.pool_create", "ip_pool", p.ID, map[string]any{"name": p.Name, "cidr": p.CIDR, "first": p.First, "last": p.Last})
	return s.poolView(ctx, p)
}

func (s *Service) DeletePool(ctx context.Context, actor *identity.Principal, id string) error {
	if err := s.requireWrite(ctx, actor); err != nil {
		return err
	}
	p, err := s.poolInOrg(ctx, actor, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteIPPool(ctx, p.ID); err != nil {
		return inUse(err, "the pool (addresses are allocated)")
	}
	s.audit(ctx, actor, "provider.pool_delete", "ip_pool", p.ID, map[string]any{"name": p.Name})
	return nil
}

// ---- templates ----

type TemplateInput struct {
	Name        string `json:"name"`
	ImageURL    string `json:"image_url"`
	ImageSHA256 string `json:"image_sha256"`
	MinDiskGB   int    `json:"min_disk_gb"`
	DefaultUser string `json:"default_user"`
}

func (s *Service) ListTemplates(ctx context.Context, actor *identity.Principal) ([]store.VMTemplate, error) {
	if err := s.requireRead(ctx, actor); err != nil {
		return nil, err
	}
	return s.store.ListVMTemplates(ctx, actor.User.OrganizationID)
}

func (s *Service) templateInOrg(ctx context.Context, actor *identity.Principal, id string) (*store.VMTemplate, error) {
	t, err := s.store.GetVMTemplate(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	return t, nil
}

func (s *Service) CreateTemplate(ctx context.Context, actor *identity.Principal, in TemplateInput) (*store.VMTemplate, error) {
	if err := s.requireWrite(ctx, actor); err != nil {
		return nil, err
	}
	in.Name, in.ImageURL, in.DefaultUser = strings.TrimSpace(in.Name), strings.TrimSpace(in.ImageURL), strings.TrimSpace(in.DefaultUser)
	in.ImageSHA256 = strings.ToLower(strings.TrimSpace(in.ImageSHA256))
	if err := validName("name", in.Name); err != nil {
		return nil, err
	}
	if !agenttask.ValidImageURL(in.ImageURL) {
		return nil, verr("image_url", "must be an https:// URL without credentials")
	}
	if !isHex64(in.ImageSHA256) {
		return nil, verr("image_sha256", "must be the image's SHA-256 (64 hex characters)")
	}
	if !agenttask.UserName.MatchString(in.DefaultUser) {
		return nil, verr("default_user", "must be the image's default user, e.g. ubuntu or debian")
	}
	if in.MinDiskGB == 0 {
		in.MinDiskGB = 5
	}
	if in.MinDiskGB < 1 || in.MinDiskGB > 4096 {
		return nil, verr("min_disk_gb", "must be 1-4096")
	}
	t := &store.VMTemplate{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, Name: in.Name, ImageURL: in.ImageURL,
		ImageSHA256: in.ImageSHA256, MinDiskGB: in.MinDiskGB, DefaultUser: in.DefaultUser}
	if err := s.store.CreateVMTemplate(ctx, t); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, verr("name", "is already used by another template")
		}
		return nil, err
	}
	s.audit(ctx, actor, "provider.template_create", "vm_template", t.ID, map[string]any{"name": t.Name, "image_sha256": t.ImageSHA256})
	return t, nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s *Service) DeleteTemplate(ctx context.Context, actor *identity.Principal, id string) error {
	if err := s.requireWrite(ctx, actor); err != nil {
		return err
	}
	t, err := s.templateInOrg(ctx, actor, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteVMTemplate(ctx, t.ID); err != nil {
		return err
	}
	s.audit(ctx, actor, "provider.template_delete", "vm_template", t.ID, map[string]any{"name": t.Name})
	return nil
}

// ---- VPS ----

type VPSInput struct {
	Name       string   `json:"name"`
	NodeID     string   `json:"node_id"`
	TemplateID string   `json:"template_id"`
	PoolID     string   `json:"pool_id"`
	VCPUs      int      `json:"vcpus"`
	MemoryMB   int      `json:"memory_mb"`
	DiskGB     int      `json:"disk_gb"`
	SSHKeys    []string `json:"ssh_keys"`
}

// pendingOp stores the operation and the state before it ("start:stopped"),
// so a failed operation can restore the previous state.
func pendingOp(op, prev string) string { return op + ":" + prev }

func splitOp(s string) (op, prev string) {
	op, prev, _ = strings.Cut(s, ":")
	return op, prev
}

// nextAddress returns the lowest free address of the pool, skipping the gateway.
func nextAddress(p *store.IPPool, used []string, skip map[string]bool) (string, bool) {
	first, err1 := netip.ParseAddr(p.First)
	last, err2 := netip.ParseAddr(p.Last)
	if err1 != nil || err2 != nil {
		return "", false
	}
	taken := map[string]bool{p.Gateway: true}
	for _, u := range used {
		taken[u] = true
	}
	for a := first; ; a = a.Next() {
		if !taken[a.String()] && !skip[a.String()] {
			return a.String(), true
		}
		if a == last {
			return "", false
		}
	}
}

func (s *Service) CreateVPS(ctx context.Context, actor *identity.Principal, in VPSInput) (*store.VPS, error) {
	if err := s.requireWrite(ctx, actor); err != nil {
		return nil, err
	}
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	if !agenttask.ValidVMHostname(in.Name) {
		return nil, verr("name", "must be a host name: 1-63 lowercase letters, digits or dashes")
	}
	n, err := s.nodeInOrg(ctx, actor, in.NodeID)
	if err != nil {
		return nil, verr("node_id", "unknown node")
	}
	if !n.Enabled {
		return nil, &identity.PolicyError{Message: "the node is disabled"}
	}
	tpl, err := s.templateInOrg(ctx, actor, in.TemplateID)
	if err != nil {
		return nil, verr("template_id", "unknown template")
	}
	pool, err := s.poolInOrg(ctx, actor, in.PoolID)
	if err != nil {
		return nil, verr("pool_id", "unknown IP pool")
	}
	if in.DiskGB == 0 {
		in.DiskGB = tpl.MinDiskGB
	}
	if in.DiskGB < tpl.MinDiskGB {
		return nil, verr("disk_gb", fmt.Sprintf("this template needs at least %d GB", tpl.MinDiskGB))
	}
	keys := slices.Compact(slices.Clone(in.SSHKeys))
	nv, err := s.nodeView(ctx, actor, n)
	if err != nil {
		return nil, err
	}
	if nv.VCPUs > 0 && nv.UsedVCPUs+in.VCPUs > nv.VCPUs {
		return nil, &identity.PolicyError{Message: fmt.Sprintf("the node has %d of %d vCPUs left", nv.VCPUs-nv.UsedVCPUs, nv.VCPUs)}
	}
	if nv.MemoryMB > 0 && nv.UsedMemoryMB+in.MemoryMB > nv.MemoryMB {
		return nil, &identity.PolicyError{Message: fmt.Sprintf("the node has %d of %d MB memory left", nv.MemoryMB-nv.UsedMemoryMB, nv.MemoryMB)}
	}
	if nv.VCPUs == 0 {
		return nil, &identity.PolicyError{Message: "the node has not reported its hardware yet"}
	}
	pfx, _ := netip.ParsePrefix(pool.CIDR)
	v := &store.VPS{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, NodeID: n.ID, TemplateID: &tpl.ID, PoolID: pool.ID, Name: in.Name,
		VCPUs: in.VCPUs, MemoryMB: in.MemoryMB, DiskGB: in.DiskGB, SSHKeys: keys, State: StateCreating, CreatedBy: &actor.User.ID}
	params := agenttask.VMCreateParams{ID: v.ID, Hostname: v.Name, VCPUs: v.VCPUs, MemoryMB: v.MemoryMB, DiskGB: v.DiskGB, ImageURL: tpl.ImageURL,
		ImageSHA256: tpl.ImageSHA256, Network: n.Network, Gateway: pool.Gateway, DNS: pool.DNS, User: tpl.DefaultUser, SSHKeys: keys}
	// Validate everything (sizes, keys) with the agent's own rules before allocating.
	first, _ := netip.ParseAddr(pool.First)
	if first.String() == pool.Gateway {
		first = first.Next()
	}
	params.Address = netip.PrefixFrom(first, pfx.Bits()).String()
	if err := agenttask.Validate(agenttask.VMCreate, mustJSON(params)); err != nil {
		return nil, verr("vps", strings.TrimPrefix(err.Error(), agenttask.ErrInvalid.Error()+": "))
	}
	skip := map[string]bool{}
	allocated := false
	for attempt := 0; attempt < 8 && !allocated; attempt++ {
		used, err := s.store.ListPoolAddresses(ctx, pool.ID)
		if err != nil {
			return nil, err
		}
		addr, ok := nextAddress(pool, used, skip)
		if !ok {
			return nil, &identity.PolicyError{Message: "the IP pool " + pool.Name + " has no free address"}
		}
		v.IPAddress = addr
		switch err := s.store.CreateVPS(ctx, v); {
		case err == nil:
			allocated = true
		case errors.Is(err, store.ErrConflict):
			if _, err := s.findVPSByName(ctx, actor, v.Name); err == nil {
				return nil, verr("name", "is already used by another VPS")
			}
			skip[addr] = true // taken concurrently: try the next one
		default:
			return nil, err
		}
	}
	if !allocated {
		return nil, &identity.PolicyError{Message: "could not allocate an address, try again"}
	}
	a, _ := netip.ParseAddr(v.IPAddress)
	params.Address = netip.PrefixFrom(a, pfx.Bits()).String()
	t, err := s.fleet.QueueTask(ctx, actor, n.ServerID, agenttask.VMCreate, mustJSON(params))
	if err != nil {
		_ = s.store.DeleteVPS(ctx, v.ID) // release the address
		return nil, err
	}
	if err := s.store.SetVPSState(ctx, v.ID, StateCreating, &t.ID, pendingOp("create", StateCreating), ""); err != nil {
		return nil, err
	}
	s.audit(ctx, actor, "provider.vps_create", "vps", v.ID, map[string]any{"name": v.Name, "node_id": n.ID, "ip": v.IPAddress,
		"vcpus": v.VCPUs, "memory_mb": v.MemoryMB, "disk_gb": v.DiskGB, "template": tpl.Name, "task_id": t.ID})
	return s.store.GetVPS(ctx, v.ID)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (s *Service) findVPSByName(ctx context.Context, actor *identity.Principal, name string) (*store.VPS, error) {
	list, err := s.store.ListVPS(ctx, actor.User.OrganizationID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// reconcile applies the outcome of a finished pending task; it returns nil
// when the VPS was deleted.
func (s *Service) reconcile(ctx context.Context, v *store.VPS) (*store.VPS, error) {
	if v.PendingTaskID == nil {
		return v, nil
	}
	t, err := s.store.GetTask(ctx, *v.PendingTaskID)
	if err != nil {
		return v, nil
	}
	if t.Status == store.TaskQueued || t.Status == store.TaskRunning {
		return v, nil
	}
	op, prev := splitOp(v.PendingOp)
	if t.Status == store.TaskSucceeded {
		var r agenttask.VMResult
		_ = json.Unmarshal(t.Result, &r)
		if op == "delete" {
			if err := s.store.DeleteVPS(ctx, v.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return v, err
			}
			s.id.Audit(ctx, nil, "provider.vps_deleted", "vps", v.ID, store.OutcomeSuccess, map[string]any{"name": v.Name, "ip": v.IPAddress})
			return nil, nil
		}
		state := StateStopped
		if r.State == "running" {
			state = StateRunning
		}
		if err := s.store.SetVPSState(ctx, v.ID, state, nil, "", ""); err != nil {
			return v, err
		}
	} else {
		msg := t.Error
		if msg == "" {
			msg = "task " + string(t.Status)
		}
		state := prev
		if op == "create" {
			state = StateError
		}
		if err := s.store.SetVPSState(ctx, v.ID, state, nil, "", truncate(op+" failed: "+msg, 1000)); err != nil {
			return v, err
		}
	}
	return s.store.GetVPS(ctx, v.ID)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Service) vpsInOrg(ctx context.Context, actor *identity.Principal, id string) (*store.VPS, error) {
	v, err := s.store.GetVPS(ctx, id)
	if err != nil {
		return nil, err
	}
	if v.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	v, err = s.reconcile(ctx, v)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, store.ErrNotFound
	}
	return v, nil
}

func (s *Service) GetVPS(ctx context.Context, actor *identity.Principal, id string) (*store.VPS, error) {
	if err := s.requireRead(ctx, actor); err != nil {
		return nil, err
	}
	return s.vpsInOrg(ctx, actor, id)
}

func (s *Service) ListVPS(ctx context.Context, actor *identity.Principal) ([]store.VPS, error) {
	if err := s.requireRead(ctx, actor); err != nil {
		return nil, err
	}
	list, err := s.store.ListVPS(ctx, actor.User.OrganizationID)
	if err != nil {
		return nil, err
	}
	out := make([]store.VPS, 0, len(list))
	for i := range list {
		v, err := s.reconcile(ctx, &list[i])
		if err != nil {
			return nil, err
		}
		if v != nil {
			out = append(out, *v)
		}
	}
	return out, nil
}

// Action starts, shuts down, reboots or force-stops a VPS.
func (s *Service) Action(ctx context.Context, actor *identity.Principal, id, action string) (*store.VPS, error) {
	if err := s.requireWrite(ctx, actor); err != nil {
		return nil, err
	}
	v, err := s.vpsInOrg(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if v.PendingTaskID != nil {
		return nil, &identity.PolicyError{Message: "another operation on this VPS is still running"}
	}
	if v.State != StateRunning && v.State != StateStopped {
		return nil, &identity.PolicyError{Message: "the VPS is " + v.State + "; only delete is possible"}
	}
	n, err := s.store.GetNode(ctx, v.NodeID)
	if err != nil {
		return nil, err
	}
	t, err := s.fleet.QueueTask(ctx, actor, n.ServerID, agenttask.VMAction, mustJSON(agenttask.VMActionParams{ID: v.ID, Action: action}))
	if err != nil {
		return nil, err
	}
	if err := s.store.SetVPSState(ctx, v.ID, v.State, &t.ID, pendingOp(action, v.State), ""); err != nil {
		return nil, err
	}
	s.audit(ctx, actor, "provider.vps_action", "vps", v.ID, map[string]any{"name": v.Name, "action": action, "task_id": t.ID})
	return s.store.GetVPS(ctx, v.ID)
}

// Delete destroys the VPS and its disk (re-auth and the typed name required).
func (s *Service) Delete(ctx context.Context, actor *identity.Principal, id, confirm string) (*store.VPS, error) {
	if err := s.requireWrite(ctx, actor); err != nil {
		return nil, err
	}
	if err := s.id.RequireRecentReauth(actor); err != nil {
		return nil, err
	}
	v, err := s.vpsInOrg(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if confirm != v.Name {
		return nil, verr("confirm", "type the VPS name to confirm that its disk is destroyed")
	}
	if v.PendingTaskID != nil {
		return nil, &identity.PolicyError{Message: "another operation on this VPS is still running"}
	}
	n, err := s.store.GetNode(ctx, v.NodeID)
	if err != nil {
		return nil, err
	}
	t, err := s.fleet.QueueTask(ctx, actor, n.ServerID, agenttask.VMDelete, mustJSON(agenttask.VMRefParams{ID: v.ID}))
	if err != nil {
		return nil, err
	}
	if err := s.store.SetVPSState(ctx, v.ID, StateDeleting, &t.ID, pendingOp("delete", v.State), ""); err != nil {
		return nil, err
	}
	s.audit(ctx, actor, "provider.vps_delete", "vps", v.ID, map[string]any{"name": v.Name, "ip": v.IPAddress, "task_id": t.ID})
	return s.store.GetVPS(ctx, v.ID)
}
