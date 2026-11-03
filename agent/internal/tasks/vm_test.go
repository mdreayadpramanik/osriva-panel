package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

const (
	vmTestID  = "6f1c2a9e-3b4d-4c5e-8f60-718293a4b5c6"
	vmTestKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb6m1lDjp0dmKt8/a2QvYgDphMwHsUeyGnUjjZyyrnD ci"
)

func vmParams() agenttask.VMCreateParams {
	return agenttask.VMCreateParams{ID: vmTestID, Hostname: "web-1", VCPUs: 2, MemoryMB: 1024, DiskGB: 10,
		ImageURL: "https://images.example.com/x.img", ImageSHA256: strings.Repeat("a", 64), Network: "default",
		Address: "192.168.122.100/24", Gateway: "192.168.122.1", DNS: []string{"192.168.122.1"}, User: "ubuntu", SSHKeys: []string{vmTestKey}}
}

func TestDomainXMLAndCloudInit(t *testing.T) {
	p := vmParams()
	x := DomainXML(p, "/v/disk.qcow2", "/v/seed.iso")
	var d struct {
		Type   string `xml:"type,attr"`
		Name   string `xml:"name"`
		Memory int    `xml:"memory"`
		VCPU   int    `xml:"vcpu"`
		Meta   struct {
			VM struct {
				ID string `xml:"id,attr"`
			} `xml:"https://osriva.dev/xmlns/vm/1 vm"`
		} `xml:"metadata"`
		Iface struct {
			Source struct {
				Network string `xml:"network,attr"`
			} `xml:"source"`
			MAC struct {
				Address string `xml:"address,attr"`
			} `xml:"mac"`
		} `xml:"devices>interface"`
	}
	if err := xml.Unmarshal([]byte(x), &d); err != nil {
		t.Fatalf("domain XML does not parse: %v\n%s", err, x)
	}
	if d.Type != "kvm" || d.Name != "osriva-"+vmTestID || d.Memory != 1024 || d.VCPU != 2 || d.Meta.VM.ID != vmTestID ||
		d.Iface.Source.Network != "default" || d.Iface.MAC.Address != VMMAC(vmTestID) {
		t.Fatalf("domain: %+v", d)
	}
	if VMMAC(vmTestID) == VMMAC("6f1c2a9e-3b4d-4c5e-8f60-718293a4b5c7") || !strings.HasPrefix(VMMAC(vmTestID), "52:54:00:") {
		t.Fatal("MACs are per VM and locally administered")
	}

	ud, md, nc := CloudInit(p)
	if !strings.HasPrefix(string(ud), "#cloud-config\n") {
		t.Fatal("user-data must start with #cloud-config")
	}
	var u struct {
		Hostname string `json:"hostname"`
		SSHPw    bool   `json:"ssh_pwauth"`
		Users    []struct {
			Name string   `json:"name"`
			Keys []string `json:"ssh_authorized_keys"`
			Lock bool     `json:"lock_passwd"`
		} `json:"users"`
	}
	if err := json.Unmarshal(ud[len("#cloud-config\n"):], &u); err != nil || u.Hostname != "web-1" || u.SSHPw || len(u.Users) != 1 ||
		u.Users[0].Name != "ubuntu" || !u.Users[0].Lock || u.Users[0].Keys[0] != vmTestKey {
		t.Fatalf("user-data: %s %v", ud, err)
	}
	if !strings.Contains(string(md), `"instance-id":"`+vmTestID+`"`) {
		t.Fatalf("meta-data: %s", md)
	}
	if !strings.Contains(string(nc), `"addresses":["192.168.122.100/24"]`) || !strings.Contains(string(nc), `"via":"192.168.122.1"`) ||
		!strings.Contains(string(nc), VMMAC(vmTestID)) {
		t.Fatalf("network-config: %s", nc)
	}
}

func TestNormalizeState(t *testing.T) {
	for in, want := range map[string]string{"running\n": "running", "shut off": "stopped", "paused": "paused", "in shutdown": "running", "blocked": "other"} {
		if got := normalizeState(in); got != want {
			t.Errorf("%q: %s", in, got)
		}
	}
}

// fakeVirsh records commands; domains maps names to {state, metadata}.
type fakeVirsh struct {
	cmds    []string
	domains map[string][2]string
}

