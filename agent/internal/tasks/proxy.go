package tasks

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

// The reverse proxy (docs/adr/0008-reverse-proxy.md) is Caddy in a container
// on the host network. Its Caddyfile, state and certificates live in
// proxyDir (0700, root only), and its admin API listens only on a Unix socket
// in that directory.

var (
	proxyDir = "/var/lib/osriva-agent/proxy"
	// proxyAdmin returns the client and base URL of Caddy's admin API; replaced in tests.
	proxyAdmin = func() (*http.Client, string) {
		sock := filepath.Join(proxyDir, "admin.sock")
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}
		// Caddy only accepts Host values of "", 127.0.0.1 or ::1 on a Unix socket.
		return &http.Client{Transport: tr, Timeout: 30 * time.Second}, "http://127.0.0.1"
	}
	// portInUse is replaced in tests.
	portInUse = func(port int) bool {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return strings.Contains(err.Error(), "in use") || strings.Contains(err.Error(), "Only one usage")
		}
		ln.Close()
		return false
	}
	proxyReadyTimeout = 20 * time.Second
)

const acmeStagingCA = "https://acme-staging-v02.api.letsencrypt.org/directory"

// renderCaddyfile turns a validated configuration into a Caddyfile. Every
// interpolated value was checked by agenttask.ValidateProxyConfig (domains are
// RFC 1123 names, ports are integers, the e-mail has no spaces or braces).
func renderCaddyfile(c agenttask.ProxyConfig) string {
	var b strings.Builder
	b.WriteString("# Managed by the OSRIVA agent. Changes made here are overwritten.\n{\n")
	b.WriteString("\tadmin unix//etc/osriva-proxy/admin.sock\n\tskip_install_trust\n")
	fmt.Fprintf(&b, "\thttp_port %d\n\thttps_port %d\n", c.HTTPPort, c.HTTPSPort)
	if c.ACMEEmail != "" {
		fmt.Fprintf(&b, "\temail %s\n", c.ACMEEmail)
	}
	if c.ACMEStaging {
		fmt.Fprintf(&b, "\tacme_ca %s\n", acmeStagingCA)
	}
	b.WriteString("}\n")
	sites := append([]agenttask.ProxySite(nil), c.Sites...)
	sort.Slice(sites, func(i, j int) bool { return sites[i].Domain < sites[j].Domain })
	for _, s := range sites {
		addr := s.Domain
		if s.TLS == agenttask.TLSNone {
			addr = fmt.Sprintf("http://%s:%d", s.Domain, c.HTTPPort)
		}
		fmt.Fprintf(&b, "\n%s {\n", addr)
		if s.TLS == agenttask.TLSInternal {
			b.WriteString("\ttls internal\n")
		}
		fmt.Fprintf(&b, "\treverse_proxy 127.0.0.1:%d\n}\n", s.UpstreamPort)
	}
	return b.String()
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func adminDo(ctx context.Context, method, path, contentType string, body io.Reader) error {
	hc, base := proxyAdmin()
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		if json.Unmarshal(msg, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("caddy admin: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func proxyApply(ctx context.Context, c *dockerClient, cfg agenttask.ProxyConfig) (agenttask.ProxyStatusResult, error) {
	if err := os.MkdirAll(filepath.Join(proxyDir, "data"), 0o700); err != nil {
		return agenttask.ProxyStatusResult{}, err
	}
	_ = os.Chmod(proxyDir, 0o700)
	caddyfile := []byte(renderCaddyfile(cfg))
	applied, _ := json.Marshal(cfg)

	in, err := c.inspect(ctx, agenttask.ProxyContainer)
	exists := err == nil
	var de *dockerError
	if err != nil && !(errors.As(err, &de) && de.Status == http.StatusNotFound) {
		return agenttask.ProxyStatusResult{}, err
	}
	if exists && in.Config.Labels[agenttask.ProxyLabel] != "true" {
		return agenttask.ProxyStatusResult{}, fmt.Errorf("a container named %s exists that OSRIVA did not create", agenttask.ProxyContainer)
	}

	if exists && in.State.Status == "running" {
		// Caddy validates the new configuration and keeps the old one if it is rejected.
		if err := adminDo(ctx, http.MethodPost, "/load", "text/caddyfile", strings.NewReader(string(caddyfile))); err != nil {
			return agenttask.ProxyStatusResult{}, fmt.Errorf("the proxy rejected the configuration (the previous one stays active): %w", err)
		}
		if err := writeFileAtomic(filepath.Join(proxyDir, "Caddyfile"), caddyfile); err != nil {
			return agenttask.ProxyStatusResult{}, err
		}
		if err := writeFileAtomic(filepath.Join(proxyDir, "applied.json"), applied); err != nil {
			return agenttask.ProxyStatusResult{}, err
		}
		return proxyStatus(ctx, c)
	}

	// Not running: the ports must be free before Caddy takes them.
	for _, p := range []int{cfg.HTTPPort, cfg.HTTPSPort} {
		if portInUse(p) {
			return agenttask.ProxyStatusResult{}, fmt.Errorf("port %d is already in use on this server (another web server, or the OSRIVA control plane itself?); choose other ports or free it", p)
		}
	}
	prevFile, _ := os.ReadFile(filepath.Join(proxyDir, "Caddyfile"))
	prevApplied, _ := os.ReadFile(filepath.Join(proxyDir, "applied.json"))
	if err := writeFileAtomic(filepath.Join(proxyDir, "Caddyfile"), caddyfile); err != nil {
		return agenttask.ProxyStatusResult{}, err
	}
	if err := writeFileAtomic(filepath.Join(proxyDir, "applied.json"), applied); err != nil {
		return agenttask.ProxyStatusResult{}, err
	}
	restore := func() {
		if prevFile != nil {
			_ = writeFileAtomic(filepath.Join(proxyDir, "Caddyfile"), prevFile)
			_ = writeFileAtomic(filepath.Join(proxyDir, "applied.json"), prevApplied)
		}
	}
	id := in.ID
	if !exists {
		if id, err = createProxy(ctx, c); err != nil {
			restore()
			return agenttask.ProxyStatusResult{}, err
		}
	}
	if err := c.json(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil, nil); err != nil {
		restore()
		return agenttask.ProxyStatusResult{}, fmt.Errorf("the proxy did not start: %w", err)
	}
	if err := waitProxy(ctx, c, id); err != nil {
		_ = c.json(ctx, http.MethodPost, "/containers/"+id+"/stop", nil, nil, nil)
		restore()
		return agenttask.ProxyStatusResult{}, err
	}
	return proxyStatus(ctx, c)
}

func createProxy(ctx context.Context, c *dockerClient) (string, error) {
	var img struct{ Id string }
	if err := c.json(ctx, http.MethodGet, "/images/"+agenttask.ProxyImage+"/json", nil, nil, &img); err != nil {
		if _, err := dockerPull(ctx, c, agenttask.ProxyImage); err != nil {
			return "", err
		}
	}
	body := map[string]any{
		"Image":  agenttask.ProxyImage,
		"Cmd":    []string{"caddy", "run", "--config", "/etc/osriva-proxy/Caddyfile", "--adapter", "caddyfile"},
		"Labels": map[string]string{agenttask.ProxyLabel: "true", "dev.osriva.created-by": "osriva-agent"},
		"HostConfig": map[string]any{
			// Host networking: the proxy binds the web ports and reaches apps on 127.0.0.1.
			"NetworkMode":   "host",
			"Binds":         []string{proxyDir + ":/etc/osriva-proxy", filepath.Join(proxyDir, "data") + ":/data"},
			"RestartPolicy": map[string]string{"Name": "unless-stopped"},
			"Privileged":    false,
			"SecurityOpt":   []string{"no-new-privileges"},
			"CapDrop":       []string{"ALL"},
			"CapAdd":        []string{"NET_BIND_SERVICE"},
		},
	}
	var created struct{ Id string }
	if err := c.json(ctx, http.MethodPost, "/containers/create", url.Values{"name": {agenttask.ProxyContainer}}, body, &created); err != nil {
		return "", err
	}
	return created.Id, nil
}

// waitProxy waits until Caddy's admin API answers, or reports why it exited.
func waitProxy(ctx context.Context, c *dockerClient, id string) error {
	deadline := time.Now().Add(proxyReadyTimeout)
	for {
		if adminDo(ctx, http.MethodGet, "/config/", "", nil) == nil {
			return nil
		}
		in, err := c.inspect(ctx, id)
		if err == nil && in.State.Status != "running" && in.State.Status != "created" {
			msg := ""
			if tail, err := dockerContainerLogs(ctx, c, agenttask.ContainerLogsParams{ID: id, Lines: 20}); err == nil {
				for _, l := range tail.Lines {
					msg += l.Text + "\n"
				}
			}
			return fmt.Errorf("the proxy exited (%s): %s", in.State.Status, strings.TrimSpace(msg))
		}
		if time.Now().After(deadline) {
			return errors.New("the proxy did not become ready in time")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func proxyStatus(ctx context.Context, c *dockerClient) (agenttask.ProxyStatusResult, error) {
	res := agenttask.ProxyStatusResult{Certificates: []agenttask.Certificate{}}
	in, err := c.inspect(ctx, agenttask.ProxyContainer)
	var de *dockerError
	if errors.As(err, &de) && de.Status == http.StatusNotFound {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	res.Installed, res.State, res.Running, res.Image = true, in.State.Status, in.State.Status == "running", in.Image
	if b, err := os.ReadFile(filepath.Join(proxyDir, "applied.json")); err == nil {
		var cfg agenttask.ProxyConfig
		if json.Unmarshal(b, &cfg) == nil {
			res.Applied = &cfg
		}
	}
	res.Certificates = readCertificates(filepath.Join(proxyDir, "data", "caddy", "certificates"))
	return res, nil
}

// readCertificates lists the leaf certificates in Caddy's storage
// (certificates/<issuer>/<name>/<name>.crt).
func readCertificates(root string) []agenttask.Certificate {
	out := []agenttask.Certificate{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".crt") || len(out) >= 500 {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		block, _ := pem.Decode(b)
		if block == nil {
			return nil
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil
		}
		issuer := cert.Issuer.CommonName
		if issuer == "" && len(cert.Issuer.Organization) > 0 {
			issuer = cert.Issuer.Organization[0]
		}
		out = append(out, agenttask.Certificate{Domain: strings.TrimSuffix(d.Name(), ".crt"), Issuer: issuer,
			NotAfter: cert.NotAfter.UTC().Format(time.RFC3339)})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

func proxyDisable(ctx context.Context, c *dockerClient) (agenttask.ProxyStatusResult, error) {
	in, err := c.inspect(ctx, agenttask.ProxyContainer)
	var de *dockerError
	if errors.As(err, &de) && de.Status == http.StatusNotFound {
		return proxyStatus(ctx, c)
	}
	if err != nil {
		return agenttask.ProxyStatusResult{}, err
	}
	if in.Config.Labels[agenttask.ProxyLabel] != "true" {
		return agenttask.ProxyStatusResult{}, fmt.Errorf("a container named %s exists that OSRIVA did not create", agenttask.ProxyContainer)
	}
	// Certificates and settings stay in proxyDir for a later re-enable.
	if err := c.json(ctx, http.MethodDelete, "/containers/"+in.ID, url.Values{"force": {"1"}}, nil, nil); err != nil {
		return agenttask.ProxyStatusResult{}, err
	}
	return proxyStatus(ctx, c)
}

func proxyTaskRun(ctx context.Context, typ string, params json.RawMessage) (any, error) {
	c, err := newDocker()
	if err != nil {
		return nil, err
	}
	switch typ {
	case agenttask.ProxyApply:
		var cfg agenttask.ProxyConfig
		_ = json.Unmarshal(params, &cfg)
		return proxyApply(ctx, c, cfg)
	case agenttask.ProxyDisable:
		return proxyDisable(ctx, c)
	}
	return proxyStatus(ctx, c)
}
