//go:build linux

package inventory

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func readFile(inv *Inventory, path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		inv.warn(path + ": " + err.Error())
		return ""
	}
	return string(b)
}

func collectPlatform(inv *Inventory) {
	rel := ""
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		if b, err := os.ReadFile(p); err == nil {
			rel = string(b)
			break
		}
	}
	if rel == "" {
		inv.warn("os-release not found")
	}
	r := ParseOSRelease(rel)
	inv.OS.ID, inv.OS.Version, inv.OS.IDLike = r["ID"], r["VERSION_ID"], r["ID_LIKE"]
	inv.OS.Name = r["PRETTY_NAME"]
	if inv.OS.Name == "" {
		inv.OS.Name = strings.TrimSpace(r["NAME"] + " " + r["VERSION"])
	}
	inv.OS.Immutable = isImmutable(r)

	var u unix.Utsname
	if err := unix.Uname(&u); err == nil {
		inv.OS.Kernel = unix.ByteSliceToString(u.Release[:])
	} else {
		inv.warn("uname: " + err.Error())
	}

	inv.CPU.Model = ParseCPUModel(readFile(inv, "/proc/cpuinfo"))
	inv.Memory.TotalBytes, inv.Memory.AvailableBytes = ParseMeminfo(readFile(inv, "/proc/meminfo"))
	inv.UptimeSeconds = ParseUptime(readFile(inv, "/proc/uptime"))
	inv.Privileged = os.Geteuid() == 0

	for _, m := range ParseMounts(readFile(inv, "/proc/mounts")) {
		var st unix.Statfs_t
		if err := unix.Statfs(m.Point, &st); err != nil {
			inv.warn("statfs " + m.Point + ": " + err.Error())
			continue
		}
		bs := uint64(st.Bsize) //nolint:unconvert // Bsize width differs per arch
		inv.Disks = append(inv.Disks, Disk{Mount: m.Point, FSType: m.FSType, Device: m.Device,
			TotalBytes: st.Blocks * bs, FreeBytes: st.Bavail * bs})
	}

	switch {
	case exists("/run/systemd/system"):
		inv.InitSystem, inv.Tools.Systemd = "systemd", true
	case exists("/sbin/openrc") || exists("/usr/sbin/openrc"):
		inv.InitSystem = "openrc"
	case exists("/etc/runit") || exists("/run/runit"):
		inv.InitSystem = "runit"
	case exists("/etc/init.d"):
		inv.InitSystem = "sysvinit"
	default:
		inv.InitSystem = "unknown"
	}
	for _, pm := range []string{"apt-get", "dnf", "yum", "zypper", "pacman", "apk", "xbps-install", "emerge"} {
		if onPath(pm) {
			inv.PackageManager = append(inv.PackageManager, pm)
		}
	}
	inv.Virtualization = detectVirt()
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func isImmutable(r map[string]string) bool {
	switch r["ID"] {
	case "fedora-coreos", "flatcar", "talos", "bottlerocket", "rhcos":
		return true
	}
	return r["VARIANT_ID"] == "coreos" || r["VARIANT_ID"] == "silverblue" || r["VARIANT_ID"] == "kinoite" ||
		exists("/run/ostree-booted")
}

func detectVirt() string {
	if exists("/.dockerenv") || exists("/run/.containerenv") {
		return "container"
	}
	if b, err := os.ReadFile("/proc/1/environ"); err == nil && strings.Contains(string(b), "container=") {
		return "container"
	}
	if b, err := os.ReadFile("/sys/class/dmi/id/sys_vendor"); err == nil {
		v := strings.ToLower(strings.TrimSpace(string(b)))
		switch {
		case strings.Contains(v, "qemu"), strings.Contains(v, "kvm"):
			return "kvm"
		case strings.Contains(v, "vmware"):
			return "vmware"
		case strings.Contains(v, "microsoft"):
			return "hyper-v"
		case strings.Contains(v, "xen"):
			return "xen"
		case strings.Contains(v, "innotek"), strings.Contains(v, "virtualbox"):
			return "virtualbox"
		case strings.Contains(v, "amazon"):
			return "aws"
		case strings.Contains(v, "google"):
			return "gce"
		}
	}
	return ""
}
