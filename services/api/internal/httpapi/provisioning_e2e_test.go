package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/jobs"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/monitor"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/provider"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/provisioning"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

type signer struct {
	t           *testing.T
	base        string
	key, secret string
}

func nonce() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// do sends a signed request; mut can change headers after signing.
func (c signer) do(method, path string, body any, idem string, mut ...func(*http.Request)) (*http.Response, map[string]any) {
	c.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, c.base+path, bytes.NewReader(raw))
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	n := nonce()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Osriva-Key", c.key)
	req.Header.Set("X-Osriva-Timestamp", ts)
	req.Header.Set("X-Osriva-Nonce", n)
	req.Header.Set("X-Osriva-Signature", provisioning.SignRequest(c.secret, method, req.URL.RequestURI(), ts, n, raw))
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	for _, m := range mut {
		m(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	if out == nil {
		out = map[string]any{"_raw": string(b)}
	}
	return resp, out
}

func errorCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		c, _ := e["code"].(string)
		return c
	}
	return ""
}

func TestProvisioningAPI(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	res, agent := e.enroll(e.addServer("kvm-p"))
	sid := res.Response.ServerID
	inv, _ := json.Marshal(inventory.Inventory{SchemaVersion: inventory.SchemaVersion, OS: inventory.OSInfo{Family: "linux"},
		CPU: inventory.CPUInfo{LogicalCores: 4}, Memory: inventory.Memory{TotalBytes: 8 << 30}})
	_ = e.store().RecordReport(ctx, sid, store.ServerFacts{OSFamily: "linux"}, "127.0.0.1", time.Now(), &store.ServerInventory{CollectedAt: time.Now(), Data: inv, Capabilities: []byte("[]")})
	keys, _ := secrets.NewKeyring(bytes.Repeat([]byte{9}, 32))
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	prov := provider.New(e.store(), e.svc, e.fleet, quiet)
	billing := provisioning.New(e.store(), e.svc, prov, keys, quiet)
	q := jobs.New(e.store(), quiet, nil)
	billing.UseQueue(q)
	srv := httptest.NewServer(New(Options{Identity: e.svc, Fleet: e.fleet, Provider: prov, Provisioning: billing, DB: e.store(), Log: quiet}))
	defer srv.Close()

	if err := e.svc.SetPanelMode(ctx, e.owner, identity.ModeProvider); err != nil {
		t.Fatal(err)
	}
	dc, _ := prov.CreateDatacenter(ctx, e.owner, provider.DatacenterInput{Name: "dc"})
	if _, err := prov.CreateNode(ctx, e.owner, provider.NodeInput{ServerID: sid, DatacenterID: dc.ID}); err != nil {
		t.Fatal(err)
	}
	pool, _ := prov.CreatePool(ctx, e.owner, provider.PoolInput{Name: "p", CIDR: "10.9.0.0/24", Gateway: "10.9.0.1", First: "10.9.0.10", Last: "10.9.0.20"})
	tpl, _ := prov.CreateTemplate(ctx, e.owner, provider.TemplateInput{Name: "u", ImageURL: "https://images.example.com/u.img",
		ImageSHA256: strings.Repeat("ab", 32), MinDiskGB: 5, DefaultUser: "ubuntu"})
	plan, err := billing.CreatePlan(ctx, e.owner, provisioning.PlanInput{Name: "vps-small", TemplateID: tpl.ID, PoolID: pool.Pool.ID, VCPUs: 1, MemoryMB: 1024, DiskGB: 10})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var hooks []map[string]string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hooks = append(hooks, map[string]string{"event": r.Header.Get("X-Osriva-Event"), "ts": r.Header.Get("X-Osriva-Timestamp"),
			"sig": r.Header.Get("X-Osriva-Signature"), "body": string(b)})
		mu.Unlock()
	}))
	defer hook.Close()

	auditor := e.principal("auditor-prov@example.com", "auditor")
	if _, err := billing.CreateClient(ctx, auditor, provisioning.ClientInput{Name: "x", Scopes: []string{provisioning.ScopeRead}}); err == nil {
		t.Fatal("an auditor created an API client")
	}
	created, err := billing.CreateClient(ctx, e.owner, provisioning.ClientInput{Name: "whmcs", Scopes: []string{provisioning.ScopeRead, provisioning.ScopeWrite},
		WebhookURL: hook.URL + "/osriva"})
	if err != nil || len(created.Secret) != 64 || !strings.HasPrefix(created.KeyID, "osk_") {
		t.Fatalf("client: %+v %v", created, err)
	}
	c := signer{t: t, base: srv.URL, key: created.KeyID, secret: created.Secret}

	// Authentication failures.
	resp, _ := http.Get(srv.URL + "/api/v1/provisioning/plans")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned request: %d", resp.StatusCode)
	}
	if r, m := c.do("GET", "/api/v1/provisioning/plans", nil, "", func(r *http.Request) { r.Header.Set("X-Osriva-Signature", "sha256=00") }); r.StatusCode != 401 || errorCode(m) != "invalid_signature" {
		t.Fatalf("bad signature: %d %v", r.StatusCode, m)
	}
	old := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	if r, _ := c.do("GET", "/api/v1/provisioning/plans", nil, "", func(r *http.Request) {
		r.Header.Set("X-Osriva-Timestamp", old)
		r.Header.Set("X-Osriva-Signature", provisioning.SignRequest(c.secret, "GET", "/api/v1/provisioning/plans", old, r.Header.Get("X-Osriva-Nonce"), nil))
	}); r.StatusCode != 401 {
		t.Fatalf("old timestamp: %d", r.StatusCode)
	}
	var replay *http.Request
	if r, m := c.do("GET", "/api/v1/provisioning/plans", nil, "", func(r *http.Request) { replay = r.Clone(context.Background()) }); r.StatusCode != 200 || len(m["plans"].([]any)) != 1 {
		t.Fatalf("plans: %d %v", r.StatusCode, m)
	}
	replay.Body = io.NopCloser(bytes.NewReader(nil))
	if r, _ := http.DefaultClient.Do(replay); r.StatusCode != 401 {
		t.Fatalf("replayed request accepted: %d", r.StatusCode)
	}
	if r, m := c.do("GET", "/api/v1/provisioning/plans?x=1", nil, "", func(r *http.Request) { r.URL.RawQuery = "x=2" }); r.StatusCode != 401 {
		t.Fatalf("tampered query: %d %v", r.StatusCode, m)
	}

	in := map[string]any{"plan_id": plan.ID, "external_id": "inv-1001", "hostname": "cust-1", "ssh_keys": []string{providerKey}}
	if r, m := c.do("POST", "/api/v1/provisioning/services", in, ""); r.StatusCode != 400 || errorCode(m) != "idempotency_key_required" {
		t.Fatalf("POST without Idempotency-Key: %d %v", r.StatusCode, m)
	}
	r1, s1 := c.do("POST", "/api/v1/provisioning/services", in, "create-inv-1001")
	if r1.StatusCode != 202 || s1["status"] != "provisioning" || s1["ip_address"] != "10.9.0.10" || s1["hostname"] != "cust-1" {
		t.Fatalf("create: %d %v", r1.StatusCode, s1)
	}
	r2, s2 := c.do("POST", "/api/v1/provisioning/services", in, "create-inv-1001")
	if r2.StatusCode != 202 || r2.Header.Get("Idempotency-Replayed") != "true" || s2["id"] != s1["id"] {
		t.Fatalf("idempotent replay: %d %v", r2.StatusCode, s2)
	}
	other := map[string]any{"plan_id": plan.ID, "external_id": "inv-1001", "hostname": "cust-x", "ssh_keys": []string{providerKey}}
	if r, m := c.do("POST", "/api/v1/provisioning/services", other, "create-inv-1001"); r.StatusCode != 422 || errorCode(m) != "idempotency_mismatch" {
		t.Fatalf("same key, other body: %d %v", r.StatusCode, m)
	}
	if r, m := c.do("POST", "/api/v1/provisioning/services", in, "create-inv-1001-retry"); r.StatusCode != 200 || m["id"] != s1["id"] {
		t.Fatalf("same external id, new key: %d %v", r.StatusCode, m)
	}
	if vpsList, _ := prov.ListVPS(ctx, e.owner); len(vpsList) != 1 {
		t.Fatalf("exactly one VPS: %d", len(vpsList))
	}

	id := s1["id"].(string)
	answer(t, agent, agenttask.VMCreate, ok("running"))
	billing.Reconcile(ctx)
	if _, m := c.do("GET", "/api/v1/provisioning/services/"+id, nil, ""); m["status"] != "active" {
		t.Fatalf("after create: %v", m)
	}
	if _, m := c.do("GET", "/api/v1/provisioning/services?external_id=inv-1001", nil, ""); m["id"] != id {
		t.Fatalf("by external id: %v", m)
	}
	q.RunOnce(ctx)

	// Suspend, unsuspend, terminate.
	if r, m := c.do("POST", "/api/v1/provisioning/services/"+id+"/actions", map[string]string{"action": "unsuspend"}, "unsusp-early"); r.StatusCode != 409 {
		t.Fatalf("unsuspend an active service: %d %v", r.StatusCode, m)
	}
	if r, m := c.do("POST", "/api/v1/provisioning/services/"+id+"/actions", map[string]string{"action": "suspend"}, "suspend-0001"); r.StatusCode != 202 || m["pending"] != "suspend" {
		t.Fatalf("suspend: %d %v", r.StatusCode, m)
	}
	answer(t, agent, agenttask.VMAction, ok("stopped"))
	billing.Reconcile(ctx)
	if _, m := c.do("GET", "/api/v1/provisioning/services/"+id, nil, ""); m["status"] != "suspended" {
		t.Fatalf("after suspend: %v", m)
	}
	c.do("POST", "/api/v1/provisioning/services/"+id+"/actions", map[string]string{"action": "unsuspend"}, "unsuspend-0001")
	answer(t, agent, agenttask.VMAction, ok("running"))
	billing.Reconcile(ctx)
	if _, m := c.do("GET", "/api/v1/provisioning/services/"+id, nil, ""); m["status"] != "active" {
		t.Fatalf("after unsuspend: %v", m)
	}

	// A read-only client cannot change anything and cannot see other clients' services.
	ro, _ := billing.CreateClient(ctx, e.owner, provisioning.ClientInput{Name: "reporting", Scopes: []string{provisioning.ScopeRead}})
	rc := signer{t: t, base: srv.URL, key: ro.KeyID, secret: ro.Secret}
	if r, _ := rc.do("POST", "/api/v1/provisioning/services", in, "ro-create-01"); r.StatusCode != 403 {
		t.Fatalf("read-only client created a service: %d", r.StatusCode)
	}
	if r, _ := rc.do("GET", "/api/v1/provisioning/services/"+id, nil, ""); r.StatusCode != 404 {
		t.Fatalf("another client's service is visible: %d", r.StatusCode)
	}

	if r, m := c.do("DELETE", "/api/v1/provisioning/services/"+id, nil, "terminate-0001"); r.StatusCode != 202 || m["status"] != "terminating" {
		t.Fatalf("terminate: %d %v", r.StatusCode, m)
	}
	answer(t, agent, agenttask.VMDelete, ok("deleted"))
	billing.Reconcile(ctx)
	if _, m := c.do("GET", "/api/v1/provisioning/services/"+id, nil, ""); m["status"] != "terminated" || m["vps_id"] != nil {
		t.Fatalf("after terminate: %v", m)
	}
	if pools, _ := prov.ListPools(ctx, e.owner); len(pools[0].Allocated) != 0 {
		t.Fatalf("address not released: %v", pools[0].Allocated)
	}
	for q.RunOnce(ctx) > 0 {
	}

	mu.Lock()
	events := []string{}
	for _, h := range hooks {
		events = append(events, h["event"])
		if h["sig"] != monitor.Sign(created.Secret, h["ts"], []byte(h["body"])) {
			t.Fatalf("webhook signature does not verify: %v", h)
		}
	}
	mu.Unlock()
	// Deliveries run in parallel and may arrive in any order; each carries its event time.
	slices.Sort(events)
	if strings.Join(events, ",") != "service.active,service.suspended,service.terminated,service.unsuspended" {
		t.Fatalf("webhooks: %v", events)
	}

	audit, _ := e.store().ListAudit(ctx, store.AuditFilter{OrganizationID: store.DefaultOrganizationID, ActionPrefix: "provisioning.service_", Limit: 50})
	if len(audit) < 4 || audit[0].Metadata["api_client"] != created.ID || audit[0].ActorUserID == nil {
		t.Fatalf("audit attribution: %+v", audit)
	}

	// Disabling the client locks it out at once.
	if _, err := billing.SetClientEnabled(ctx, e.owner, created.ID, false); err != nil {
		t.Fatal(err)
	}
	if r, _ := c.do("GET", "/api/v1/provisioning/plans", nil, ""); r.StatusCode != 401 {
		t.Fatalf("disabled client: %d", r.StatusCode)
	}
}
