package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/osriva/osriva-panel/services/api/internal/auth"
	"github.com/osriva/osriva-panel/services/api/internal/identity"
	"github.com/osriva/osriva-panel/services/api/internal/rbac"
	"github.com/osriva/osriva-panel/services/api/internal/secrets"
	"github.com/osriva/osriva-panel/services/api/internal/store/memstore"
)

const pw = "a long enough password"

type env struct {
	t   *testing.T
	h   http.Handler
	svc *identity.Service
	db  *fakePinger
}

type fakePinger struct{ err error }

func (f *fakePinger) Ping(context.Context) error { return f.err }

func newEnv(t *testing.T) *env {
	t.Helper()
	st := memstore.New()
	keys, _ := secrets.NewKeyring(bytes.Repeat([]byte{3}, 32))
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := identity.New(identity.Options{Store: st, Keys: keys, Log: quiet,
		Argon2: auth.Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncRBAC(context.Background()); err != nil {
		t.Fatal(err)
	}
	for email, role := range map[string]string{"owner@example.com": rbac.RoleOwner, "member@example.com": rbac.RoleMember} {
		if _, err := svc.BootstrapUser(context.Background(), email, "U", pw, role, "test"); err != nil {
			t.Fatal(err)
		}
	}
	db := &fakePinger{}
	return &env{t: t, svc: svc, db: db, h: New(Options{Identity: svc, DB: db, Log: quiet, Version: "test", CookieSecure: true})}
}

type client struct {
	e      *env
	cookie *http.Cookie
	csrf   string
}

func (e *env) do(method, path string, body any, c *client, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "192.0.2.1:5555"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		if c.cookie != nil {
			req.AddCookie(c.cookie)
		}
		if c.csrf != "" {
			req.Header.Set(CSRFHeader, c.csrf)
		}
	}
	for _, m := range mutate {
		m(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) login(email string) *client {
	e.t.Helper()
	rec := e.do("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": pw}, nil)
	if rec.Code != 200 {
		e.t.Fatalf("login %s: %d %s", email, rec.Code, rec.Body)
	}
	var body struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	var ck *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			ck = c
		}
	}
	if ck == nil || body.CSRF == "" {
		e.t.Fatal("login did not return cookie and csrf token")
	}
	return &client{e: e, cookie: ck, csrf: body.CSRF}
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b errorBody
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	return b.Error.Code
}