func (f *fakeVirsh) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
	if name != "virsh" {
		return nil, nil
	}
	if len(args) >= 2 && args[0] == "-c" {
		args = args[2:]
	}
	switch args[0] {
	case "--version":
		return []byte("10.0.0\n"), nil
	case "net-list":
		return []byte("default\n"), nil
	case "dominfo":
		if _, ok := f.domains[args[1]]; ok {
			return []byte("Name: x"), nil
		}
		return nil, errors.New("failed to get domain")
	case "metadata":
		d, ok := f.domains[args[1]]
		if !ok || d[1] == "" {
			return nil, errors.New("metadata not found")
		}
		return []byte(d[1]), nil
	case "domstate":
		return []byte(f.domains[args[1]][0]), nil
	case "destroy":
		d := f.domains[args[1]]
		f.domains[args[1]] = [2]string{"shut off", d[1]}
	case "start":
		d := f.domains[args[1]]
		f.domains[args[1]] = [2]string{"running", d[1]}
	case "undefine":
		delete(f.domains, args[1])
	case "list":
		var names []string
		for n := range f.domains {
			names = append(names, n)
		}
		return []byte(strings.Join(names, "\n")), nil
	}
	return nil, nil
}

func withFakeHost(t *testing.T, f *fakeVirsh) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("libvirt tasks run on Linux only")
	}
	oldRun, oldLook, oldRoot, oldKVM := vmRun, vmLookPath, vmRoot, vmKVMPath
	kvm := filepath.Join(t.TempDir(), "kvm")
	_ = os.WriteFile(kvm, nil, 0o600)
	vmRun, vmLookPath, vmRoot, vmKVMPath = f.run, func(n string) (string, error) { return "/usr/bin/" + n, nil }, t.TempDir(), kvm
	t.Cleanup(func() { vmRun, vmLookPath, vmRoot, vmKVMPath = oldRun, oldLook, oldRoot, oldKVM })
}

func TestVMActionsOnlyTouchOwnedDomains(t *testing.T) {
	owned := `<osriva:vm xmlns:osriva="https://osriva.dev/xmlns/vm/1" id="` + vmTestID + `"/>`
	other := "a1b2c3d4-0000-4000-8000-000000000001"
	f := &fakeVirsh{domains: map[string][2]string{
		"osriva-" + vmTestID: {"running", owned},
		"osriva-" + other:    {"running", ""}, // same naming scheme, but not created by OSRIVA
		"customer-db":        {"running", ""},
	}}
	withFakeHost(t, f)
	ctx := context.Background()

	if _, err := vmAction(ctx, other, agenttask.VMStop); err == nil || !strings.Contains(err.Error(), "did not create") {
		t.Fatalf("stop of a foreign domain: %v", err)
	}
	if _, err := vmDelete(ctx, other); err == nil {
		t.Fatal("delete of a foreign domain must be refused")
	}
	res, err := vmAction(ctx, vmTestID, agenttask.VMStop)
	if err != nil || res.(agenttask.VMResult).State != "stopped" {
		t.Fatalf("stop: %+v %v", res, err)
	}
	list, err := vmList(ctx)
	if err != nil || len(list.(agenttask.VMListResult).VMs) != 1 || list.(agenttask.VMListResult).VMs[0].ID != vmTestID {
		t.Fatalf("list shows only owned domains: %+v %v", list, err)
	}
	disk, seed, def := vmPaths(vmTestID)
	for _, p := range []string{disk, seed, def} {
		_ = os.WriteFile(p, []byte("x"), 0o600)
	}
	if _, err := vmDelete(ctx, vmTestID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{disk, seed, def} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left behind", p)
		}
	}
	if _, ok := f.domains["customer-db"]; !ok {
		t.Fatal("unrelated domains are untouched")
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "customer-db") || strings.Contains(c, "net-define") || strings.Contains(c, "net-destroy") {
			t.Fatalf("unexpected command: %s", c)
		}
	}
	if _, err := vmCreate(ctx, func() agenttask.VMCreateParams { p := vmParams(); p.Network = "missing"; return p }()); err == nil ||
		!strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unknown network: %v", err)
	}
}

func TestFetchImageVerifiesChecksum(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("libvirt tasks run on Linux only")
	}
	body := []byte("fake disk image")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	oldHTTP, oldRoot := vmHTTP, vmRoot
	vmHTTP, vmRoot = srv.Client(), t.TempDir()
	defer func() { vmHTTP, vmRoot = oldHTTP, oldRoot }()
	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])
	if _, err := fetchImage(context.Background(), srv.URL+"/x.img", strings.Repeat("b", 64)); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("wrong checksum accepted: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(vmRoot, "base")); len(entries) != 0 {
		t.Fatalf("a rejected download must not stay: %v", entries)
	}
	p, err := fetchImage(context.Background(), srv.URL+"/x.img", good)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(body) {
		t.Fatal("cached image content")
	}
	// A damaged cache entry is downloaded again.
	_ = os.WriteFile(p, []byte("tampered"), 0o644)
	if p2, err := fetchImage(context.Background(), srv.URL+"/x.img", good); err != nil || p2 != p {
		t.Fatalf("re-download: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(body) {
		t.Fatal("tampered cache entry was used")
	}
}
