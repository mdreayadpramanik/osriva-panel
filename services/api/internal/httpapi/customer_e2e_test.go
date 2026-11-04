package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/pkg/inventory"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/customer"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/provider"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

type browser struct {
	t    *testing.T
	base string
	hc   *http.Client
	csrf string
}

func newBrowser(t *testing.T, base string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: base, hc: &http.Client{Jar: jar}}
}

func (b *browser) do(method, path string, body any) (int, map[string]any, *http.Response) {
	b.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, b.base+path, bytes.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.csrf != "" {
		req.Header.Set(CSRFHeader, b.csrf)
	}
	resp, err := b.hc.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out, resp
}

func TestCustomerPanelIsolation(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	res, agent := e.enroll(e.addServer("kvm-c"))
	sid := res.Response.ServerID
	inv, _ := json.Marshal(inventory.Inventory{SchemaVersion: inventory.SchemaVersion, OS: inventory.OSInfo{Family: "linux"},
		CPU: inventory.CPUInfo{LogicalCores: 4}, Memory: inventory.Memory{TotalBytes: 8 << 30}})
	_ = e.store().RecordReport(ctx, sid, store.ServerFacts{OSFamily: "linux"}, "127.0.0.1", time.Now(), &store.ServerInventory{CollectedAt: time.Now(), Data: inv, Capabilities: []byte("[]")})
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	keys, _ := secrets.NewKeyring(bytes.Repeat([]byte{9}, 32))
	prov := provider.New(e.store(), e.svc, e.fleet, quiet)
	cs := customer.New(customer.Options{Store: e.store(), Identity: e.svc, Keys: keys, Provider: prov, Log: quiet})
	srv := httptest.NewServer(New(Options{Identity: e.svc, Fleet: e.fleet, Provider: prov, Customers: cs, DB: e.store(), Log: quiet}))
	defer srv.Close()

	// Infrastructure and three VPS: Alice's, Bob's and one without a customer.
	if err := e.svc.SetPanelMode(ctx, e.owner, identity.ModeProvider); err != nil {
		t.Fatal(err)
	}
	dc, _ := prov.CreateDatacenter(ctx, e.owner, provider.DatacenterInput{Name: "dc"})
	node, _ := prov.CreateNode(ctx, e.owner, provider.NodeInput{ServerID: sid, DatacenterID: dc.ID})
	pool, _ := prov.CreatePool(ctx, e.owner, provider.PoolInput{Name: "p", CIDR: "10.8.0.0/24", Gateway: "10.8.0.1", First: "10.8.0.10", Last: "10.8.0.20"})
	tpl, _ := prov.CreateTemplate(ctx, e.owner, provider.TemplateInput{Name: "u", ImageURL: "https://images.example.com/u.img",
		ImageSHA256: strings.Repeat("cd", 32), MinDiskGB: 5, DefaultUser: "ubuntu"})
	mk := func(name string) *store.VPS {
		v, err := prov.CreateVPS(ctx, e.owner, provider.VPSInput{Name: name, NodeID: node.Node.ID, TemplateID: tpl.ID, PoolID: pool.Pool.ID,
			VCPUs: 1, MemoryMB: 512, DiskGB: 5, SSHKeys: []string{providerKey}})
		if err != nil {
			t.Fatal(err)
		}
		answer(t, agent, agenttask.VMCreate, ok("running"))
		got, _ := prov.GetVPS(ctx, e.owner, v.ID)
		return got
	}
	va, vb, vn := mk("alice-web"), mk("bob-db"), mk("ops-only")

	alice, err := cs.Create(ctx, e.owner, customer.Input{Email: " Alice@Example.com ", Name: "Alice"})
	if err != nil || alice.Customer.Email != "alice@example.com" || alice.SetupToken == "" {
		t.Fatalf("create: %+v %v", alice, err)
	}
	bob, _ := cs.Create(ctx, e.owner, customer.Input{Email: "bob@example.com", Name: "Bob"})
	auditor := e.principal("auditor-cu@example.com", "auditor")
	if _, err := cs.Create(ctx, auditor, customer.Input{Email: "x@example.com", Name: "X"}); err == nil {
		t.Fatal("an auditor created a customer")
	}
	if _, err := prov.AssignCustomer(ctx, e.owner, va.ID, &alice.Customer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := prov.AssignCustomer(ctx, e.owner, vb.ID, &bob.Customer.ID); err != nil {
		t.Fatal(err)
	}

	a := newBrowser(t, srv.URL)
	if code, m, _ := a.do("POST", "/api/v1/customer/auth/login", map[string]string{"email": "alice@example.com", "password": "anything at all 1"}); code != 401 {
		t.Fatalf("login before setup: %d %v", code, m)
	}
	if code, _, _ := a.do("POST", "/api/v1/customer/auth/setup", map[string]string{"token": alice.SetupToken, "password": "short"}); code != 400 {
		t.Fatalf("weak password: %d", code)
	}
	if code, _, _ := a.do("POST", "/api/v1/customer/auth/setup", map[string]string{"token": alice.SetupToken, "password": "alice's long password"}); code != 204 {
		t.Fatalf("setup: %d", code)
	}
	if code, _, _ := a.do("POST", "/api/v1/customer/auth/setup", map[string]string{"token": alice.SetupToken, "password": "another long password"}); code != 409 {
		t.Fatalf("a setup link works once: %d", code)
	}
	code, m, resp := a.do("POST", "/api/v1/customer/auth/login", map[string]string{"email": "ALICE@example.com", "password": "alice's long password"})
	if code != 200 || m["csrf_token"] == "" {
		t.Fatalf("login: %d %v", code, m)
	}
	setCookie := resp.Header.Get("Set-Cookie")
	if !strings.Contains(setCookie, "osriva_customer=") || !strings.Contains(setCookie, "Path=/api/v1/customer") || !strings.Contains(setCookie, "HttpOnly") ||
		!strings.Contains(setCookie, "SameSite=Strict") {
		t.Fatalf("customer cookie: %s", setCookie)
	}
	token := strings.TrimPrefix(strings.Split(setCookie, ";")[0], "osriva_customer=")

	// Alice sees exactly her VPS, without operator details.
	code, m, _ = a.do("GET", "/api/v1/customer/vps", nil)
	list, _ := m["vps"].([]any)
	if code != 200 || len(list) != 1 {
		t.Fatalf("alice's list: %d %v", code, m)
	}
	first := list[0].(map[string]any)
	if first["id"] != va.ID || first["ip_address"] != va.IPAddress {
		t.Fatalf("alice's VPS: %v", first)
	}
	for _, k := range []string{"node_id", "pending_task_id", "last_error", "pool_id", "template_id"} {
		if _, leaked := first[k]; leaked {
			t.Errorf("customer view leaks %s", k)
		}
	}
	for _, id := range []string{vb.ID, vn.ID, auth.NewID()} {
		if code, _, _ := a.do("GET", "/api/v1/customer/vps/"+id, nil); code != 404 {
			t.Fatalf("alice reached VPS %s: %d", id, code)
		}
	}

	// Unsafe requests need the customer CSRF token.
	if code, m, _ := a.do("POST", "/api/v1/customer/vps/"+va.ID+"/actions", map[string]string{"action": "reboot"}); code != 403 || errCodeOf(m) != "csrf_failed" {
		t.Fatalf("without CSRF: %d %v", code, m)
	}
	_, me, _ := a.do("GET", "/api/v1/customer/auth/me", nil)
	a.csrf = me["csrf_token"].(string)
	for _, id := range []string{vb.ID, vn.ID} {
		if code, _, _ := a.do("POST", "/api/v1/customer/vps/"+id+"/actions", map[string]string{"action": "shutdown"}); code != 404 {
			t.Fatalf("alice acted on VPS %s: %d", id, code)
		}
	}
	if code, _, _ := a.do("POST", "/api/v1/customer/vps/"+va.ID+"/actions", map[string]string{"action": "stop"}); code != 400 {
		t.Fatalf("force stop is not for customers: %d", code)
	}
	if code, m, _ := a.do("POST", "/api/v1/customer/vps/"+va.ID+"/actions", map[string]string{"action": "shutdown"}); code != 202 || m["pending"] != "shutdown" {
		t.Fatalf("shutdown: %d %v", code, m)
	}
	params := answer(t, agent, agenttask.VMAction, ok("stopped"))
	if !strings.Contains(string(params), va.ID) || !strings.Contains(string(params), `"shutdown"`) {
		t.Fatalf("task params: %s", params)
	}
	if _, m, _ := a.do("GET", "/api/v1/customer/vps/"+va.ID, nil); m["state"] != "stopped" {
		t.Fatalf("after shutdown: %v", m)
	}

	// A suspended billing service cannot be started by its customer.
	client := &store.APIClient{ID: auth.NewID(), OrganizationID: store.DefaultOrganizationID, Name: "b", KeyID: "osk_t", SecretEnc: []byte{1},
		Scopes: []string{"provisioning.write"}, Enabled: true, CreatedBy: e.owner.User.ID}
	_ = e.store().CreateAPIClient(ctx, client)
	_ = e.store().CreateProvisionedService(ctx, &store.ProvisionedService{ID: auth.NewID(), OrganizationID: store.DefaultOrganizationID,
		ClientID: client.ID, ExternalID: "x1", VPSID: &va.ID, Status: "suspended"})
	if code, m, _ := a.do("POST", "/api/v1/customer/vps/"+va.ID+"/actions", map[string]string{"action": "start"}); code != 409 {
		t.Fatalf("starting a suspended service: %d %v", code, m)
	}

	// Cookies never cross: a customer session is no operator session and vice versa.
	opLogin, _ := e.svc.Login(ctx, identity.LoginInput{Email: "owner@example.com", Password: pw})
	cross := func(path, cookie, value string) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.AddCookie(&http.Cookie{Name: cookie, Value: value})
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if code := cross("/api/v1/servers", CustomerCookie, token); code != 401 {
		t.Fatalf("customer cookie on an operator endpoint: %d", code)
	}
	if code := cross("/api/v1/servers", SessionCookie, token); code != 401 {
		t.Fatalf("customer token as an operator session: %d", code)
	}
	if code := cross("/api/v1/customer/vps", SessionCookie, opLogin.Token); code != 401 {
		t.Fatalf("operator cookie on the customer API: %d", code)
	}
	if code := cross("/api/v1/customer/vps", CustomerCookie, opLogin.Token); code != 401 {
		t.Fatalf("operator token as a customer session: %d", code)
	}

	// Password change signs out the other sessions; disabling signs out everywhere.
	a2 := newBrowser(t, srv.URL)
	if code, _, _ := a2.do("POST", "/api/v1/customer/auth/login", map[string]string{"email": "alice@example.com", "password": "alice's long password"}); code != 200 {
		t.Fatal("second login")
	}
	if code, _, _ := a.do("POST", "/api/v1/customer/auth/password", map[string]string{"current_password": "alice's long password", "new_password": "a brand new long password"}); code != 204 {
		t.Fatalf("change password: %d", code)
	}
	if code, _, _ := a2.do("GET", "/api/v1/customer/vps", nil); code != 401 {
		t.Fatalf("other session after a password change: %d", code)
	}
	if code, _, _ := a.do("GET", "/api/v1/customer/vps", nil); code != 200 {
		t.Fatalf("current session after a password change: %d", code)
	}
	if _, err := cs.SetEnabled(ctx, e.owner, alice.Customer.ID, false); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := a.do("GET", "/api/v1/customer/vps", nil); code != 401 {
		t.Fatalf("disabled customer: %d", code)
	}

	audit, _ := e.store().ListAudit(ctx, store.AuditFilter{OrganizationID: store.DefaultOrganizationID, ActionPrefix: "customer.vps_action", Limit: 10})
	if len(audit) != 1 || audit[0].Metadata["customer"] != "alice@example.com" || audit[0].ActorUserID != nil {
		t.Fatalf("audit: %+v", audit)
	}
}

func errCodeOf(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		c, _ := e["code"].(string)
		return c
	}
	return ""
}
