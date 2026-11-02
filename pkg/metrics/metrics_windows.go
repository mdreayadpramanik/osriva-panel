//go:build windows

package metrics

import (
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
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

func ft(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }

func u64(v uint64) *uint64 { return &v }

func readPlatform() raw {
	r := raw{at: time.Now()}

	// Kernel time includes idle time on Windows.
	var idle, kernel, user windows.Filetime
	if ok, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok != 0 {
		r.cpuTotal, r.cpuIdle, r.haveCPU = ft(kernel)+ft(user), ft(idle), true
	}

	ms := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if ok, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); ok != 0 && ms.AvailPhys <= ms.TotalPhys {
		r.memUsed, r.memTotal = u64(ms.TotalPhys-ms.AvailPhys), u64(ms.TotalPhys)
	}

	// Sum octet counters of all up, non-loopback interfaces.
	if ifs, err := net.Interfaces(); err == nil {
		for _, i := range ifs {
			if i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagUp == 0 {
				continue
			}
			row := windows.MibIfRow2{InterfaceIndex: uint32(i.Index)}
			if windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row) == nil {
				r.netRx += row.InOctets
				r.netTx += row.OutOctets
				r.haveNet = true
			}
		}
	}

	if drives, err := windows.GetLogicalDrives(); err == nil {
		for i := 0; i < 26; i++ {
			if drives&(1<<uint(i)) == 0 {
				continue
			}
			root := string(rune('A'+i)) + `:\`
			p, _ := windows.UTF16PtrFromString(root)
			if windows.GetDriveType(p) != windows.DRIVE_FIXED {
				continue
			}
			var free, total, totalFree uint64
			if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) == nil && totalFree <= total {
				r.disks = append(r.disks, Disk{Mount: root, TotalBytes: total, UsedBytes: total - totalFree})
			}
		}
	}
	return r
}
