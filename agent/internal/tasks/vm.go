package tasks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

// libvirt/KVM virtual machines (docs/adr/0013-provider-mode-libvirt.md). The
// agent drives virsh, qemu-img and an ISO tool with explicit arguments, installs
// nothing, never changes libvirt networks, and only acts on domains carrying
// the OSRIVA metadata element.

const (
	libvirtURI    = "qemu:///system"
	vmMetadataNS  = "https://osriva.dev/xmlns/vm/1"
	maxImageBytes = 20 << 30
)

var (
	// vmRoot is libvirt's own image location, readable by QEMU under the default
	// AppArmor profile and dynamic ownership.
	vmRoot = "/var/lib/libvirt/images/osriva"
	// vmRun runs a tool (replaced in tests).
	vmRun = run
	// vmLookPath finds a tool (replaced in tests).
	vmLookPath = exec.LookPath
	// vmHTTP downloads images.
	vmHTTP = &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) > 5 {
			return errors.New("image redirects must stay on https")
		}
		return nil
	}}
	vmKVMPath = "/dev/kvm"
)

func vmTaskRun(ctx context.Context, typ string, params json.RawMessage) (any, error) {
	switch typ {
	case agenttask.VMHost:
		return vmHost(ctx), nil
	case agenttask.VMList:
		return vmList(ctx)
	case agenttask.VMCreate:
		var p agenttask.VMCreateParams
		_ = json.Unmarshal(params, &p)
		return vmCreate(ctx, p)
	case agenttask.VMAction:
		var p agenttask.VMActionParams
		_ = json.Unmarshal(params, &p)
		return vmAction(ctx, p.ID, p.Action)
	case agenttask.VMDelete:
		var p agenttask.VMRefParams
		_ = json.Unmarshal(params, &p)
		return vmDelete(ctx, p.ID)
	}
	return nil, fmt.Errorf("%w: %s", agenttask.ErrInvalid, typ)
}

func virsh(ctx context.Context, args ...string) ([]byte, error) {
	return vmRun(ctx, "virsh", append([]string{"-c", libvirtURI}, args...)...)
}

// isoTool returns the first available ISO 9660 writer.
func isoTool() string {
	for _, t := range []string{"genisoimage", "mkisofs", "xorriso"} {
		if _, err := vmLookPath(t); err == nil {
			return t
		}
	}
	return ""
}

func vmHost(ctx context.Context) agenttask.VMHostResult {
	r := agenttask.VMHostResult{Networks: []agenttask.VMNetworkInfo{}, CPUs: runtime.NumCPU(), MemoryBytes: hostMemory()}
	if runtime.GOOS != "linux" {
		r.Reason = "virtual machines are supported on Linux hosts with libvirt/KVM only"
		return r
	}
	_, err := os.Stat(vmKVMPath)
	r.KVM = err == nil
	r.ISOTool = isoTool()
	var missing []string
	for _, t := range []string{"virsh", "qemu-img"} {
		if _, err := vmLookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	if r.ISOTool == "" {
		missing = append(missing, "genisoimage (or mkisofs / xorriso)")
	}
	if len(missing) > 0 {
		r.Reason = "missing tools: " + strings.Join(missing, ", ") + " (install libvirt, qemu-kvm and genisoimage on this host)"
		return r
	}
	if !r.KVM {
		r.Reason = vmKVMPath + " is not available: hardware virtualization is off or not exposed to this host"
		return r
	}
	if out, err := vmRun(ctx, "virsh", "--version"); err == nil {
		r.LibvirtVersion = strings.TrimSpace(string(out))
	}
	all, err := virsh(ctx, "net-list", "--all", "--name")
	if err != nil {
		r.Reason = "cannot reach libvirtd: " + err.Error()
		return r
	}
	active, _ := virsh(ctx, "net-list", "--name")
	act := map[string]bool{}
	for _, n := range lines(active) {
		act[n] = true
	}
	for _, n := range lines(all) {
		r.Networks = append(r.Networks, agenttask.VMNetworkInfo{Name: n, Active: act[n]})
	}
	r.FreeDiskBytes = freeBytes(filepath.Dir(vmRoot))
	r.Available = true
	return r
}

func lines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func hostMemory() uint64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		var kb uint64
		if _, err := fmt.Sscanf(l, "MemTotal: %d kB", &kb); err == nil {
			return kb * 1024
		}
	}
	return 0
}

