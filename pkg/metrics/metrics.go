// Package metrics samples live resource usage on the agent. Rates (CPU %,
// network throughput) come from the difference between two real readings, so
// the first sample after start has no rate. Values a platform cannot provide
// are nil and omitted from JSON, never zero-filled.
package metrics

import (
	"sort"
	"time"
)

type Disk struct {
	Mount      string `json:"mount"`
	UsedBytes  uint64 `json:"used_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

type Sample struct {
	TS             time.Time `json:"ts"`
	CPUPercent     *float64  `json:"cpu_percent,omitempty"`
	MemUsedBytes   *uint64   `json:"mem_used_bytes,omitempty"`
	MemTotalBytes  *uint64   `json:"mem_total_bytes,omitempty"`
	SwapUsedBytes  *uint64   `json:"swap_used_bytes,omitempty"`
	SwapTotalBytes *uint64   `json:"swap_total_bytes,omitempty"`
	Load1          *float64  `json:"load1,omitempty"`
	Load5          *float64  `json:"load5,omitempty"`
	Load15         *float64  `json:"load15,omitempty"`
	NetRxBps       *float64  `json:"net_rx_bps,omitempty"`
	NetTxBps       *float64  `json:"net_tx_bps,omitempty"`
	Disks          []Disk    `json:"disks,omitempty"`
}

// raw is one platform reading; rate fields are cumulative counters.
type raw struct {
	at                   time.Time
	cpuTotal, cpuIdle    uint64 // cumulative ticks
	haveCPU              bool
	netRx, netTx         uint64 // cumulative bytes
	haveNet              bool
	memUsed, memTotal    *uint64
	swapUsed, swapTotal  *uint64
	load1, load5, load15 *float64
	disks                []Disk
}

// Collector produces samples; it keeps the previous reading to compute rates.
type Collector struct {
	prev *raw
	read func() raw
}

func NewCollector() *Collector { return &Collector{read: readPlatform} }

func ptrF(v float64) *float64 { return &v }

// Sample takes a reading and returns a sample.
func (c *Collector) Sample() Sample {
	cur := c.read()
	if cur.at.IsZero() {
		cur.at = time.Now()
	}
	s := Sample{TS: cur.at.UTC().Truncate(time.Second), MemUsedBytes: cur.memUsed, MemTotalBytes: cur.memTotal,
		SwapUsedBytes: cur.swapUsed, SwapTotalBytes: cur.swapTotal, Load1: cur.load1, Load5: cur.load5, Load15: cur.load15,
		Disks: cur.disks}
	if p := c.prev; p != nil {
		if cur.haveCPU && p.haveCPU && cur.cpuTotal > p.cpuTotal && cur.cpuIdle >= p.cpuIdle {
			dt, di := float64(cur.cpuTotal-p.cpuTotal), float64(cur.cpuIdle-p.cpuIdle)
			s.CPUPercent = ptrF(clamp((1-di/dt)*100, 0, 100))
		}
		secs := cur.at.Sub(p.at).Seconds()
		// Counters that went backwards (interface reset, wrap) yield no rate.
		if cur.haveNet && p.haveNet && secs > 0 && cur.netRx >= p.netRx && cur.netTx >= p.netTx {
			s.NetRxBps = ptrF(float64(cur.netRx-p.netRx) / secs)
			s.NetTxBps = ptrF(float64(cur.netTx-p.netTx) / secs)
		}
	}
	sort.Slice(s.Disks, func(i, j int) bool { return s.Disks[i].Mount < s.Disks[j].Mount })
	c.prev = &cur
	return s
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
