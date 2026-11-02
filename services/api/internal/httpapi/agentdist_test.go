package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/agentdist"
)

func TestAgentDownloads(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "osriva-agent-linux-amd64"), []byte("ELF..."), 0o755); err != nil {
		t.Fatal(err)
	}
	dist, err := agentdist.Load(dir, "https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	h := New(Options{AgentDist: dist, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	sum := sha256.Sum256([]byte("ELF..."))
	want := hex.EncodeToString(sum[:])

	if rec := get("/api/v1/agent/downloads"); rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	rec := get("/api/v1/agent/download/linux/amd64")
	if rec.Code != 200 || rec.Body.String() != "ELF..." || rec.Header().Get("X-Checksum-SHA256") != want ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "osriva-agent-linux-amd64") {
		t.Fatalf("download: %d %v", rec.Code, rec.Header())
	}
	if rec := get("/api/v1/agent/download/linux/amd64/sha256"); rec.Body.String() != want+"\n" {
		t.Fatalf("sha: %q", rec.Body)
	}
	for _, p := range []string{"/api/v1/agent/download/windows/amd64", "/api/v1/agent/download/..%2f..%2fetc/passwd"} {
		if rec := get(p); rec.Code != 404 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if rec := get("/api/v1/agent/install.sh"); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "#!/bin/sh") {
		t.Fatalf("install.sh: %d", rec.Code)
	}
	if rec := get("/api/v1/agent/install.ps1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "$PanelUrl = 'https://panel.example.com'") {
		t.Fatalf("install.ps1: %d", rec.Code)
	}
}