func requireHost(ctx context.Context) error {
	h := vmHost(ctx)
	if !h.Available {
		return fmt.Errorf("%w: %s", ErrUnsupported, h.Reason)
	}
	return nil
}

// normalizeState maps virsh domstate output to running | stopped | paused | other.
func normalizeState(s string) string {
	switch strings.TrimSpace(s) {
	case "running", "idle", "in shutdown":
		return "running"
	case "shut off", "crashed":
		return "stopped"
	case "paused", "pmsuspended":
		return "paused"
	}
	return "other"
}

func domState(ctx context.Context, name string) (string, error) {
	out, err := virsh(ctx, "domstate", name)
	if err != nil {
		return "", err
	}
	return normalizeState(string(out)), nil
}

// ownedDomain reports whether the domain exists and whether OSRIVA created it.
func ownedDomain(ctx context.Context, id string) (exists, owned bool, err error) {
	name := agenttask.VMDomainName(id)
	if _, err := virsh(ctx, "dominfo", name); err != nil {
		return false, false, nil
	}
	out, err := virsh(ctx, "metadata", name, vmMetadataNS)
	if err != nil {
		return true, false, nil
	}
	s := string(out)
	return true, strings.Contains(s, `id="`+id+`"`) || strings.Contains(s, `id='`+id+`'`), nil
}

func vmList(ctx context.Context) (any, error) {
	if err := requireHost(ctx); err != nil {
		return nil, err
	}
	out, err := virsh(ctx, "list", "--all", "--name")
	if err != nil {
		return nil, err
	}
	res := agenttask.VMListResult{VMs: []agenttask.VMInfo{}}
	for _, n := range lines(out) {
		id, ok := strings.CutPrefix(n, "osriva-")
		if !ok || agenttask.Validate(agenttask.VMDelete, json.RawMessage(`{"id":"`+id+`"}`)) != nil {
			continue
		}
		if _, owned, _ := ownedDomain(ctx, id); !owned {
			continue
		}
		st, err := domState(ctx, n)
		if err != nil {
			continue
		}
		res.VMs = append(res.VMs, agenttask.VMInfo{ID: id, State: st})
	}
	return res, nil
}

// VMMAC derives a stable locally administered MAC (QEMU's 52:54:00 prefix) from the VPS id.
func VMMAC(id string) string {
	h := sha256.Sum256([]byte(id))
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", h[0], h[1], h[2])
}

