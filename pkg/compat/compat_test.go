package compat

import (
	"testing"

	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
)

func byKey(caps []Capability) map[string]Capability {
	m := map[string]Capability{}
	for _, c := range caps {
		m[c.Key] = c
	}
	return m
}

func linuxHost() *inventory.Inventory {
	return &inventory.Inventory{
		OS:             inventory.OSInfo{Family: "linux", Arch: "amd64", ID: "ubuntu"},
		Privileged:     true,
		InitSystem:     "systemd",
		PackageManager: []string{"apt-get"},
		Tools:          inventory.Tools{Docker: true},
	}
}

func TestOnlyImplementedAndTestedIsSupported(t *testing.T) {
	for _, c := range Evaluate(linuxHost()) {
		if c.Status == Supported && c.Phase != 0 {
			t.Errorf("%s is SUPPORTED but its module is planned for phase %d", c.Key, c.Phase)
		}
		if c.Reason == "" {
			t.Errorf("%s has no reason", c.Key)
		}
	}
}

func TestPlatformStatuses(t *testing.T) {
	win := &inventory.Inventory{OS: inventory.OSInfo{Family: "windows", Arch: "amd64"}, InitSystem: "windows-scm"}
	if s := byKey(Evaluate(win))["agent.inventory"].Status; s != Supported {
		t.Errorf("windows/amd64: %s", s)
	}
	inv := linuxHost()
	if s := byKey(Evaluate(inv))["agent.inventory"].Status; s != Supported {
		t.Errorf("linux/amd64: %s", s)
	}
	inv.OS.Arch = "arm64"
	if s := byKey(Evaluate(inv))["agent.inventory"].Status; s != NotTested {
		t.Errorf("linux/arm64 (never run on a real host): %s", s)
	}
	inv.OS.Family = "darwin"
	if s := byKey(Evaluate(inv))["agent.inventory"].Status; s != Unsupported {
		t.Errorf("darwin: %s", s)
	}
	win.Warnings = []string{"registry: access denied"}
	if c := byKey(Evaluate(win))["agent.inventory"]; c.Status != PartiallySupported {
		t.Errorf("warnings should downgrade to partial: %+v", c)
	}
}

func TestDependencyAndConfigurationStatuses(t *testing.T) {
	inv := linuxHost()
	inv.Privileged = false
	inv.Tools.Docker = false
	inv.PackageManager = nil
	caps := byKey(Evaluate(inv))
	if caps["agent.privileges"].Status != RequiresConfiguration || len(caps["agent.privileges"].Alternatives) == 0 {
		t.Errorf("unprivileged: %+v", caps["agent.privileges"])
	}
	if caps["docker"].Status != DependencyMissing || caps["packages"].Status != DependencyMissing {
		t.Errorf("missing deps: docker=%s packages=%s", caps["docker"].Status, caps["packages"].Status)
	}
	inv.Virtualization = "container"
	inv.Tools.Docker = true
	if s := byKey(Evaluate(inv))["docker"].Status; s != Unsupported {
		t.Errorf("docker inside container: %s", s)
	}
	inv.InitSystem = "runit"
	if s := byKey(Evaluate(inv))["services"].Status; s != Unsupported {
		t.Errorf("runit: %s", s)
	}
}

func TestImmutableOnlyReportedWhenRelevant(t *testing.T) {
	if _, ok := byKey(Evaluate(linuxHost()))["os.immutable"]; ok {
		t.Fatal("immutable capability reported for a mutable OS")
	}
	inv := linuxHost()
	inv.OS.Immutable = true
	if c, ok := byKey(Evaluate(inv))["os.immutable"]; !ok || c.Status != PartiallySupported {
		t.Fatalf("immutable: %+v", c)
	}
}
