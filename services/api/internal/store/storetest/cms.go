package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func testCMS(t *testing.T, s store.Store) {
	now := time.Now().UTC()
	a, _ := newServer(t, s, "cms-a", now)
	task := newTask(t, s, a.ID, "cms.wp.install", now.Add(time.Hour))
	site := &store.Site{ID: auth.NewID(), ServerID: a.ID, Domain: "blog.example.com", UpstreamPort: 18095, TLSMode: "acme"}
	if err := s.CreateSite(ctx(), site); err != nil {
		t.Fatal(err)
	}
	inst := func(name string, port int) *store.CMSInstance {
		return &store.CMSInstance{ID: auth.NewID(), ServerID: a.ID, Kind: "wordpress", Name: name, Title: "Blog", URL: "https://blog.example.com",
			HostPort: port, AdminUser: "admin", AdminEmail: "a@example.com"}
	}
	blog := inst("blog", 18095)
	blog.InstallTaskID, blog.SiteID = &task.ID, &site.ID
	if err := s.CreateCMS(ctx(), blog); err != nil || blog.CreatedAt.IsZero() {
		t.Fatalf("CreateCMS: %v", err)
	}
	if err := s.CreateCMS(ctx(), inst("blog", 18096)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if err := s.CreateCMS(ctx(), inst("shop", 18095)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate port: %v", err)
	}
	got, err := s.GetCMS(ctx(), blog.ID)
	if err != nil || got.InstallTaskID == nil || *got.InstallTaskID != task.ID || got.SiteID == nil || got.Kind != "wordpress" {
		t.Fatalf("GetCMS: %+v %v", got, err)
	}
	if err := s.DeleteSite(ctx(), site.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetCMS(ctx(), blog.ID); got.SiteID != nil {
		t.Fatalf("site link must be cleared when the site goes: %+v", got)
	}
	if list, _ := s.ListCMS(ctx(), a.ID); len(list) != 1 {
		t.Fatalf("ListCMS: %+v", list)
	}
	if err := s.DeleteCMS(ctx(), blog.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCMS(ctx(), blog.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}