func TestLoginCookieAttributesAndMe(t *testing.T) {
	e := newEnv(t)
	c := e.login("owner@example.com")
	if !c.cookie.HttpOnly || !c.cookie.Secure || c.cookie.SameSite != http.SameSiteStrictMode || c.cookie.Path != "/" {
		t.Fatalf("insecure cookie: %+v", c.cookie)
	}
	rec := e.do("GET", "/api/v1/auth/me", nil, c)
	if rec.Code != 200 {
		t.Fatalf("me: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "argon2") || strings.Contains(rec.Body.String(), c.cookie.Value) {
		t.Fatal("me response leaks password hash or session token")
	}
	var me struct {
		Permissions []string `json:"permissions"`
		PanelMode   string   `json:"panel_mode"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if len(me.Permissions) != len(rbac.Catalog) || me.PanelMode != "personal" {
		t.Fatalf("me body: %+v", me)
	}
}

func TestUnauthenticatedAndBadLogin(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/api/v1/auth/me", nil, nil); rec.Code != 401 || errCode(t, rec) != "unauthenticated" {
		t.Fatalf("me without cookie: %d", rec.Code)
	}
	bogus := &client{cookie: &http.Cookie{Name: SessionCookie, Value: "nope"}}
	if rec := e.do("GET", "/api/v1/auth/me", nil, bogus); rec.Code != 401 {
		t.Fatalf("bogus cookie: %d", rec.Code)
	}
	rec := e.do("POST", "/api/v1/auth/login", map[string]string{"email": "owner@example.com", "password": "wrong password!!"}, nil)
	if rec.Code != 401 || errCode(t, rec) != "invalid_credentials" {
		t.Fatalf("bad login: %d %s", rec.Code, rec.Body)
	}
}

func TestCSRFRequiredForUnsafeMethods(t *testing.T) {
	e := newEnv(t)
	c := e.login("owner@example.com")
	noCSRF := &client{cookie: c.cookie}
	rec := e.do("POST", "/api/v1/auth/logout", nil, noCSRF)
	if rec.Code != 403 || errCode(t, rec) != "csrf_failed" {
		t.Fatalf("logout without csrf: %d", rec.Code)
	}
	wrong := &client{cookie: c.cookie, csrf: "forged"}
	if rec := e.do("POST", "/api/v1/auth/logout", nil, wrong); rec.Code != 403 {
		t.Fatalf("logout with forged csrf: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/auth/logout", nil, c); rec.Code != 204 {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/api/v1/auth/me", nil, c); rec.Code != 401 {
		t.Fatalf("session usable after logout: %d", rec.Code)
	}
}

func TestBodyValidation(t *testing.T) {
	e := newEnv(t)
	rec := e.do("POST", "/api/v1/auth/login", nil, nil, func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(`email=x&password=y`))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	})
	if rec.Code != 415 {
		t.Fatalf("form body: %d", rec.Code)
	}
	rec = e.do("POST", "/api/v1/auth/login", map[string]string{"email": "a", "password": "b", "admin": "true"}, nil)
	if rec.Code != 400 || errCode(t, rec) != "invalid_json" {
		t.Fatalf("unknown field: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("POST", "/api/v1/auth/login", nil, nil, func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(`{"email":"a","password":"b"}{"x":1}`))
		r.Header.Set("Content-Type", "application/json")
	})
	if rec.Code != 400 {
		t.Fatalf("trailing data: %d", rec.Code)
	}
	rec = e.do("POST", "/api/v1/auth/login", nil, nil, func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(`{"email":"` + strings.Repeat("a", maxBodyBytes) + `"}`))
		r.Header.Set("Content-Type", "application/json")
	})
	if rec.Code != 413 {
		t.Fatalf("oversized body: %d", rec.Code)
	}
}

func TestSecurityHeadersAndRequestID(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/api/v1/meta", nil, nil, func(r *http.Request) { r.Header.Set("X-Request-ID", "abc-123") })
	h := rec.Header()
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Cache-Control": "no-store",
		"X-Request-Id": "abc-123", "Strict-Transport-Security": "max-age=63072000; includeSubDomains",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	rec = e.do("GET", "/api/v1/meta", nil, nil, func(r *http.Request) { r.Header.Set("X-Request-ID", "bad id\nwith newline") })
	if id := rec.Header().Get("X-Request-ID"); id == "" || strings.ContainsAny(id, " \n") {
		t.Fatalf("unsafe request id echoed: %q", id)
	}
}

func TestPermissionsEnforced(t *testing.T) {
	e := newEnv(t)
	member := e.login("member@example.com")
	for _, path := range []string{"/api/v1/users", "/api/v1/audit-logs", "/api/v1/roles", "/api/v1/settings"} {
		if rec := e.do("GET", path, nil, member); rec.Code != 403 || errCode(t, rec) != "forbidden" {
			t.Errorf("member GET %s: %d", path, rec.Code)
		}
	}
	// Members can still manage their own account.
	if rec := e.do("GET", "/api/v1/auth/sessions", nil, member); rec.Code != 200 {
		t.Errorf("member sessions: %d", rec.Code)
	}
}

func TestUserManagementFlow(t *testing.T) {
	e := newEnv(t)
	owner := e.login("owner@example.com")
	rec := e.do("POST", "/api/v1/users", map[string]any{"email": "new@example.com", "display_name": "New", "password": pw, "roles": []string{"auditor"}}, owner)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var u userDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &u)
	if u.Email != "new@example.com" || len(u.Roles) != 1 || u.Roles[0].Key != "auditor" {
		t.Fatalf("created user: %+v", u)
	}
	rec = e.do("POST", "/api/v1/users", map[string]any{"email": "new@example.com", "display_name": "Dup", "password": pw}, owner)
	if rec.Code != 400 || errCode(t, rec) != "validation_failed" {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body)
	}
	// Login just happened, so the re-auth window is open.
	rec = e.do("PATCH", "/api/v1/users/"+u.ID, map[string]any{"status": "disabled"}, owner)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"disabled"`) {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/api/v1/users/"+auth.NewID(), nil, owner); rec.Code != 404 {
		t.Fatalf("missing user: %d", rec.Code)
	}
	rec = e.do("GET", "/api/v1/audit-logs?action=users.", nil, owner)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"users.create"`) || !strings.Contains(rec.Body.String(), `"users.update"`) {
		t.Fatalf("audit: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("GET", "/api/v1/users?q=new", nil, owner)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":1`) || strings.Contains(rec.Body.String(), "argon2") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
}

func TestReadyz(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/readyz", nil, nil); rec.Code != 200 {
		t.Fatalf("ready: %d", rec.Code)
	}
	e.db.err = errors.New("down")
	if rec := e.do("GET", "/readyz", nil, nil); rec.Code != 503 || strings.Contains(rec.Body.String(), "down") {
		t.Fatalf("not ready: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/api/v1/nope", nil, nil); rec.Code != 404 || errCode(t, rec) != "not_found" {
		t.Fatalf("unknown route: %d", rec.Code)
	}
}

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	mk := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	cases := []struct {
		r    *http.Request
		want string
	}{
		{mk("203.0.113.5:1"), "203.0.113.5"},
		{mk("203.0.113.5:1", "1.2.3.4"), "203.0.113.5"},                       // untrusted peer: header ignored
		{mk("10.0.0.2:1", "198.51.100.9"), "198.51.100.9"},                    // trusted proxy
		{mk("10.0.0.2:1", "6.6.6.6, 198.51.100.9, 10.0.0.3"), "198.51.100.9"}, // spoofed left-most entry ignored
		{mk("10.0.0.2:1", "garbage"), "10.0.0.2"},
		{mk("[::ffff:203.0.113.5]:1"), "203.0.113.5"},
	}
	for i, c := range cases {
		if got := clientIP(c.r, trusted); got != c.want {
			t.Errorf("case %d: got %s want %s", i, got, c.want)
		}
	}
}
