package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func testProxy(t *testing.T, s store.Store) {
	now := time.Now().UTC()
	a, _ := newServer(t, s, "proxy-a", now)
	b, _ := newServer(t, s, "proxy-b", now)

	if _, err := s.GetProxySettings(ctx(), a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no settings yet: %v", err)
	}
	p := &store.ProxySettings{ServerID: a.ID, Enabled: true, HTTPPort: 80, HTTPSPort: 443, ACMEEmail: "ops@example.com"}
	if err := s.SaveProxySettings(ctx(), p); err != nil {
		t.Fatal(err)
	}
	p.HTTPSPort, p.ACMEStaging = 8443, true
	if err := s.SaveProxySettings(ctx(), p); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetProxySettings(ctx(), a.ID); err != nil || got.HTTPSPort != 8443 || !got.ACMEStaging || got.ACMEEmail != "ops@example.com" {
		t.Fatalf("upsert: %+v %v", got, err)
	}
	if err := s.SaveProxySettings(ctx(), &store.ProxySettings{ServerID: auth.NewID(), HTTPPort: 80, HTTPSPort: 443}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("settings for a missing server: %v", err)
	}

	www := &store.Site{ID: auth.NewID(), ServerID: a.ID, Domain: "www.example.com", UpstreamPort: 8080, TLSMode: "acme"}
	if err := s.CreateSite(ctx(), www); err != nil || www.CreatedAt.IsZero() {
		t.Fatalf("CreateSite: %v", err)
	}
	if err := s.CreateSite(ctx(), &store.Site{ID: auth.NewID(), ServerID: b.ID, Domain: "www.example.com", UpstreamPort: 1, TLSMode: "acme"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a domain must be unique across servers: %v", err)
	}
	api := &store.Site{ID: auth.NewID(), ServerID: a.ID, Domain: "api.example.com", UpstreamPort: 3000, TLSMode: "internal"}
	if err := s.CreateSite(ctx(), api); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListSites(ctx(), a.ID); len(list) != 2 || list[0].Domain != "api.example.com" {
		t.Fatalf("ListSites: %+v", list)
	}
	upd := *api
	upd.Domain = "www.example.com"
	if err := s.UpdateSite(ctx(), &upd); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("update to a used domain: %v", err)
	}
	upd.Domain, upd.UpstreamPort, upd.TLSMode, upd.ServerID = "api2.example.com", 3001, "none", b.ID
	if err := s.UpdateSite(ctx(), &upd); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSite(ctx(), api.ID); got.Domain != "api2.example.com" || got.UpstreamPort != 3001 || got.TLSMode != "none" || got.ServerID != a.ID {
		t.Fatalf("UpdateSite (server is immutable): %+v", got)
	}
	if err := s.DeleteSite(ctx(), api.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSite(ctx(), api.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}
