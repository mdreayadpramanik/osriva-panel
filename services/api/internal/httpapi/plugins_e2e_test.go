package httpapi

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mdreayadpramanik/osriva-panel/pkg/pluginsdk"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/monitor"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/plugins"
)

func TestPluginNotificationChannel(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin")
	}
	e := newEnv(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	root := t.TempDir()
	dir := filepath.Join(root, "chat-notifier")
	_ = os.MkdirAll(dir, 0o755)
	exe := "chat-notifier"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", filepath.Join(dir, exe), "github.com/mdreayadpramanik/osriva-panel/pkg/pluginsdk/examples/chat-notifier").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	sum, _ := pluginsdk.HashFile(filepath.Join(dir, exe))
	m := pluginsdk.Manifest{Name: "chat-notifier", Version: "0.1.0", Publisher: "OSRIVA examples", Protocol: pluginsdk.Protocol,
		Kinds: []string{pluginsdk.KindNotifier}, Executables: map[string]pluginsdk.Executable{runtime.GOOS + "/" + runtime.GOARCH: {Path: exe, SHA256: sum}}}
	m.Sign(priv)
	raw, _ := json.Marshal(m)
	_ = os.WriteFile(filepath.Join(dir, pluginsdk.ManifestFile), raw, 0o644)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg, err := plugins.New(root, []string{base64.StdEncoding.EncodeToString(pub)}, e.store, e.svc, quiet)
	if err != nil {
		t.Fatal(err)
	}
	mon := monitor.New(e.store, e.svc, e.keys, quiet, nil)
	mon.UsePlugins(reg)
	e.h = New(Options{Identity: e.svc, Monitor: mon, Plugins: reg, DB: e.db, Log: quiet, Version: "test", CookieSecure: true})
	c := e.login("owner@example.com")

	got := make(chan string, 4)
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
	}))
	defer chat.Close()

	rec := e.do("GET", "/api/v1/plugins", nil, c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"valid":true`) || !strings.Contains(rec.Body.String(), `"runnable":false`) {
		t.Fatalf("plugins: %d %s", rec.Code, rec.Body)
	}
	channel := map[string]any{"name": "team chat", "kind": "plugin", "enabled": true,
		"config": map[string]any{"plugin": "chat-notifier", "settings": map[string]string{}}, "secret": chat.URL + "/hooks/T0KEN"}
	rec = e.do("POST", "/api/v1/alerts/channels", channel, c)
	if rec.Code != 201 {
		t.Fatalf("plugin channel: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// Not approved yet: the test notification fails with a clear reason.
	rec = e.do("POST", "/api/v1/alerts/channels/"+created.Channel.ID+"/test", nil, c)
	if !strings.Contains(rec.Body.String(), "not enabled") {
		t.Fatalf("unapproved plugin: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("PUT", "/api/v1/plugins/chat-notifier", map[string]bool{"enabled": true}, c); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"runnable":true`) {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("POST", "/api/v1/alerts/channels/"+created.Channel.ID+"/test", nil, c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"delivered":1`) {
		t.Fatalf("test through the plugin: %d %s", rec.Code, rec.Body)
	}
	if body := <-got; !strings.Contains(body, `"text":"[OSRIVA] Test notification from OSRIVA Panel."`) {
		t.Fatalf("chat message: %s", body)
	}
	if strings.Contains(e.do("GET", "/api/v1/audit-logs?limit=100", nil, c).Body.String(), "T0KEN") {
		t.Fatal("the plugin secret leaked into the audit log")
	}
	if rec := e.do("POST", "/api/v1/alerts/channels", map[string]any{"name": "x", "kind": "plugin", "enabled": true,
		"config": map[string]any{"plugin": "not-installed"}}, c); rec.Code != 400 {
		t.Fatalf("unknown plugin: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("PUT", "/api/v1/plugins/chat-notifier", map[string]bool{"enabled": false}, c); rec.Code != 200 {
		t.Fatalf("disable: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/alerts/channels/"+created.Channel.ID+"/test", nil, c); !bytes.Contains(rec.Body.Bytes(), []byte("not enabled")) {
		t.Fatalf("disabled plugin ran: %s", rec.Body)
	}
}
