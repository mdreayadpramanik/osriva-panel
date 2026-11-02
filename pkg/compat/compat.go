// Package compat is the OSRIVA compatibility engine. It turns a host inventory
// into capability statuses with reasons and alternatives. It runs on the
// control plane, so rules can improve without upgrading agents.
//
// A capability is SUPPORTED only when OSRIVA implements it AND it has been
// tested on that platform. Host readiness for modules OSRIVA has not built
// yet is reported, but never as SUPPORTED.
package compat

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
)

type Status string

const (
	Supported             Status = "SUPPORTED"
	PartiallySupported    Status = "PARTIALLY_SUPPORTED"
	Unsupported           Status = "UNSUPPORTED"
	NotTested             Status = "NOT_TESTED"
	DependencyMissing     Status = "DEPENDENCY_MISSING"
	RequiresConfiguration Status = "REQUIRES_CONFIGURATION"
)

type Capability struct {
	Key          string   `json:"key"`
	Title        string   `json:"title"`
	Status       Status   `json:"status"`
	Reason       string   `json:"reason"`
	Alternatives []string `json:"alternatives,omitempty"`
	// Phase in which OSRIVA implements the module; 0 = implemented now.
	Phase int `json:"phase"`
}

// testedPlatforms lists GOOS/GOARCH pairs where the agent's inventory has been
// verified on a real host. Update only with evidence (see COMPATIBILITY_MATRIX.md).
var testedPlatforms = map[string]string{
	"windows/amd64": "verified on Windows 11 (2026-11-02)",
	"linux/amd64":   "verified on Ubuntu in CI (2026-11-02)",
}

// targetFamilies are OS families OSRIVA intends to manage.
var targetFamilies = []string{"linux", "windows", "freebsd", "openbsd", "netbsd"}

func Evaluate(inv *inventory.Inventory) []Capability {
	platform := inv.OS.Family + "/" + inv.OS.Arch
	caps := []Capability{agentPlatform(inv, platform), privileges(inv), serviceManager(inv), packageManager(inv), containers(inv), immutability(inv)}
	return slices.DeleteFunc(caps, func(c Capability) bool { return c.Key == "" })
}

func agentPlatform(inv *inventory.Inventory, platform string) Capability {
	c := Capability{Key: "agent.inventory", Title: "Agent and host inventory", Phase: 0}
	switch {
	case testedPlatforms[platform] != "":
		c.Status, c.Reason = Supported, "Inventory collection "+testedPlatforms[platform]+"."
	case slices.Contains(targetFamilies, inv.OS.Family):
		c.Status, c.Reason = NotTested, fmt.Sprintf("The agent builds for %s and has a native collector, but it has not been verified on a real %s host yet.", platform, inv.OS.Family)
	default:
		c.Status, c.Reason = Unsupported, fmt.Sprintf("%s is not a target platform. Only portable facts are collected.", inv.OS.Family)
		c.Alternatives = []string{"Run the agent on a Linux, Windows or BSD host"}
	}
	if len(inv.Warnings) > 0 && c.Status == Supported {
		c.Status = PartiallySupported
		c.Reason += fmt.Sprintf(" %d fact(s) could not be collected: %s", len(inv.Warnings), strings.Join(inv.Warnings, "; "))
	}
	return c
}

func privileges(inv *inventory.Inventory) Capability {
	c := Capability{Key: "agent.privileges", Title: "Privileged operations", Phase: 3}
	if inv.Privileged {
		c.Status, c.Reason = NotTested, "The agent runs with administrative rights, which managing services, packages and files will need. Those operations arrive in Phase 3."
		return c
	}
	admin := "root"
	if inv.OS.Family == "windows" {
		admin = "an elevated Administrator account (or LocalSystem service)"
	}
	c.Status = RequiresConfiguration
	c.Reason = "The agent is running unprivileged. Inventory works, but server management (Phase 3) will need " + admin + "."
	c.Alternatives = []string{"Install the agent as a system service (Phase 2b installer)"}
	return c
}

var knownInit = map[string]string{"systemd": "linux", "openrc": "linux", "windows-scm": "windows", "rc": "bsd"}

func serviceManager(inv *inventory.Inventory) Capability {
	c := Capability{Key: "services", Title: "Service management", Phase: 3}
	if _, ok := knownInit[inv.InitSystem]; ok {
		c.Status, c.Reason = NotTested, fmt.Sprintf("Detected %s, which the Phase 3 service adapter will target. Not implemented yet.", inv.InitSystem)
		return c
	}
	c.Status = Unsupported
	c.Reason = fmt.Sprintf("Service manager %q has no planned OSRIVA adapter.", inv.InitSystem)
	c.Alternatives = []string{"Manage applications as Docker containers instead (Phase 4)"}
	return c
}

var knownPkg = []string{"apt-get", "dnf", "yum", "zypper", "pacman", "apk", "pkg", "winget", "choco"}

func packageManager(inv *inventory.Inventory) Capability {
	c := Capability{Key: "packages", Title: "Package information and installs", Phase: 3}
	for _, pm := range inv.PackageManager {
		if slices.Contains(knownPkg, pm) {
			c.Status, c.Reason = NotTested, "Detected "+strings.Join(inv.PackageManager, ", ")+". The package adapter arrives in Phase 3."
			return c
		}
	}
	if len(inv.PackageManager) == 0 {
		c.Status, c.Reason = DependencyMissing, "No supported package manager was found on PATH."
		if inv.OS.Family == "windows" {
			c.Alternatives = []string{"Install winget (App Installer) from Microsoft"}
		}
		return c
	}
	c.Status, c.Reason = Unsupported, "Detected "+strings.Join(inv.PackageManager, ", ")+", which has no planned adapter."
	return c
}

func containers(inv *inventory.Inventory) Capability {
	c := Capability{Key: "docker", Title: "Docker / containers", Phase: 4}
	switch {
	case inv.Tools.Docker:
		c.Status, c.Reason = NotTested, "Docker CLI found. The Docker manager arrives in Phase 4."
	case inv.Tools.Podman:
		c.Status, c.Reason = PartiallySupported, "Podman found, but Docker is not. The Phase 4 Docker manager will target the Docker Engine API, which Podman only partly emulates."
		c.Alternatives = []string{"Install Docker Engine"}
	default:
		c.Status, c.Reason = DependencyMissing, "Neither Docker nor Podman was found."
		c.Alternatives = []string{"Install Docker Engine (the Phase 4 Docker manager will be able to do this)"}
	}
	if inv.Virtualization == "container" {
		c.Status = Unsupported
		c.Reason = "This host is itself a container. Running containers inside it usually needs privileged nesting."
		c.Alternatives = []string{"Install the agent on the container's host instead"}
	}
	return c
}

func immutability(inv *inventory.Inventory) Capability {
	if !inv.OS.Immutable {
		return Capability{}
	}
	return Capability{
		Key: "os.immutable", Title: "Immutable operating system", Phase: 3, Status: PartiallySupported,
		Reason:       "This is an immutable/image-based OS. Packages and system files cannot be changed conventionally, so package management and in-place configuration will not apply.",
		Alternatives: []string{"Deploy workloads as containers", "Change the OS through its own image/ignition tooling"},
	}
}
