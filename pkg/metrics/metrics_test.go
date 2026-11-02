package metrics

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseProcStat(t *testing.T) {
	total, idle, ok := ParseProcStat("cpu  100 5 50 800 20 3 2 10 7 0\ncpu0 1 2 3 4\nintr 1\n")
	if !ok || total != 990 || idle != 820 {
		t.Fatalf("total=%d idle=%d ok=%v", total, idle, ok)
	}
	if _, _, ok := ParseProcStat("garbage"); ok {
		t.Fatal("garbage accepted")
	}
}

func TestParseNetDev(t *testing.T) {
	rx, tx, ok := ParseNetDev(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 9999999     100    0    0    0     0          0         0  9999999     100    0    0    0     0       0          0
  eth0: 1000      10    0    0    0     0          0         0      400       5    0    0    0     0       0          0
docker0:  500       3    0    0    0     0          0         0      100       1    0    0    0     0       0          0
`)
	if !ok || rx != 1500 || tx != 500 {
		t.Fatalf("rx=%d tx=%d", rx, tx)
	}
}

func TestParseLoadavgAndMem(t *testing.T) {
	l1, l5, l15, ok := ParseLoadavg("0.52 0.58 0.59 1/467 12345\n")
	if !ok || l1 != 0.52 || l5 != 0.58 || l15 != 0.59 {
		t.Fatalf("load %v %v %v", l1, l5, l15)
	}
	mu, mt, su, st, ok := ParseMemSwap("MemTotal: 1000 kB\nMemAvailable: 250 kB\nSwapTotal: 400 kB\nSwapFree: 100 kB\n")
	if !ok || mu != 750*1024 || mt != 1000*1024 || su != 300*1024 || st != 400*1024 {
		t.Fatalf("mem %d/%d swap %d/%d", mu, mt, su, st)
	}
}

func TestRatesNeedTwoReadings(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	readings := []raw{
		{at: t0, cpuTotal: 1000, cpuIdle: 800, haveCPU: true, netRx: 0, netTx: 0, haveNet: true},
		{at: t0.Add(10 * time.Second), cpuTotal: 1100, cpuIdle: 875, haveCPU: true, netRx: 10_000, netTx: 2_000, haveNet: true},
		{at: t0.Add(20 * time.Second), cpuTotal: 1200, cpuIdle: 900, haveCPU: true, netRx: 5, netTx: 2_500, haveNet: true}, // rx counter reset
	}
	i := 0
	c := &Collector{read: func() raw { r := readings[i]; i++; return r }}

	first := c.Sample()
	if first.CPUPercent != nil || first.NetRxBps != nil {
		t.Fatal("first sample must not invent rates")
	}
	second := c.Sample()
	if second.CPUPercent == nil || *second.CPUPercent != 25 || second.NetRxBps == nil || *second.NetRxBps != 1000 || *second.NetTxBps != 200 {
		t.Fatalf("second: cpu=%v rx=%v tx=%v", second.CPUPercent, second.NetRxBps, second.NetTxBps)
	}
	third := c.Sample()
	if third.CPUPercent == nil || *third.CPUPercent != 75 {
		t.Fatalf("third cpu: %v", third.CPUPercent)
	}
	if third.NetRxBps != nil {
		t.Fatal("a counter that went backwards must yield no rate")
	}
}

func TestUnavailableValuesAreOmitted(t *testing.T) {
	c := &Collector{read: func() raw { return raw{at: time.Unix(1, 0)} }}
	b, _ := json.Marshal(c.Sample())
	if s := string(b); strings.Contains(s, "cpu_percent") || strings.Contains(s, "mem_") || strings.Contains(s, "load1") {
		t.Fatalf("missing values must be omitted, got %s", s)
	}
}

// Two real readings on the machine running the test (Windows locally, Linux in CI).
func TestRealHost(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("no collector on " + runtime.GOOS)
	}
	c := NewCollector()
	c.Sample()
	time.Sleep(1100 * time.Millisecond)
	s := c.Sample()
	if s.CPUPercent == nil || *s.CPUPercent < 0 || *s.CPUPercent > 100 {
		t.Fatalf("cpu: %v", s.CPUPercent)
	}
	if s.MemTotalBytes == nil || s.MemUsedBytes == nil || *s.MemUsedBytes > *s.MemTotalBytes || len(s.Disks) == 0 {
		raw, _ := json.Marshal(s)
		t.Fatalf("memory/disks missing: %s", raw)
	}
	if runtime.GOOS == "linux" && s.Load1 == nil {
		t.Fatal("load average missing on linux")
	}
}
