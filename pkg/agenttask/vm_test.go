package agenttask

import (
	"encoding/json"
	"errors"
	"testing"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb6m1lDjp0dmKt8/a2QvYgDphMwHsUeyGnUjjZyyrnD ci"

func vmCreate(mut func(p *VMCreateParams)) json.RawMessage {
	p := VMCreateParams{ID: "6f1c2a9e-3b4d-4c5e-8f60-718293a4b5c6", Hostname: "web-1", VCPUs: 1, MemoryMB: 1024, DiskGB: 10,
		ImageURL:    "https://cloud-images.ubuntu.com/minimal/releases/noble/release/ubuntu-24.04-minimal-cloudimg-amd64.img",
		ImageSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Network: "default",
		Address: "192.168.122.100/24", Gateway: "192.168.122.1", DNS: []string{"192.168.122.1"}, User: "ubuntu", SSHKeys: []string{testKey}}
	if mut != nil {
		mut(&p)
	}
	b, _ := json.Marshal(p)
	return b
}

func TestValidateVM(t *testing.T) {
	if err := Validate(VMCreate, vmCreate(nil)); err != nil {
		t.Fatalf("valid create: %v", err)
	}
	bad := map[string]func(p *VMCreateParams){
		"hostname":      func(p *VMCreateParams) { p.Hostname = "Web_1" },
		"vcpus":         func(p *VMCreateParams) { p.VCPUs = 0 },
		"memory":        func(p *VMCreateParams) { p.MemoryMB = 64 },
		"http image":    func(p *VMCreateParams) { p.ImageURL = "http://example.com/x.img" },
		"creds in url":  func(p *VMCreateParams) { p.ImageURL = "https://u:p@example.com/x.img" },
		"sha":           func(p *VMCreateParams) { p.ImageSHA256 = "ABC" },
		"network":       func(p *VMCreateParams) { p.Network = "br0;reboot" },
		"ipv6":          func(p *VMCreateParams) { p.Address = "fd00::5/64" },
		"no prefix":     func(p *VMCreateParams) { p.Address = "192.168.122.100" },
		"gateway out":   func(p *VMCreateParams) { p.Gateway = "10.0.0.1" },
		"gateway is ip": func(p *VMCreateParams) { p.Gateway = "192.168.122.100" },
		"dns":           func(p *VMCreateParams) { p.DNS = []string{"dns.example.com"} },
		"user":          func(p *VMCreateParams) { p.User = "root;x" },
		"no keys":       func(p *VMCreateParams) { p.SSHKeys = nil },
		"key options":   func(p *VMCreateParams) { p.SSHKeys = []string{`command="id" ` + testKey} },
		"id":            func(p *VMCreateParams) { p.ID = "../../etc" },
	}
	for name, mut := range bad {
		if err := Validate(VMCreate, vmCreate(mut)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	if err := Validate(VMAction, json.RawMessage(`{"id":"6f1c2a9e-3b4d-4c5e-8f60-718293a4b5c6","action":"migrate"}`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown action: %v", err)
	}
	if !NeedsReauth(VMDelete, nil) || !NeedsReauth(VMAction, json.RawMessage(`{"action":"stop"}`)) || NeedsReauth(VMAction, json.RawMessage(`{"action":"reboot"}`)) {
		t.Error("re-auth for delete and force stop only")
	}
	if Class(VMCreate) != ClassOperate || Class(VMHost) != ClassRead || !ReadOnly(VMList) || ReadOnly(VMDelete) || !LongRunning(VMCreate) || Bulk(VMCreate) {
		t.Error("vm task metadata")
	}
}
