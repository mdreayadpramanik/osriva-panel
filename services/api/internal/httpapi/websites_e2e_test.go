package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
)

func TestWebsitesAndProxy(t *testing.T) {
	e := newAgentEnv(t)
	res, client := e.enroll(e.addServer("web-a"))
	id := res.Response.ServerID
	res2, _ := e.enroll(e.addServer("web-b"))
	ctx := context.Background()
	auditor := e.principal("auditor-web@example.com", rbac.RoleAuditor)

	// proxy off: sites are stored but nothing is sent to the agent
	site, task, err := e.fleet.CreateSite(ctx, e.owner, id, fleet.SiteInput{Domain: "WWW.Example.com.", UpstreamPort: 8080, TLSMode: "acme"})
	if err != nil || task != nil || site.Domain != "www.example.com" {
		t.Fatalf("create site with proxy off: %+v %v %v", site, task, err)
	}
	for name, in := range map[string]fleet.SiteInput{
		"IP address":     {Domain: "10.0.0.1", UpstreamPort: 80, TLSMode: "acme"},
		"wildcard":       {Domain: "*.example.com", UpstreamPort: 80, TLSMode: "acme"},
		"single label":   {Domain: "intranet", UpstreamPort: 80, TLSMode: "acme"},
		"injection":      {Domain: "a.com {\n respond 1", UpstreamPort: 80, TLSMode: "acme"},
		"bad tls":        {Domain: "a.example.com", UpstreamPort: 80, TLSMode: "self"},
		"upstream = 443": {Domain: "b.example.com", UpstreamPort: 443, TLSMode: "acme"},
	} {
		var ve *identity.ValidationError
		if _, _, err := e.fleet.CreateSite(ctx, e.owner, id, in); !errors.As(err, &ve) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	var ve *identity.ValidationError
	if _, _, err := e.fleet.CreateSite(ctx, e.owner, res2.Response.ServerID, fleet.SiteInput{Domain: "www.example.com", UpstreamPort: 80, TLSMode: "acme"}); !errors.As(err, &ve) {
		t.Fatalf("same domain on another server: %v", err)
	}
	if _, _, err := e.fleet.CreateSite(ctx, auditor, id, fleet.SiteInput{Domain: "c.example.com", UpstreamPort: 80, TLSMode: "acme"}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor created a site: %v", err)
	}
	stale := *e.owner
	sess := *e.owner.Session
	old := time.Now().Add(-time.Hour)
	sess.ReauthenticatedAt = &old
	stale.Session = &sess
	if _, _, err := e.fleet.SaveProxy(ctx, &stale, id, fleet.ProxyInput{Enabled: true, HTTPPort: 80, HTTPSPort: 443}); !errors.Is(err, identity.ErrReauthRequired) {
		t.Fatalf("proxy change without reauth: %v", err)
	}
	if _, _, err := e.fleet.SaveProxy(ctx, e.owner, id, fleet.ProxyInput{Enabled: true, HTTPPort: 80, HTTPSPort: 443, ACMEEmail: "x y@z"}); !errors.As(err, &ve) {
		t.Fatalf("bad e-mail: %v", err)
	}
	if _, _, err := e.fleet.SaveProxy(ctx, e.owner, id, fleet.ProxyInput{Enabled: true, HTTPPort: 8080, HTTPSPort: 443}); !errors.As(err, &ve) {
		t.Fatalf("http port equal to a site's upstream: %v", err)
	}

	// enabling sends the full configuration
	p, task, err := e.fleet.SaveProxy(ctx, e.owner, id, fleet.ProxyInput{Enabled: true, HTTPPort: 80, HTTPSPort: 443, ACMEEmail: "ops@example.com"})
	if err != nil || !p.Enabled || task == nil || task.Type != agenttask.ProxyApply {
		t.Fatalf("enable: %+v %+v %v", p, task, err)
	}
	ts, _ := client.PollTasks(ctx)
	var cfg agenttask.ProxyConfig
	_ = json.Unmarshal(ts[0].Params, &cfg)
	if len(cfg.Sites) != 1 || cfg.Sites[0].Domain != "www.example.com" || cfg.ACMEEmail != "ops@example.com" {
		t.Fatalf("applied config: %s", ts[0].Params)
	}

	// with the proxy on, each change queues the new configuration
	api, task, err := e.fleet.CreateSite(ctx, e.owner, id, fleet.SiteInput{Domain: "ci.localhost", UpstreamPort: 3000, TLSMode: "internal"})
	if err != nil || task == nil || !strings.Contains(string(task.Params), "ci.localhost") {
		t.Fatalf("create with proxy on: %v %+v", err, task)
	}
	if _, task, err = e.fleet.UpdateSite(ctx, e.owner, id, api.ID, fleet.SiteInput{Domain: "ci.localhost", UpstreamPort: 3001, TLSMode: "none"}); err != nil || !strings.Contains(string(task.Params), `"upstream_port":3001`) {
		t.Fatalf("update: %v %+v", err, task)
	}
	if task, err = e.fleet.DeleteSite(ctx, e.owner, id, site.ID); err != nil || strings.Contains(string(task.Params), "www.example.com") {
		t.Fatalf("delete: %v %+v", err, task)
	}

	v, err := e.fleet.Websites(ctx, auditor, id)
	if err != nil || len(v.Sites) != 1 || !v.Sites[0].DNS.PointsHere || !v.Settings.Enabled {
		t.Fatalf("auditor reads websites: %+v %v", v, err)
	}
	if _, _, err := e.fleet.CreateSite(ctx, e.owner, id, fleet.SiteInput{Domain: "osriva-test.invalid", UpstreamPort: 5000, TLSMode: "none"}); err != nil {
		t.Fatal(err)
	}
	v, _ = e.fleet.Websites(ctx, e.owner, id)
	for _, s := range v.Sites {
		if s.Domain == "osriva-test.invalid" && (s.DNS.Error == "" || s.DNS.PointsHere) {
			t.Fatalf("unresolvable domain: %+v", s.DNS)
		}
	}

	if _, task, err = e.fleet.SaveProxy(ctx, e.owner, id, fleet.ProxyInput{Enabled: false, HTTPPort: 80, HTTPSPort: 443}); err != nil || task == nil || task.Type != agenttask.ProxyDisable {
		t.Fatalf("disable: %+v %v", task, err)
	}
}
