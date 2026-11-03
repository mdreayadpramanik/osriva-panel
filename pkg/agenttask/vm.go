package agenttask

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

// Virtual machines on a libvirt/KVM node (docs/adr/0013-provider-mode-libvirt.md).
// The agent only acts on domains it created: named osriva-<id> and carrying the
// OSRIVA metadata element.

const (
	VMHost   = "vm.host"
	VMList   = "vm.list"
	VMCreate = "vm.create"
	VMAction = "vm.action"
	VMDelete = "vm.delete"

	VMStart    = "start"
	VMShutdown = "shutdown" // graceful (ACPI)
	VMReboot   = "reboot"
	VMStop     = "stop" // force off

	MaxVMSSHKeys = 10
)

func vmTask(typ string) bool { return strings.HasPrefix(typ, "vm.") }

func vmReadOnly(typ string) bool { return typ == VMHost || typ == VMList }

var (
	vmID        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	vmHostname  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	vmNetwork   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	vmUser      = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	vmActionSet = map[string]bool{VMStart: true, VMShutdown: true, VMReboot: true, VMStop: true}
)

// ValidVMHostname reports whether s can be a VPS name (an RFC 1123 label).
func ValidVMHostname(s string) bool { return vmHostname.MatchString(s) }

// ValidVMNetwork reports whether s can name a libvirt network.
func ValidVMNetwork(s string) bool { return vmNetwork.MatchString(s) }

type VMRefParams struct {
	ID string `json:"id"`
}

type VMActionParams struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

type VMCreateParams struct {
	ID          string   `json:"id"`
	Hostname    string   `json:"hostname"`
	VCPUs       int      `json:"vcpus"`
	MemoryMB    int      `json:"memory_mb"`
	DiskGB      int      `json:"disk_gb"`
	ImageURL    string   `json:"image_url"`
	ImageSHA256 string   `json:"image_sha256"`
	Network     string   `json:"network"`
	Address     string   `json:"address"` // CIDR form, e.g. 192.168.122.100/24
	Gateway     string   `json:"gateway"`
	DNS         []string `json:"dns,omitempty"`
	User        string   `json:"user"` // the image's default user, which receives the SSH keys
	SSHKeys     []string `json:"ssh_keys"`
}

// ValidImageURL accepts https:// URLs without credentials.
func ValidImageURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && len(raw) <= 2000
}

func validateVM(typ string, raw json.RawMessage) error {
	switch typ {
	case VMHost, VMList:
		var empty struct{}
		return decodeStrict(raw, &empty)
	case VMDelete:
		var p VMRefParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !vmID.MatchString(p.ID) {
			return fmt.Errorf("%w: invalid VM id", ErrInvalid)
		}
	case VMAction:
		var p VMActionParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		if !vmID.MatchString(p.ID) {
			return fmt.Errorf("%w: invalid VM id", ErrInvalid)
		}
		if !vmActionSet[p.Action] {
			return fmt.Errorf("%w: action must be start, shutdown, reboot or stop", ErrInvalid)
		}
	case VMCreate:
		var p VMCreateParams
		if err := decodeStrict(raw, &p); err != nil {
			return err
		}
		return p.validate()
	}
	return nil
}

func (p *VMCreateParams) validate() error {
	bad := func(msg string) error { return fmt.Errorf("%w: %s", ErrInvalid, msg) }
	if !vmID.MatchString(p.ID) {
		return bad("invalid VM id")
	}
	if !vmHostname.MatchString(p.Hostname) {
		return bad("hostname must be 1-63 lowercase letters, digits or dashes")
	}
	if p.VCPUs < 1 || p.VCPUs > 64 {
		return bad("vcpus must be 1-64")
	}
	if p.MemoryMB < 256 || p.MemoryMB > 262144 {
		return bad("memory_mb must be 256-262144")
	}
	if p.DiskGB < 1 || p.DiskGB > 4096 {
		return bad("disk_gb must be 1-4096")
	}
	if !ValidImageURL(p.ImageURL) {
		return bad("image_url must be an https:// URL")
	}
	if !sha256Hex.MatchString(p.ImageSHA256) {
		return bad("image_sha256 must be 64 lowercase hex characters")
	}
	if !vmNetwork.MatchString(p.Network) {
		return bad("invalid libvirt network name")
	}
	pfx, err := netip.ParsePrefix(p.Address)
	if err != nil || !pfx.Addr().Is4() || pfx.Bits() < 8 || pfx.Bits() > 30 {
		return bad("address must be an IPv4 address with a /8-/30 prefix")
	}
	gw, err := netip.ParseAddr(p.Gateway)
	if err != nil || !gw.Is4() || !pfx.Masked().Contains(gw) || gw == pfx.Addr() {
		return bad("gateway must be another IPv4 address in the same network")
	}
	if len(p.DNS) > 3 {
		return bad("at most 3 DNS servers")
	}
	for _, d := range p.DNS {
		if a, err := netip.ParseAddr(d); err != nil || !a.Is4() {
			return bad("DNS servers must be IPv4 addresses")
		}
	}
	if !vmUser.MatchString(p.User) {
		return bad("invalid user name")
	}
	if len(p.SSHKeys) == 0 || len(p.SSHKeys) > MaxVMSSHKeys {
		return bad(fmt.Sprintf("1-%d SSH public keys are required (password login is disabled)", MaxVMSSHKeys))
	}
	for _, k := range p.SSHKeys {
		if _, _, _, err := ParsePublicKey(k); err != nil {
			return bad("invalid SSH public key: " + err.Error())
		}
	}
	return nil
}

// VMDomainName is the libvirt name of the domain for a VPS id.
func VMDomainName(id string) string { return "osriva-" + id }

type VMNetworkInfo struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

type VMHostResult struct {
	Available      bool            `json:"available"`
	Reason         string          `json:"reason,omitempty"`
	KVM            bool            `json:"kvm"`
	LibvirtVersion string          `json:"libvirt_version,omitempty"`
	ISOTool        string          `json:"iso_tool,omitempty"`
	Networks       []VMNetworkInfo `json:"networks"`
	FreeDiskBytes  uint64          `json:"free_disk_bytes"`
	CPUs           int             `json:"cpus"`
	MemoryBytes    uint64          `json:"memory_bytes"`
}

type VMInfo struct {
	ID    string `json:"id"`
	State string `json:"state"` // running | stopped | paused | other
}

type VMListResult struct {
	VMs []VMInfo `json:"vms"`
}

// VMResult is returned by vm.create, vm.action and vm.delete.
type VMResult struct {
	ID      string `json:"id"`
	State   string `json:"state"` // running | stopped | deleted | …
	MAC     string `json:"mac,omitempty"`
	Logs    string `json:"logs,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
}
