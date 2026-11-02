//go:build freebsd || openbsd || netbsd || dragonfly

package inventory

import (
	"os"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func collectPlatform(inv *Inventory) {
	inv.OS.ID = runtime.GOOS
	if s, err := unix.Sysctl("kern.ostype"); err == nil {
		inv.OS.Name = s
	}
	if s, err := unix.Sysctl("kern.osrelease"); err == nil {
		inv.OS.Version, inv.OS.Kernel = s, s
		inv.OS.Name = strings.TrimSpace(inv.OS.Name + " " + s)
	} else {
		inv.warn("sysctl kern.osrelease: " + err.Error())
	}
	if s, err := unix.Sysctl("hw.model"); err == nil {
		inv.CPU.Model = strings.TrimSpace(s)
	}
	physmem := "hw.physmem"
	if runtime.GOOS == "openbsd" || runtime.GOOS == "netbsd" {
		physmem = "hw.physmem64"
	}
	if n, err := unix.SysctlUint64(physmem); err == nil {
		inv.Memory.TotalBytes = n
	} else {
		inv.warn("sysctl " + physmem + ": " + err.Error())
	}
	if tv, err := unix.SysctlTimeval("kern.boottime"); err == nil {
		boot := time.Unix(tv.Unix())
		inv.UptimeSeconds = uint64(time.Since(boot).Seconds())
	}
	inv.Privileged = os.Geteuid() == 0
	inv.InitSystem = "rc"
	collectBSDDisks(inv)
	for _, pm := range []string{"pkg", "pkg_add", "pkgin"} {
		if onPath(pm) {
			inv.PackageManager = append(inv.PackageManager, pm)
		}
	}
}
