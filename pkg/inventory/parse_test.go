package inventory

import (
	"encoding/json"
	"runtime"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	r := ParseOSRelease(`# comment
PRETTY_NAME="Ubuntu 24.04.1 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
ID=ubuntu
ID_LIKE=debian
QUOTED='single'
ESCAPED="a \"b\" c"
garbage line
`)
	want := map[string]string{"PRETTY_NAME": "Ubuntu 24.04.1 LTS", "VERSION_ID": "24.04", "ID": "ubuntu",
		"ID_LIKE": "debian", "QUOTED": "single", "ESCAPED": `a "b" c`}
	for k, v := range want {
		if r[k] != v {
			t.Errorf("%s = %q, want %q", k, r[k], v)
		}
	}
}

func TestParseMeminfo(t *testing.T) {
	total, avail := ParseMeminfo("MemTotal:       16303524 kB\nMemFree:  1000 kB\nMemAvailable:   12045632 kB\n")
	if total != 16303524*1024 || avail != 12045632*1024 {
		t.Fatalf("total=%d avail=%d", total, avail)
	}
	// Old kernels without MemAvailable fall back to free+buffers+cached.
	_, avail = ParseMeminfo("MemTotal: 100 kB\nMemFree: 10 kB\nBuffers: 5 kB\nCached: 20 kB\n")
	if avail != 35*1024 {
		t.Fatalf("fallback avail=%d", avail)
	}
}

func TestParseCPUModel(t *testing.T) {
	x86 := "processor\t: 0\nvendor_id\t: GenuineIntel\nmodel name\t: Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz\n"
	if m := ParseCPUModel(x86); m != "Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz" {
		t.Fatalf("x86: %q", m)
	}
	arm := "processor\t: 0\nBogoMIPS\t: 108.00\nModel\t\t: Raspberry Pi 4 Model B Rev 1.4\n"
	if m := ParseCPUModel(arm); m != "Raspberry Pi 4 Model B Rev 1.4" {
		t.Fatalf("arm: %q", m)
	}
}

func TestParseUptime(t *testing.T) {
	if u := ParseUptime("350735.47 234388.90\n"); u != 350735 {
		t.Fatalf("uptime %d", u)
	}
	if ParseUptime("") != 0 || ParseUptime("nope") != 0 {
		t.Fatal("invalid input should yield 0")
	}
}

func TestParseMounts(t *testing.T) {
	ms := ParseMounts(`sysfs /sys sysfs rw 0 0
proc /proc proc rw 0 0
/dev/sda1 / ext4 rw,relatime 0 0
tmpfs /run tmpfs rw 0 0
/dev/sda2 /mnt/my\040disk xfs rw 0 0
/dev/sda1 / ext4 rw 0 0
overlay /var/lib/docker/overlay2/x/merged overlay rw 0 0
`)
	if len(ms) != 2 || ms[0].Point != "/" || ms[1].Point != "/mnt/my disk" || ms[1].FSType != "xfs" {
		t.Fatalf("mounts: %+v", ms)
	}
}

// Collect runs against the real host this test executes on (Windows locally,
// Linux in CI) and checks that the essential facts were found.
func TestCollectOnThisHost(t *testing.T) {
	inv := Collect()
	if inv.OS.Family != runtime.GOOS || inv.OS.Arch != runtime.GOARCH || inv.Hostname == "" {
		t.Fatalf("portable facts missing: %+v", inv.OS)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "windows" {
		if inv.OS.Name == "" || inv.OS.Kernel == "" || inv.Memory.TotalBytes == 0 || len(inv.Disks) == 0 || inv.InitSystem == "" {
			raw, _ := json.MarshalIndent(inv, "", "  ")
			t.Fatalf("native facts missing:\n%s", raw)
		}
		if inv.Memory.AvailableBytes > inv.Memory.TotalBytes {
			t.Fatal("available memory exceeds total")
		}
		for _, d := range inv.Disks {
			if d.FreeBytes > d.TotalBytes {
				t.Fatalf("disk %s: free > total", d.Mount)
			}
		}
	}
}
