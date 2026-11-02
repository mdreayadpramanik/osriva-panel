//go:build windows

package inventory

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64       = kernel32.NewProc("GetTickCount64")
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func regString(k registry.Key, name string) string {
	v, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return v
}

func collectPlatform(inv *Inventory) {
	inv.OS.ID = "windows"
	v := windows.RtlGetVersion()
	inv.OS.Kernel = fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	inv.OS.Version = inv.OS.Kernel
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		name := regString(k, "ProductName")
		// Windows 11 still reports "Windows 10" in ProductName; build >= 22000 is Windows 11.
		if v.BuildNumber >= 22000 && len(name) >= 10 && name[:10] == "Windows 10" {
			name = "Windows 11" + name[10:]
		}
		if dv := regString(k, "DisplayVersion"); dv != "" {
			name += " " + dv
		}
		inv.OS.Name = name
		inv.OS.BuildExtra = regString(k, "EditionID")
		k.Close()
	} else {
		inv.warn("registry CurrentVersion: " + err.Error())
	}

	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		inv.CPU.Model = strings.TrimSpace(regString(k, "ProcessorNameString"))
		k.Close()
	}

	ms := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		inv.Memory.TotalBytes, inv.Memory.AvailableBytes = ms.TotalPhys, ms.AvailPhys
	} else {
		inv.warn("GlobalMemoryStatusEx: " + err.Error())
	}
	if r, _, _ := procGetTickCount64.Call(); r != 0 {
		inv.UptimeSeconds = uint64(r) / 1000
	}

	inv.Privileged = windows.GetCurrentProcessToken().IsElevated()
	inv.InitSystem = "windows-scm"

	drives, err := windows.GetLogicalDrives()
	if err != nil {
		inv.warn("GetLogicalDrives: " + err.Error())
	}
	for i := 0; i < 26; i++ {
		if drives&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		rp, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(rp) != windows.DRIVE_FIXED {
			continue
		}
		var free, total, totalFree uint64
		if err := windows.GetDiskFreeSpaceEx(rp, &free, &total, &totalFree); err != nil {
			inv.warn("GetDiskFreeSpaceEx " + root + ": " + err.Error())
			continue
		}
		fs := make([]uint16, windows.MAX_PATH+1)
		_ = windows.GetVolumeInformation(rp, nil, 0, nil, nil, nil, &fs[0], uint32(len(fs)))
		inv.Disks = append(inv.Disks, Disk{Mount: root, FSType: windows.UTF16ToString(fs), TotalBytes: total, FreeBytes: free})
	}

	for _, pm := range []string{"winget", "choco", "scoop"} {
		if onPath(pm) {
			inv.PackageManager = append(inv.PackageManager, pm)
		}
	}
}
