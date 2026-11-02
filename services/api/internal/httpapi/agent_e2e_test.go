package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agentclient"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/pki"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store/memstore"
)

type agentEnv struct {
	t        *testing.T
	fleet    *fleet.Service
	owner    *identity.Principal
	panelURL string
	svc      *identity.Service
}

func serve(t *testing.T, ln net.Listener, h http.Handler) {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// newAgentEnv runs the real panel API (plain HTTP on loopback) and the real
// mutual-TLS agent listener on ephemeral ports.
func newAgentEnv(t *testing.T) *agentEnv {
	t.Helper()
	ctx := context.Background()
	st := memstore.New()
	keys, _ := secrets.NewKeyring(bytes.Repeat([]byte{9}, 32))
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := identity.New(identity.Options{Store: st, Keys: keys, Log: quiet,
		Argon2: auth.Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncRBAC(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BootstrapUser(ctx, "owner@example.com", "Owner", pw, rbac.RoleOwner, "test"); err != nil {
		t.Fatal(err)
	}
	login, err := svc.Login(ctx, identity.LoginInput{Email: "owner@example.com", Password: pw})
	if err != nil {
		t.Fatal(err)
	}

	panelLn, _ := net.Listen("tcp", "127.0.0.1:0")
	agentLn, _ := net.Listen("tcp", "127.0.0.1:0")
	panelURL := "http://" + panelLn.Addr().String()
	fl, err := fleet.New(ctx, fleet.Options{Store: st, Identity: svc, Keys: keys, Log: quiet,
		PanelURL: panelURL, AgentURL: "https://" + agentLn.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Identity: svc, Fleet: fl, DB: st, Log: quiet}
	tlsCfg, err := fl.AgentTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	serve(t, panelLn, New(opts))
	serve(t, tls.NewListener(agentLn, tlsCfg), NewAgentHandler(opts))
	return &agentEnv{t: t, fleet: fl, owner: login.Principal, panelURL: panelURL, svc: svc}
}

func (e *agentEnv) addServer(name string) *fleet.Enrollment {
	e.t.Helper()
	_, enr, err := e.fleet.CreateServer(context.Background(), e.owner, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return enr
}

func (e *agentEnv) enroll(enr *fleet.Enrollment) (*agentclient.EnrollResult, *agentclient.Client) {
	e.t.Helper()
	res, err := agentclient.Enroll(context.Background(), enr.PanelURL, enr.Token, enr.CAFingerprint, "test-agent", nil)
	if err != nil {
		e.t.Fatalf("enroll: %v", err)
	}
	c, err := agentclient.New(res.Response.AgentEndpoint, []byte(res.Response.CAPEM), []byte(res.Response.CertificatePEM), res.KeyPEM)
	if err != nil {
		e.t.Fatal(err)
	}
	return res, c
}

func apiCode(err error) string {
	var ae *agentclient.APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestAgentEnrolReportAndInventory(t *testing.T) {
	e := newAgentEnv(t)
	enr := e.addServer("web-1")
	res, client := e.enroll(enr)

	// A real inventory of the machine running the test.
	if _, err := client.Report(context.Background(), agentproto.ReportRequest{AgentVersion: "test-agent", Inventory: inventory.Collect()}); err != nil {
		t.Fatalf("report: %v", err)
	}
	d, err := e.fleet.Get(context.Background(), e.owner, res.Response.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Connection != fleet.ConnOnline || d.Inventory == nil || d.Server.Facts.AgentVersion != "test-agent" || d.Server.LastSeenIP != "127.0.0.1" {
		t.Fatalf("after report: conn=%s inv=%v facts=%+v ip=%q", d.Connection, d.Inventory != nil, d.Server.Facts, d.Server.LastSeenIP)
	}
	if len(d.Capabilities) == 0 || d.Capabilities[0].Key != "agent.inventory" {
		t.Fatalf("capabilities not computed: %+v", d.Capabilities)
	}

	// The token is single use.
	_, err = agentclient.Enroll(context.Background(), enr.PanelURL, enr.Token, enr.CAFingerprint, "x", nil)
	if apiCode(err) != "invalid_enrollment_token" {
		t.Fatalf("token replay: %v", err)
	}

	// Reports without a schema version are rejected, not silently stored.
	if _, err := client.Report(context.Background(), agentproto.ReportRequest{Inventory: &inventory.Inventory{}}); apiCode(err) != "validation_failed" {
		t.Fatalf("bad schema: %v", err)
	}
}

func TestWrongFingerprintNeverSendsToken(t *testing.T) {
	e := newAgentEnv(t)
	enr := e.addServer("db-1")
	wrong := "sha256:" + string(bytes.Repeat([]byte("0"), 64))
	_, err := agentclient.Enroll(context.Background(), enr.PanelURL, enr.Token, wrong, "x", nil)
	if err == nil || apiCode(err) != "" {
		t.Fatalf("expected a local fingerprint error, got %v", err)
	}
	// The token was not consumed, so enrolment with the right fingerprint still works.
	e.enroll(enr)
}

func TestRenewAndRevoke(t *testing.T) {
	e := newAgentEnv(t)
	res, client := e.enroll(e.addServer("app-1"))
	ctx := context.Background()

	certPEM, keyPEM, err := client.Renew(ctx)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	renewed, err := agentclient.New(res.Response.AgentEndpoint, []byte(res.Response.CAPEM), certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	inv := inventory.Collect()
	if _, err := renewed.Report(ctx, agentproto.ReportRequest{Inventory: inv}); err != nil {
		t.Fatalf("report with renewed cert: %v", err)
	}

	if err := e.fleet.Revoke(ctx, e.owner, res.Response.ServerID); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*agentclient.Client{"original": client, "renewed": renewed} {
		if _, err := c.Report(ctx, agentproto.ReportRequest{Inventory: inv}); apiCode(err) != "agent_forbidden" {
			t.Errorf("%s cert after revoke: %v", name, err)
		}
	}
	d, _ := e.fleet.Get(ctx, e.owner, res.Response.ServerID)
	if d.Connection != fleet.ConnRevoked {
		t.Fatalf("connection after revoke: %s", d.Connection)
	}
}

func TestAgentListenerRejectsUntrustedClients(t *testing.T) {
	e := newAgentEnv(t)
	res, _ := e.enroll(e.addServer("edge-1"))
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(res.Response.CAPEM))
	url := res.Response.AgentEndpoint + agentproto.ReportPath

	// No client certificate: the TLS handshake fails.
	noCert := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}}
	if resp, err := noCert.Post(url, "application/json", bytes.NewReader([]byte(`{}`))); err == nil {
		resp.Body.Close()
		t.Fatalf("request without client cert got HTTP %d", resp.StatusCode)
	}

	// A certificate from a different CA, even with the right server ID as CN, is refused.
	rogueCA, _ := pki.NewCA(time.Now())
	keyPEM, csrPEM, _ := agentclient.NewKey()
	_, rogueCert, err := rogueCA.SignClientCSR(csrPEM, res.Response.ServerID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, _ := tls.X509KeyPair(rogueCert, keyPEM)
	rogue := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: pool, Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}}}
	if resp, err := rogue.Post(url, "application/json", bytes.NewReader([]byte(`{}`))); err == nil {
		resp.Body.Close()
		t.Fatalf("rogue CA cert got HTTP %d", resp.StatusCode)
	}
}

func TestEnrolmentRequiresConfiguredEndpoint(t *testing.T) {
	st := memstore.New()
	keys, _ := secrets.NewKeyring(bytes.Repeat([]byte{9}, 32))
	svc, _ := identity.New(identity.Options{Store: st, Keys: keys,
		Argon2: auth.Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}})
	_ = svc.SyncRBAC(context.Background())
	_, _ = svc.BootstrapUser(context.Background(), "o@example.com", "O", pw, rbac.RoleOwner, "test")
	login, _ := svc.Login(context.Background(), identity.LoginInput{Email: "o@example.com", Password: pw})
	fl, err := fleet.New(context.Background(), fleet.Options{Store: st, Identity: svc, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	var pe *identity.PolicyError
	if _, _, err := fl.CreateServer(context.Background(), login.Principal, "x"); !errors.As(err, &pe) {
		t.Fatalf("create without agent endpoint: %v", err)
	}
}
