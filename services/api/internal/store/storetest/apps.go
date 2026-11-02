package storetest

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func testApps(t *testing.T, s store.Store) {
	now := time.Now().UTC()
	a, _ := newServer(t, s, "apps-a", now)
	b, _ := newServer(t, s, "apps-b", now)
	app := func(serverID, name string, port int) *store.App {
		return &store.App{ID: auth.NewID(), ServerID: serverID, Name: name, GitURL: "https://example.com/r.git", GitRef: "main",
			BuildMode: "static", ContainerPort: 80, HostPort: port, EnvEnc: []byte{1, 2, 3}}
	}
	web := app(a.ID, "web", 8080)
	if err := s.CreateApp(ctx(), web); err != nil || web.CreatedAt.IsZero() {
		t.Fatalf("CreateApp: %v", err)
	}
	if err := s.CreateApp(ctx(), app(a.ID, "web", 8081)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if err := s.CreateApp(ctx(), app(a.ID, "api", 8080)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate host port: %v", err)
	}
	if err := s.CreateApp(ctx(), app(b.ID, "web", 8080)); err != nil {
		t.Fatalf("same name on another server: %v", err)
	}
	if err := s.CreateApp(ctx(), app(auth.NewID(), "x", 1)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing server: %v", err)
	}
	api := app(a.ID, "api", 9000)
	if err := s.CreateApp(ctx(), api); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListApps(ctx(), a.ID)
	if len(list) != 2 || list[0].Name != "api" || !bytes.Equal(list[1].EnvEnc, []byte{1, 2, 3}) {
		t.Fatalf("ListApps: %+v", list)
	}

	upd := *web
	upd.Name, upd.GitRef, upd.HostPort, upd.EnvEnc, upd.Public = "renamed", "v2", 8082, nil, true
	if err := s.UpdateApp(ctx(), &upd); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetApp(ctx(), web.ID)
	if got.Name != "web" || got.GitRef != "v2" || got.HostPort != 8082 || got.EnvEnc != nil || !got.Public {
		t.Fatalf("UpdateApp (name is immutable): %+v", got)
	}
	upd.HostPort = 9000
	if err := s.UpdateApp(ctx(), &upd); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("update to a used port: %v", err)
	}

	task := newTask(t, s, a.ID, "app.deploy", now.Add(time.Hour))
	d1 := &store.Deployment{ID: auth.NewID(), AppID: web.ID, TaskID: &task.ID, Kind: "deploy", Image: "osriva-app/web:1", GitRef: "main"}
	if err := s.CreateDeployment(ctx(), d1); err != nil || d1.CreatedAt.IsZero() {
		t.Fatalf("CreateDeployment: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	d2 := &store.Deployment{ID: auth.NewID(), AppID: web.ID, Kind: "rollback", RollbackOf: &d1.ID, Image: d1.Image}
	if err := s.CreateDeployment(ctx(), d2); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDeployment(ctx(), &store.Deployment{ID: auth.NewID(), AppID: auth.NewID(), Kind: "deploy", Image: "x"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deployment for missing app: %v", err)
	}
	ds, _ := s.ListDeployments(ctx(), web.ID, 10)
	if len(ds) != 2 || ds[0].ID != d2.ID || ds[0].RollbackOf == nil || *ds[0].RollbackOf != d1.ID || ds[1].TaskID == nil || *ds[1].TaskID != task.ID {
		t.Fatalf("ListDeployments newest first: %+v", ds)
	}
	if one, _ := s.ListDeployments(ctx(), web.ID, 1); len(one) != 1 {
		t.Fatal("limit")
	}
	if g, err := s.GetDeployment(ctx(), d1.ID); err != nil || g.Image != d1.Image {
		t.Fatalf("GetDeployment: %+v %v", g, err)
	}

	if err := s.DeleteApp(ctx(), web.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDeployment(ctx(), d1.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deployments must go with their app: %v", err)
	}
	if err := s.DeleteApp(ctx(), web.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}
