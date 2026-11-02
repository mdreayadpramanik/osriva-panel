// Package inventory describes and collects the facts an OSRIVA Agent reports
// about its host. Collection uses native OS interfaces only (procfs/sysfs on
// Linux, Win32 APIs on Windows, sysctl on BSD); it never runs shell scripts.
// Values that cannot be determined are left empty and listed in Warnings.
package inventory

import (
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"time"
)

// SchemaVersion is bumped whenever a field changes meaning.
const SchemaVersion = 1

type Inventory struct {
	SchemaVersion  int       `json:"schema_version"`
	CollectedAt    time.Time `json:"collected_at"`
	Hostname       string    `json:"hostname"`
	OS             OSInfo    `json:"os"`
	CPU            CPUInfo   `json:"cpu"`
	Memory         Memory    `json:"memory"`
	Disks          []Disk    `json:"disks"`
	Network        []NIC     `json:"network"`
	UptimeSeconds  uint64    `json:"uptime_seconds"`
	Privileged     bool      `json:"privileged"` // root / elevated Administrator
	InitSystem     string    `json:"init_system"`
	PackageManager []string  `json:"package_managers"`
	Tools          Tools     `json:"tools"`
	Virtualization string    `json:"virtualization,omitempty"` // e.g. kvm, container; empty if bare metal or unknown
	Warnings       []string  `json:"warnings,omitempty"`
}

type OSInfo struct {
	Family     string `json:"family"` // runtime.GOOS: linux, windows, freebsd, openbsd, netbsd, illumos, darwin
	ID         string `json:"id"`     // e.g. ubuntu, debian, rhel, windows, freebsd
	Name       string `json:"name"`   // human readable, e.g. "Ubuntu 24.04.1 LTS"
	Version    string `json:"version"`
	IDLike     string `json:"id_like,omitempty"`
	Kernel     string `json:"kernel"`
	Arch       string `json:"arch"` // runtime.GOARCH
	Immutable  bool   `json:"immutable,omitempty"`
	BuildExtra string `json:"build,omitempty"`
}

type CPUInfo struct {
	Model        string `json:"model"`
	LogicalCores int    `json:"logical_cores"`
}

type Memory struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

type Disk struct {
	Mount      string `json:"mount"`
	FSType     string `json:"fs_type"`
	Device     string `json:"device,omitempty"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
}

type NIC struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac,omitempty"`
	Addresses []string `json:"addresses"`
	Up        bool     `json:"up"`
}

type Tools struct {
	Docker  bool `json:"docker"`
	Podman  bool `json:"podman"`
	Git     bool `json:"git"`
	Systemd bool `json:"systemd"`
}

// Collect gathers the inventory of the current host.
func Collect() *Inventory {
	inv := &Inventory{SchemaVersion: SchemaVersion, CollectedAt: time.Now().UTC()}
	inv.OS.Family = runtime.GOOS
	inv.OS.Arch = runtime.GOARCH
	inv.CPU.LogicalCores = runtime.NumCPU()
	if h, err := os.Hostname(); err == nil {
		inv.Hostname = h
	} else {
		inv.warn("hostname: " + err.Error())
	}
	inv.Network = collectNICs(inv)
	collectPlatform(inv) // OS-specific (inventory_<goos>.go)
	inv.Tools.Docker = onPath("docker")
	inv.Tools.Podman = onPath("podman")
	inv.Tools.Git = onPath("git")
	sort.Slice(inv.Disks, func(i, j int) bool { return inv.Disks[i].Mount < inv.Disks[j].Mount })
	return inv
}

func (inv *Inventory) warn(msg string) { inv.Warnings = append(inv.Warnings, msg) }

func onPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func collectNICs(inv *Inventory) []NIC {
	ifs, err := net.Interfaces()
	if err != nil {
		inv.warn("network interfaces: " + err.Error())
		return nil
	}
	out := []NIC{}
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			continue
		}
		n := NIC{Name: i.Name, MAC: i.HardwareAddr.String(), Up: i.Flags&net.FlagUp != 0, Addresses: []string{}}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLinkLocalUnicast() {
				n.Addresses = append(n.Addresses, ipn.String())
			}
		}
		if len(n.Addresses) > 0 || n.Up {
			out = append(out, n)
		}
	}
	return out
}
