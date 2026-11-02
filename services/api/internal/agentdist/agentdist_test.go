package agentdist

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadOnlyAcceptsKnownBuilds(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "osriva-agent-linux-amd64", "linux binary")
	write(t, dir, "osriva-agent-windows-amd64.exe", "windows binary")
	write(t, dir, "osriva-agent-windows-arm64", "missing .exe: ignored")
	write(t, dir, "osriva-agent-linux-amd64.exe", "linux with .exe: ignored")
	write(t, dir, "osriva-agent-plan9-386", "unknown platform: ignored")
	write(t, dir, "README", "ignored")
	_ = os.Mkdir(filepath.Join(dir, "osriva-agent-freebsd-amd64"), 0o755) // a directory: ignored

	d, err := Load(dir, "https://panel.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(d.Builds()); got != 2 {
		t.Fatalf("builds = %d: %+v", got, d.Builds())
	}
	b, ok := d.Get("linux", "amd64")
	sum := sha256.Sum256([]byte("linux binary"))
	if !ok || b.SHA256 != hex.EncodeToString(sum[:]) || b.Size != int64(len("linux binary")) {
		t.Fatalf("linux build: %+v", b)
	}
	for _, p := range [][2]string{{"../../etc", "passwd"}, {"windows", "arm64"}, {"linux", "amd64.exe"}} {
		if _, ok := d.Get(p[0], p[1]); ok {
			t.Errorf("Get(%q, %q) should not exist", p[0], p[1])
		}
	}
	if !d.HasFamily("windows") || !d.HasFamily("unix") {
		t.Fatal("HasFamily")
	}
	if w, _ := d.Get("windows", "amd64"); w.FileName() != "osriva-agent-windows-amd64.exe" {
		t.Fatal(w.FileName())
	}
}

func TestEmptyDirMeansNoBuilds(t *testing.T) {
	d, err := Load("", "https://p")
	if err != nil || len(d.Builds()) != 0 || d.HasFamily("unix") {
		t.Fatalf("empty: %v %+v", err, d.Builds())
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing"), "https://p"); err == nil {
		t.Fatal("missing dir should be an error")
	}
}

func TestScriptsQuotePanelURL(t *testing.T) {
	d, _ := Load("", "https://panel.example.com")
	sh := string(d.InstallSh())
	if !strings.HasPrefix(sh, "#!/bin/sh\n") || !strings.Contains(sh, "PANEL_URL='https://panel.example.com'") {
		t.Fatalf("install.sh header:\n%.300s", sh)
	}
	ps := string(d.InstallPs1())
	if !strings.Contains(ps, "$PanelUrl = 'https://panel.example.com'\r\n") || strings.Contains(strings.ReplaceAll(ps, "\r\n", ""), "\n") {
		t.Fatal("install.ps1 must use CRLF and a quoted URL")
	}
	// A hostile URL cannot break out of the quoting.
	evil, _ := Load("", "https://x'; rm -rf / #")
	if !strings.Contains(string(evil.InstallSh()), `PANEL_URL='https://x'\''; rm -rf / #'`) {
		t.Fatal("sh quoting")
	}
	if !strings.Contains(string(evil.InstallPs1()), `$PanelUrl = 'https://x''; rm -rf / #'`) {
		t.Fatal("ps quoting")
	}
}

func TestCommands(t *testing.T) {
	d, _ := Load("", "https://panel.example.com")
	u := d.UnixCommand("tok", "sha256:ab")
	if u != "curl -fsSL https://panel.example.com/api/v1/agent/install.sh | sudo OSRIVA_ENROLL_TOKEN='tok' sh -s -- --ca-fingerprint sha256:ab" {
		t.Fatal(u)
	}
	w := d.WindowsCommand("t'ok", "sha256:ab")
	if w != "& ([scriptblock]::Create((irm 'https://panel.example.com/api/v1/agent/install.ps1'))) -Token 't''ok' -CaFingerprint 'sha256:ab'" {
		t.Fatal(w)
	}
}
