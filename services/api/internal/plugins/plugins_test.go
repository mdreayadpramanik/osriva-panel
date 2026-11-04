package plugins

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/pluginsdk"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store/memstore"
)

func exeName(n string) string {
	if runtime.GOOS == "windows" {
		return n + ".exe"
	}
	return n
}

// install builds pkg into root/name and writes a manifest signed with key.
func install(t *testing.T, root, name, pkg string, key ed25519.PrivateKey) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := exeName(name)
	out, err := exec.Command("go", "build", "-o", filepath.Join(dir, exe), pkg).CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}
	sum, _ := pluginsdk.HashFile(filepath.Join(dir, exe))
	m := pluginsdk.Manifest{Name: name, Version: "1.0.0", Publisher: "test", Protocol: pluginsdk.Protocol, Kinds: []string{pluginsdk.KindNotifier},
		Executables: map[string]pluginsdk.Executable{runtime.GOOS + "/" + runtime.GOARCH: {Path: exe, SHA256: sum}}}
	m.Sign(key)
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, pluginsdk.ManifestFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

type env struct {
	reg   *Registry
	owner *identity.Principal
	root  string
}

func setup(t *testing.T) (*env, ed25519.PrivateKey) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds plugins")
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	root := t.TempDir()
	install(t, root, "chat-notifier", "github.com/mdreayadpramanik/osriva-panel/pkg/pluginsdk/examples/chat-notifier", priv)
	install(t, root, "evil", "github.com/mdreayadpramanik/osriva-panel/services/api/internal/plugins/testdata/evil", priv)
	install(t, root, "untrusted", "github.com/mdreayadpramanik/osriva-panel/pkg/pluginsdk/examples/chat-notifier", other)

	st := memstore.New()
	keys, _ := secrets.NewKeyring(bytes.Repeat([]byte{4}, 32))
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	id, err := identity.New(identity.Options{Store: st, Keys: keys, Log: quiet,
		Argon2: auth.Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	_ = id.SyncRBAC(context.Background())
	pw := "a long enough password"
	if _, err := id.BootstrapUser(context.Background(), "owner@example.com", "O", pw, rbac.RoleOwner, "test"); err != nil {
		t.Fatal(err)
	}
	login, _ := id.Login(context.Background(), identity.LoginInput{Email: "owner@example.com", Password: pw})
	reg, err := New(root, []string{base64.StdEncoding.EncodeToString(pub)}, st, id, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return &env{reg: reg, owner: login.Principal, root: root}, priv
}

func TestRegistryVerifiesAndPins(t *testing.T) {
	e, _ := setup(t)
	ctx := context.Background()
	list, err := e.reg.List(ctx, e.owner)
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %+v %v", list, err)
	}
	byName := map[string]View{}
	for _, v := range list {
		byName[v.Entry.Name] = v
	}
	if v := byName["untrusted"]; v.Entry.Valid || !strings.Contains(v.Entry.Reason, "not trusted") {
		t.Fatalf("untrusted plugin: %+v", v.Entry)
	}
	if v := byName["chat-notifier"]; !v.Entry.Valid || v.Runnable {
		t.Fatalf("valid but not yet approved: %+v", v)
	}
	if _, err := e.reg.SetEnabled(ctx, e.owner, "untrusted", true); err == nil {
		t.Fatal("an invalid plugin was approved")
	}
	if err := e.reg.Notify(ctx, "chat-notifier", nil, "", pluginsdk.Event{}); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("unapproved plugin ran: %v", err)
	}

	got := make(chan string, 1)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
	}))
	defer hook.Close()
	if _, err := e.reg.SetEnabled(ctx, e.owner, "chat-notifier", true); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]string{"url": hook.URL})
	if err := e.reg.Notify(ctx, "chat-notifier", settings, "", pluginsdk.Event{Message: "[OSRIVA] FIRING: cpu on web-1"}); err != nil {
		t.Fatal(err)
	}
	if body := <-got; body != `{"text":"[OSRIVA] FIRING: cpu on web-1"}` {
		t.Fatalf("chat payload: %s", body)
	}
	if err := e.reg.Notify(ctx, "chat-notifier", json.RawMessage(`{"url":"ftp://x"}`), "", pluginsdk.Event{}); err == nil {
		t.Fatal("bad URL accepted")
	} else {
		var ce *CallError
		if !errors.As(err, &ce) || !ce.Permanent {
			t.Fatalf("a bad URL is a permanent error: %v", err)
		}
	}

	// Replacing the binary after approval stops it until it is approved again.
	exe := filepath.Join(e.root, "chat-notifier", exeName("chat-notifier"))
	f, _ := os.OpenFile(exe, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.Write([]byte("tampered"))
	f.Close()
	if err := e.reg.Notify(ctx, "chat-notifier", settings, "", pluginsdk.Event{}); err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("tampered binary ran: %v", err)
	}
	e.reg.Scan()
	if v, _ := e.reg.List(ctx, e.owner); v[0].Entry.Valid {
		t.Fatalf("a binary that no longer matches its signature is invalid: %+v", v[0].Entry)
	}
}

func TestRunnerContainsPlugins(t *testing.T) {
	e, _ := setup(t)
	ctx := context.Background()
	t.Setenv("OSRIVA_SECRET_KEY", "must-not-leak")
	t.Setenv("OSRIVA_DATABASE_URL", "postgres://secret@db")
	if _, err := e.reg.SetEnabled(ctx, e.owner, "evil", true); err != nil {
		t.Fatal(err)
	}
	call := func(mode, secret string) error {
		return e.reg.Notify(ctx, "evil", json.RawMessage(`{"mode":"`+mode+`"}`), secret, pluginsdk.Event{})
	}

	err := call("env", "channel-token")
	if err == nil || strings.Contains(err.Error(), "must-not-leak") || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("the panel environment leaked into the plugin: %v", err)
	}
	if !strings.Contains(err.Error(), "OSRIVA_PLUGIN_PROTOCOL=1") || !strings.Contains(err.Error(), "secret=channel-token") ||
		!strings.Contains(err.Error(), filepath.Join(e.root, "evil")) {
		t.Fatalf("clean environment, secret on stdin, plugin directory as cwd: %v", err)
	}

	var ce *CallError
	if err := call("flood", ""); !errors.As(err, &ce) || ce.Code != "output_too_large" {
		t.Fatalf("flood: %v", err)
	}
	if err := call("garbage", ""); !errors.As(err, &ce) || ce.Code != "bad_response" {
		t.Fatalf("garbage: %v", err)
	}
	if err := call("fail", ""); !errors.As(err, &ce) || ce.Permanent {
		t.Fatalf("a plain error is retryable: %v", err)
	}

	old := CallTimeout
	CallTimeout = 2 * time.Second
	defer func() { CallTimeout = old }()
	start := time.Now()
	if err := call("hang", ""); !errors.As(err, &ce) || ce.Code != "timeout" {
		t.Fatalf("hang: %v", err)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("a hanging plugin was not killed in time: %v", d)
	}
}
