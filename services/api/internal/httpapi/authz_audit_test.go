package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/customer"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/provisioning"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/ratelimit"
)

// publicRoutes are the only operator-side routes reachable without a session.
var publicRoutes = map[string]bool{
	"GET /healthz": true, "GET /readyz": true, "GET /api/v1/meta": true, "GET /api/v1/openapi.yaml": true,
	"POST /api/v1/auth/login": true, "GET /api/v1/agent/ca": true, "GET /api/v1/agent/downloads": true,
	"GET /api/v1/agent/download/{os}/{arch}": true, "GET /api/v1/agent/download/{os}/{arch}/sha256": true,
	"GET /api/v1/agent/install.sh": true, "GET /api/v1/agent/install.ps1": true, "POST /api/v1/agent/enroll": true,
	"POST /api/v1/customer/auth/login": true, "POST /api/v1/customer/auth/setup": true,
}

var paramRe = regexp.MustCompile(`\{[^}]+\}`)

// TestEveryRouteRequiresItsAuthentication is the systematic part of the Phase 14
// audit: it walks the router and checks every route, so a new endpoint cannot
// silently be added without authentication or CSRF protection.
func TestEveryRouteRequiresItsAuthentication(t *testing.T) {
	e := newEnv(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cs := customer.New(customer.Options{Store: e.store, Identity: e.svc, Keys: e.keys, Log: quiet})
	billing := provisioning.New(e.store, e.svc, nil, e.keys, quiet)
	e.h = New(Options{Identity: e.svc, Customers: cs, Provisioning: billing, DB: e.db, Log: quiet, Version: "test", CookieSecure: true,
		APILimiter: ratelimit.NewKeyed(1e6, 1e6)}) // hundreds of requests from one address
	c := e.login("owner@example.com")

	type route struct{ method, pattern string }
	var routes []route
	_ = chi.Walk(e.h.(chi.Routes), func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method != http.MethodHead && method != http.MethodOptions {
			routes = append(routes, route{method, strings.TrimSuffix(pattern, "/")})
		}
		return nil
	})
	if len(routes) < 100 {
		t.Fatalf("walked only %d routes", len(routes))
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].pattern+routes[i].method < routes[j].pattern+routes[j].method })

	var problems []string
	checked := map[string]int{}
	for _, r := range routes {
		key := r.method + " " + r.pattern
		path := paramRe.ReplaceAllString(r.pattern, "00000000-0000-4000-8000-0000000000aa")
		send := func(withSession bool) *httptest.ResponseRecorder {
			req := httptest.NewRequest(r.method, path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "192.0.2.10:1234"
			if withSession {
				req.AddCookie(c.cookie) // operator session, deliberately without the CSRF header
			}
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			return rec
		}
		switch {
		case publicRoutes[key]:
			checked["public"]++
		case strings.HasPrefix(r.pattern, "/api/v1/provisioning/"):
			if rec := send(true); rec.Code != http.StatusUnauthorized {
				problems = append(problems, key+": unsigned request (even with an operator cookie) got "+rec.Result().Status)
			}
			checked["signed"]++
		case strings.HasPrefix(r.pattern, "/api/v1/customer/"):
			if rec := send(true); rec.Code != http.StatusUnauthorized {
				problems = append(problems, key+": an operator session reached the customer API: "+rec.Result().Status)
			}
			checked["customer"]++
		case strings.HasPrefix(r.pattern, "/api/v1/"):
			if rec := send(false); rec.Code != http.StatusUnauthorized {
				problems = append(problems, key+": no session got "+rec.Result().Status)
			}
			if r.method != http.MethodGet {
				if rec := send(true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "csrf_failed") {
					problems = append(problems, key+": a session without the CSRF token got "+rec.Result().Status)
				}
			}
			checked["operator"]++
		default:
			problems = append(problems, key+": route outside /api/v1 and not in the public list")
		}
	}
	if len(problems) > 0 {
		t.Fatalf("authentication gaps:\n%s", strings.Join(problems, "\n"))
	}
	t.Logf("checked routes: %v", checked)
}
