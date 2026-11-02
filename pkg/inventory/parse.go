package inventory

import (
	"bufio"
	"strconv"
	"strings"
)

// Parsers for Linux text formats. They are OS-independent so they can be unit
// tested on any platform with recorded fixtures.

// ParseOSRelease parses /etc/os-release (freedesktop.org format).
func ParseOSRelease(content string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		v = strings.NewReplacer(`\"`, `"`, `\$`, `$`, `\\`, `\`, "\\`", "`").Replace(v)
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// ParseMeminfo returns MemTotal and MemAvailable in bytes from /proc/meminfo.
func ParseMeminfo(content string) (total, available uint64) {
	sc := bufio.NewScanner(strings.NewReader(content))
	var free, buffers, cached uint64
	haveAvail := false
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		kib := n * 1024
		switch strings.TrimSuffix(f[0], ":") {
		case "MemTotal":
			total = kib
		case "MemAvailable":
			available, haveAvail = kib, true
		case "MemFree":
			free = kib
		case "Buffers":
			buffers = kib
		case "Cached":
			cached = kib
		}
	}
	if !haveAvail { // kernels older than 3.14
		available = free + buffers + cached
	}
	return total, available
}

// ParseCPUModel returns the first CPU model name from /proc/cpuinfo.
func ParseCPUModel(content string) string {
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "model name", "Model", "cpu model", "Processor":
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
	}
	return ""
}

// ParseUptime returns whole seconds from /proc/uptime.
func ParseUptime(content string) uint64 {
	f := strings.Fields(content)
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0
	}
	return uint64(v)
}

// Mount is one line of /proc/mounts.
type Mount struct{ Device, Point, FSType string }

// realFilesystems are on-disk or network filesystems worth reporting.
var realFilesystems = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true, "f2fs": true,
	"jfs": true, "reiserfs": true, "vfat": true, "exfat": true, "ntfs": true, "ntfs3": true,
	"nfs": true, "nfs4": true, "cifs": true, "bcachefs": true,
}

// ParseMounts returns real-filesystem mounts from /proc/mounts, de-duplicated by mount point.
func ParseMounts(content string) []Mount {
	seen := map[string]bool{}
	var out []Mount
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || !realFilesystems[f[2]] {
			continue
		}
		point := unescapeMount(f[1])
		if seen[point] {
			continue
		}
		seen[point] = true
		out = append(out, Mount{Device: f[0], Point: point, FSType: f[2]})
	}
	return out
}

// unescapeMount decodes the octal escapes (\040 for space, …) used in /proc/mounts.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
