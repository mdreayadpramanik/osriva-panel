package metrics

import (
	"bufio"
	"strconv"
	"strings"
)

// ParseProcStat returns cumulative total and idle (idle+iowait) jiffies from
// the aggregate "cpu" line of /proc/stat. Guest time is already included in
// user/nice, so it is not added twice.
func ParseProcStat(content string) (total, idle uint64, ok bool) {
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var v [8]uint64
		for i := 0; i < 8 && i+1 < len(f); i++ {
			n, err := strconv.ParseUint(f[i+1], 10, 64)
			if err != nil {
				return 0, 0, false
			}
			v[i] = n
		}
		// user nice system idle iowait irq softirq steal
		total = v[0] + v[1] + v[2] + v[3] + v[4] + v[5] + v[6] + v[7]
		return total, v[3] + v[4], true
	}
	return 0, 0, false
}

// ParseNetDev sums received and transmitted bytes over all non-loopback
// interfaces in /proc/net/dev.
func ParseNetDev(content string) (rx, tx uint64, ok bool) {
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		name, rest, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || name == "" || strings.Contains(name, "|") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, err1 := strconv.ParseUint(f[0], 10, 64)
		t, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx, tx, ok = rx+r, tx+t, true
	}
	return rx, tx, ok
}

// ParseLoadavg parses the first three fields of /proc/loadavg.
func ParseLoadavg(content string) (l1, l5, l15 float64, ok bool) {
	f := strings.Fields(content)
	if len(f) < 3 {
		return 0, 0, 0, false
	}
	var v [3]float64
	for i := range v {
		n, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return 0, 0, 0, false
		}
		v[i] = n
	}
	return v[0], v[1], v[2], true
}

// ParseMemSwap returns memory used (total - available) and swap figures from /proc/meminfo, in bytes.
func ParseMemSwap(content string) (memUsed, memTotal, swapUsed, swapTotal uint64, ok bool) {
	vals := map[string]uint64{}
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		if n, err := strconv.ParseUint(f[1], 10, 64); err == nil {
			vals[strings.TrimSuffix(f[0], ":")] = n * 1024
		}
	}
	total, haveTotal := vals["MemTotal"]
	avail, haveAvail := vals["MemAvailable"]
	if !haveAvail {
		avail = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
	}
	if !haveTotal || avail > total {
		return 0, 0, 0, 0, false
	}
	st, sf := vals["SwapTotal"], vals["SwapFree"]
	if sf > st {
		sf = st
	}
	return total - avail, total, st - sf, st, true
}
