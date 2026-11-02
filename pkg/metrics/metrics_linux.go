//go:build linux

package metrics

import (
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
)

func read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func u64(v uint64) *uint64 { return &v }

func readPlatform() raw {
	r := raw{at: time.Now()}
	r.cpuTotal, r.cpuIdle, r.haveCPU = ParseProcStat(read("/proc/stat"))
	r.netRx, r.netTx, r.haveNet = ParseNetDev(read("/proc/net/dev"))
	if mu, mt, su, st, ok := ParseMemSwap(read("/proc/meminfo")); ok {
		r.memUsed, r.memTotal, r.swapUsed, r.swapTotal = u64(mu), u64(mt), u64(su), u64(st)
	}
	if l1, l5, l15, ok := ParseLoadavg(read("/proc/loadavg")); ok {
		r.load1, r.load5, r.load15 = &l1, &l5, &l15
	}
	for _, m := range inventory.ParseMounts(read("/proc/mounts")) {
		var st unix.Statfs_t
		if unix.Statfs(m.Point, &st) != nil {
			continue
		}
		bs := uint64(st.Bsize)
		total := st.Blocks * bs
		r.disks = append(r.disks, Disk{Mount: m.Point, TotalBytes: total, UsedBytes: total - st.Bfree*bs})
	}
	return r
}