func vmPaths(id string) (disk, seed, def string) {
	return filepath.Join(vmRoot, id+".qcow2"), filepath.Join(vmRoot, id+"-seed.iso"), filepath.Join(vmRoot, id+".xml")
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// DomainXML is the libvirt definition of a VPS.
func DomainXML(p agenttask.VMCreateParams, disk, seed string) string {
	return fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <metadata><osriva:vm xmlns:osriva='%s' id='%s'/></metadata>
  <memory unit='MiB'>%d</memory>
  <vcpu>%d</vcpu>
  <os><type>hvm</type><boot dev='hd'/></os>
  <features><acpi/><apic/></features>
  <cpu mode='host-passthrough'/>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>destroy</on_crash>
  <devices>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='%s'/><target dev='vda' bus='virtio'/></disk>
    <disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='%s'/><target dev='sda' bus='sata'/><readonly/></disk>
    <interface type='network'><source network='%s'/><mac address='%s'/><model type='virtio'/></interface>
    <serial type='pty'/>
    <console type='pty'/>
    <rng model='virtio'><backend model='random'>/dev/urandom</backend></rng>
  </devices>
</domain>
`, esc(agenttask.VMDomainName(p.ID)), vmMetadataNS, esc(p.ID), p.MemoryMB, p.VCPUs, esc(disk), esc(seed), esc(p.Network), VMMAC(p.ID))
}

// CloudInit returns the NoCloud user-data, meta-data and network-config. They
// are JSON (valid YAML), so no value can add YAML structure.
func CloudInit(p agenttask.VMCreateParams) (userData, metaData, networkConfig []byte) {
	dns := p.DNS
	if dns == nil {
		dns = []string{}
	}
	ud, _ := json.Marshal(map[string]any{
		"hostname": p.Hostname, "manage_etc_hosts": true, "ssh_pwauth": false, "disable_root": true,
		"users": []map[string]any{{"name": p.User, "shell": "/bin/bash", "lock_passwd": true, "sudo": "ALL=(ALL) NOPASSWD:ALL",
			"ssh_authorized_keys": p.SSHKeys}},
	})
	md, _ := json.Marshal(map[string]any{"instance-id": p.ID, "local-hostname": p.Hostname})
	nc, _ := json.Marshal(map[string]any{"version": 2, "ethernets": map[string]any{"eth0": map[string]any{
		"match": map[string]string{"macaddress": VMMAC(p.ID)}, "set-name": "eth0", "addresses": []string{p.Address},
		"routes": []map[string]string{{"to": "default", "via": p.Gateway}}, "nameservers": map[string]any{"addresses": dns},
	}}})
	return append([]byte("#cloud-config\n"), ud...), md, nc
}

// fetchImage returns the cached image with the given SHA-256, downloading and
// verifying it first when needed.
func fetchImage(ctx context.Context, rawURL, sum string) (string, error) {
	dir := filepath.Join(vmRoot, "base")
	if err := mkdirTraversable(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, sum+".img")
	if f, err := os.Open(path); err == nil {
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err == nil && hex.EncodeToString(h.Sum(nil)) == sum {
			return path, nil
		}
		_ = os.Remove(path) // damaged cache entry
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "OSRIVA-Agent")
	resp, err := vmHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download image: HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxImageBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("download image: %w", err)
	}
	if n > maxImageBytes {
		return "", errors.New("image is larger than 20 GiB")
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return "", fmt.Errorf("image checksum mismatch: expected %s, got %s", sum, got)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}

type imageInfo struct {
	Format      string `json:"format"`
	VirtualSize int64  `json:"virtual-size"`
}

func vmCreate(ctx context.Context, p agenttask.VMCreateParams) (any, error) {
	if err := requireHost(ctx); err != nil {
		return nil, err
	}
	var logs strings.Builder
	step := func(s string) { fmt.Fprintf(&logs, "%s %s\n", time.Now().UTC().Format("15:04:05"), s) }
	name := agenttask.VMDomainName(p.ID)
	if exists, _, _ := ownedDomain(ctx, p.ID); exists {
		return nil, fmt.Errorf("a domain named %s already exists", name)
	}
	nets, err := virsh(ctx, "net-list", "--name")
	if err != nil {
		return nil, err
	}
	if !contains(lines(nets), p.Network) {
		return nil, fmt.Errorf("libvirt network %q does not exist or is not active on this host", p.Network)
	}
	if err := mkdirTraversable(vmRoot); err != nil {
		return nil, err
	}
	step("fetching image " + p.ImageSHA256[:12])
	base, err := fetchImage(ctx, p.ImageURL, p.ImageSHA256)
	if err != nil {
		return nil, err
	}
	out, err := vmRun(ctx, "qemu-img", "info", "--output=json", base)
	if err != nil {
		return nil, err
	}
	var info imageInfo
	if err := json.Unmarshal(out, &info); err != nil || (info.Format != "qcow2" && info.Format != "raw") {
		return nil, fmt.Errorf("the image is not a qcow2 or raw disk image (format %q)", info.Format)
	}
	if want := int64(p.DiskGB) << 30; want < info.VirtualSize {
		return nil, fmt.Errorf("disk must be at least %d GB for this image", (info.VirtualSize+(1<<30)-1)>>30)
	}
	if free := freeBytes(vmRoot); free > 0 && free < uint64(p.DiskGB)<<30/4 {
		return nil, fmt.Errorf("not enough free space in %s", vmRoot)
	}
	disk, seed, def := vmPaths(p.ID)
	cleanup := func() {
		_, _ = virsh(context.Background(), "undefine", name)
		for _, f := range []string{disk, seed, def} {
			_ = os.Remove(f)
		}
	}
	step("creating disk")
	if _, err := vmRun(ctx, "qemu-img", "create", "-q", "-f", "qcow2", "-F", info.Format, "-b", base, disk, fmt.Sprintf("%dG", p.DiskGB)); err != nil {
		cleanup()
		return nil, err
	}
	_ = os.Chmod(disk, 0o600)
	step("writing cloud-init seed")
	if err := writeSeed(ctx, p, seed); err != nil {
		cleanup()
		return nil, err
	}
	if err := os.WriteFile(def, []byte(DomainXML(p, disk, seed)), 0o600); err != nil {
		cleanup()
		return nil, err
	}
	step("defining and starting " + name)
	if _, err := virsh(ctx, "define", def); err != nil {
		cleanup()
		return nil, err
	}
	if _, err := virsh(ctx, "start", name); err != nil {
		cleanup()
		return nil, err
	}
	st, _ := domState(ctx, name)
	step("state " + st)
	return agenttask.VMResult{ID: p.ID, State: st, MAC: VMMAC(p.ID), Logs: logs.String()}, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func writeSeed(ctx context.Context, p agenttask.VMCreateParams, seed string) error {
	dir, err := os.MkdirTemp(vmRoot, ".seed-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	ud, md, nc := CloudInit(p)
	files := map[string][]byte{"user-data": ud, "meta-data": md, "network-config": nc}
	var paths []string
	for _, n := range []string{"user-data", "meta-data", "network-config"} {
		f := filepath.Join(dir, n)
		if err := os.WriteFile(f, files[n], 0o644); err != nil {
			return err
		}
		paths = append(paths, f)
	}
	tool := isoTool()
	args := []string{"-quiet", "-output", seed, "-volid", "cidata", "-joliet", "-rock"}
	if tool == "xorriso" {
		args = append([]string{"-as", "mkisofs"}, args...)
	}
	if _, err := vmRun(ctx, tool, append(args, paths...)...); err != nil {
		return err
	}
	return os.Chmod(seed, 0o644)
}

func vmAction(ctx context.Context, id, action string) (any, error) {
	if err := requireHost(ctx); err != nil {
		return nil, err
	}
	exists, owned, _ := ownedDomain(ctx, id)
	if !exists {
		return nil, errors.New("the virtual machine does not exist on this node")
	}
	if !owned {
		return nil, errors.New("refusing to touch a domain that OSRIVA did not create")
	}
	name := agenttask.VMDomainName(id)
	cmd := map[string]string{agenttask.VMStart: "start", agenttask.VMShutdown: "shutdown", agenttask.VMReboot: "reboot", agenttask.VMStop: "destroy"}[action]
	if _, err := virsh(ctx, cmd, name); err != nil {
		return nil, err
	}
	st, err := domState(ctx, name)
	if action == agenttask.VMShutdown {
		// A graceful shutdown takes the guest a while; wait for it (up to 2 minutes).
		for i := 0; i < 60 && err == nil && st == "running"; i++ {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
			}
			st, err = domState(ctx, name)
		}
	}
	if err != nil {
		return nil, err
	}
	res := agenttask.VMResult{ID: id, State: st}
	if action == agenttask.VMShutdown && st == "running" {
		res.Logs = "the guest did not power off within 2 minutes; use stop to force it off"
	}
	return res, nil
}

func vmDelete(ctx context.Context, id string) (any, error) {
	if err := requireHost(ctx); err != nil {
		return nil, err
	}
	exists, owned, _ := ownedDomain(ctx, id)
	if exists && !owned {
		return nil, errors.New("refusing to delete a domain that OSRIVA did not create")
	}
	name := agenttask.VMDomainName(id)
	if exists {
		if st, _ := domState(ctx, name); st != "stopped" {
			if _, err := virsh(ctx, "destroy", name); err != nil {
				return nil, err
			}
		}
		if _, err := virsh(ctx, "undefine", name); err != nil {
			return nil, err
		}
	}
	disk, seed, def := vmPaths(id)
	for _, f := range []string{disk, seed, def} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return agenttask.VMResult{ID: id, State: "deleted", Deleted: true}, nil
}

// mkdirTraversable creates dir with mode 0711 regardless of the agent's umask
// (0077 under systemd): QEMU runs as its own user and must traverse it, while
// listing stays root-only.
func mkdirTraversable(dir string) error {
	if err := os.MkdirAll(dir, 0o711); err != nil {
		return err
	}
	return os.Chmod(dir, 0o711)
}
