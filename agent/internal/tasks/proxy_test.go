package tasks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

func TestRenderCaddyfile(t *testing.T) {
	got := renderCaddyfile(agenttask.ProxyConfig{HTTPPort: 80, HTTPSPort: 443, ACMEEmail: "ops@example.com", ACMEStaging: true,
		Sites: []agenttask.ProxySite{
			{Domain: "www.example.com", UpstreamPort: 8080, TLS: agenttask.TLSACME},
			{Domain: "app.internal.test", UpstreamPort: 3000, TLS: agenttask.TLSInternal},
			{Domain: "plain.example.com", UpstreamPort: 9000, TLS: agenttask.TLSNone},
		}})
	want := `# Managed by the OSRIVA agent. Changes made here are overwritten.
{
	admin unix//etc/osriva-proxy/admin.sock
	skip_install_trust
	http_port 80
	https_port 443
	email ops@example.com
	acme_ca https://acme-staging-v02.api.letsencrypt.org/directory
}

app.internal.test {
	tls internal
	reverse_proxy 127.0.0.1:3000
}

http://plain.example.com:80 {
	reverse_proxy 127.0.0.1:9000
}

www.example.com {
	reverse_proxy 127.0.0.1:8080
}
`
	if got != want {
		t.Fatalf("Caddyfile:\n%s", got)
	}
}

type fakeAdmin struct {
	mu     sync.Mutex
	loads  []string
	reject string
}

func (a *fakeAdmin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == http.MethodPost && r.URL.Path == "/load" {
		b, _ := io.ReadAll(r.Body)
		if a.reject != "" && strings.Contains(string(b), a.reject) {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"adapting config using caddyfile: bad"}`)
			return
		}
		a.loads = append(a.loads, string(b))
	}
	w.WriteHeader(200)
}

func setupProxy(t *testing.T) (*fakeEngine, *fakeAdmin, map[int]bool) {
	f, _ := setupDeploy(t)
	f.images[agenttask.ProxyImage] = map[string]string{}
	adm := &fakeAdmin{}
	srv := httptest.NewServer(adm)
	t.Cleanup(srv.Close)
	busy := map[int]bool{}
	oldDir, oldAdmin, oldPort, oldWait := proxyDir, proxyAdmin, portInUse, proxyReadyTimeout
	proxyDir = t.TempDir()
	proxyAdmin = func() (*http.Client, string) { return srv.Client(), srv.URL }
	portInUse = func(p int) bool { return busy[p] }
	proxyReadyTimeout = 2 * time.Second
	t.Cleanup(func() { proxyDir, proxyAdmin, portInUse, proxyReadyTimeout = oldDir, oldAdmin, oldPort, oldWait })
	return f, adm, busy
}

func proxyRun(t *testing.T, typ string, cfg any) (agenttask.ProxyStatusResult, error) {
	t.Helper()
	raw, _ := json.Marshal(cfg)
	if cfg == nil {
		raw = []byte(`{}`)
	}
	out, err := Execute(context.Background(), typ, raw)
	if out == nil {
		return agenttask.ProxyStatusResult{}, err
	}
	return out.(agenttask.ProxyStatusResult), err
}

func TestProxyEnableReloadDisable(t *testing.T) {
	f, adm, busy := setupProxy(t)
	cfg := agenttask.ProxyConfig{HTTPPort: 18880, HTTPSPort: 18443,
		Sites: []agenttask.ProxySite{{Domain: "ci.localhost", UpstreamPort: 8080, TLS: agenttask.TLSInternal}}}

	busy[18443] = true
	if _, err := proxyRun(t, agenttask.ProxyApply, cfg); err == nil || !strings.Contains(err.Error(), "port 18443 is already in use") {
		t.Fatalf("busy port: %v", err)
	}
	if len(f.cts) != 0 {
		t.Fatal("nothing may be created when a port is busy")
	}
	delete(busy, 18443)

	st, err := proxyRun(t, agenttask.ProxyApply, cfg)
	if err != nil || !st.Running || st.Applied == nil || st.Applied.Sites[0].Domain != "ci.localhost" {
		t.Fatalf("enable: %+v %v", st, err)
	}
	ct := f.byRef(agenttask.ProxyContainer)
	hc := ct.Body["HostConfig"].(map[string]any)
	if hc["NetworkMode"] != "host" || hc["Privileged"] != false || ct.Labels[agenttask.ProxyLabel] != "true" {
		t.Fatalf("proxy container: %+v", hc)
	}
	if b, _ := os.ReadFile(filepath.Join(proxyDir, "Caddyfile")); !strings.Contains(string(b), "reverse_proxy 127.0.0.1:8080") {
		t.Fatalf("Caddyfile: %s", b)
	}

	// running: changes go through /load; a rejected config leaves everything as it was
	cfg.Sites = append(cfg.Sites, agenttask.ProxySite{Domain: "two.localhost", UpstreamPort: 9090, TLS: agenttask.TLSInternal})
	if _, err := proxyRun(t, agenttask.ProxyApply, cfg); err != nil || len(adm.loads) != 1 || !strings.Contains(adm.loads[0], "two.localhost") {
		t.Fatalf("reload: %v %d", err, len(adm.loads))
	}
	adm.reject = "three.localhost"
	bad := cfg
	bad.Sites = append(append([]agenttask.ProxySite(nil), cfg.Sites...), agenttask.ProxySite{Domain: "three.localhost", UpstreamPort: 7070, TLS: agenttask.TLSInternal})
	if _, err := proxyRun(t, agenttask.ProxyApply, bad); err == nil || !strings.Contains(err.Error(), "previous one stays active") {
		t.Fatalf("rejected config: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(proxyDir, "Caddyfile")); strings.Contains(string(b), "three.localhost") {
		t.Fatal("rejected config was written")
	}

	st, err = proxyRun(t, agenttask.ProxyDisable, nil)
	if err != nil || st.Installed || len(f.cts) != 0 {
		t.Fatalf("disable: %+v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(proxyDir, "Caddyfile")); err != nil {
		t.Fatal("settings must survive disable")
	}
}

func TestProxyRefusesForeignContainer(t *testing.T) {
	f, _, _ := setupProxy(t)
	f.cts["x"] = &fakeCt{ID: "x", Name: agenttask.ProxyContainer, State: "running", Labels: map[string]string{}}
	cfg := agenttask.ProxyConfig{HTTPPort: 80, HTTPSPort: 443, Sites: []agenttask.ProxySite{}}
	if _, err := proxyRun(t, agenttask.ProxyApply, cfg); err == nil || !strings.Contains(err.Error(), "did not create") {
		t.Fatalf("foreign proxy container: %v", err)
	}
	if _, err := proxyRun(t, agenttask.ProxyDisable, nil); err == nil || f.cts["x"] == nil {
		t.Fatalf("foreign container removed: %v", err)
	}
}

func TestReadCertificates(t *testing.T) {
	root := t.TempDir()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	exp := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ci.localhost"},
		Issuer: pkix.Name{CommonName: "Caddy Local Authority"}, NotBefore: time.Now(), NotAfter: exp, DNSNames: []string{"ci.localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "local", "ci.localhost")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "ci.localhost.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "ci.localhost.key"), []byte("secret"), 0o600)
	certs := readCertificates(root)
	if len(certs) != 1 || certs[0].Domain != "ci.localhost" || certs[0].NotAfter != "2027-01-02T03:04:05Z" || certs[0].Issuer != "ci.localhost" {
		t.Fatalf("certs: %+v", certs)
	}
}
