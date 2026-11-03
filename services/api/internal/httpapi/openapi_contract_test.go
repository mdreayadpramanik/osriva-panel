package httpapi

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/mdreayadpramanik/osriva-panel/services/api/openapi"
)

// TestOpenAPIDocumentsEveryRoute keeps the specification in step with the router:
// every /api/v1 route must be documented with its method, and the YAML must parse.
func TestOpenAPIDocumentsEveryRoute(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(openapi.Spec, &spec); err != nil {
		t.Fatalf("openapi.yaml does not parse: %v", err)
	}
	e := newEnv(t)
	var missing []string
	seen := 0
	for _, h := range []http.Handler{e.h, NewAgentHandler(Options{Identity: e.svc, DB: e.db})} {
		routes, ok := h.(chi.Routes)
		if !ok {
			t.Fatalf("handler %T is not a chi router", h)
		}
		err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			path, ok := strings.CutPrefix(route, "/api/v1")
			if !ok || method == http.MethodOptions || method == http.MethodHead {
				return nil
			}
			path = strings.TrimSuffix(path, "/")
			seen++
			if ops, ok := spec.Paths[path]; !ok || ops[strings.ToLower(method)] == nil {
				missing = append(missing, method+" "+path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen < 60 {
		t.Fatalf("walked only %d routes", seen)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("routes missing from openapi.yaml:\n%s", strings.Join(missing, "\n"))
	}
}
