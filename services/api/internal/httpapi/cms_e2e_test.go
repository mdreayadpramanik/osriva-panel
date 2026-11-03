package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func TestWordPressInstallFlow(t *testing.T) {
	e := newAgentEnv(t)
	res, client := e.enroll(e.addServer("cms-1"))
	id := res.Response.ServerID
	ctx := context.Background()
	auditor := e.principal("auditor-cms@example.com", rbac.RoleAuditor)
	in := fleet.WordPressInput{Name: "blog", Title: "My blog", AdminUser: "admin", AdminEmail: "admin@example.com", HostPort: 18095,
		Domain: "Blog.Example.com", TLSMode: "acme"}

	if _, err := e.fleet.InstallWordPress(ctx, auditor, id, in); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor installed WordPress: %v", err)
	}
	for name, bad := range map[string]func(*fleet.WordPressInput){
		"bad name":   func(w *fleet.WordPressInput) { w.Name = "My Blog" },
		"bad email":  func(w *fleet.WordPressInput) { w.AdminEmail = "nope" },
		"bad user":   func(w *fleet.WordPressInput) { w.AdminUser = "a b; rm" },
		"bad domain": func(w *fleet.WordPressInput) { w.Domain = "*.example.com" },
		"no title":   func(w *fleet.WordPressInput) { w.Title = "" },
	} {
		x := in
		bad(&x)
		var ve *identity.ValidationError
		if _, err := e.fleet.InstallWordPress(ctx, e.owner, id, x); !errors.As(err, &ve) {
			t.Errorf("%s: %v", name, err)
		}
	}

	c, err := e.fleet.InstallWordPress(ctx, e.owner, id, in)
	if err != nil || len(c.AdminPassword) < 16 || c.URL != "https://blog.example.com" || c.Domain != "blog.example.com" || c.InstallStatus != store.TaskQueued {
		t.Fatalf("install: %+v %v", c, err)
	}
	ts, _ := client.PollTasks(ctx)
	var p agenttask.WPInstallParams
	_ = json.Unmarshal(ts[0].Params, &p)
	if ts[0].Type != agenttask.CMSWPInstall || p.AdminPassword != c.AdminPassword || p.HostPort != 18095 {
		t.Fatalf("agent params: %s", ts[0].Params)
	}
	// the website was created and points at the instance
	v, _ := e.fleet.Websites(ctx, e.owner, id)
	if len(v.Sites) != 1 || v.Sites[0].Domain != "blog.example.com" || v.Sites[0].UpstreamPort != 18095 {
		t.Fatalf("website: %+v", v.Sites)
	}
	again := in
	again.Domain, again.HostPort = "", 18096
	var ve *identity.ValidationError
	if _, err := e.fleet.InstallWordPress(ctx, e.owner, id, again); !errors.As(err, &ve) {
		t.Fatalf("duplicate name: %v", err)
	}
	again.Name, again.HostPort = "shop", 18095
	if _, err := e.fleet.InstallWordPress(ctx, e.owner, id, again); !errors.As(err, &ve) {
		t.Fatalf("duplicate port: %v", err)
	}

	ok, _ := json.Marshal(agenttask.WPCommandResult{Name: "blog", OK: true, Output: "Success"})
	if err := client.SendResult(ctx, ts[0].ID, agentproto.TaskResult{Status: "succeeded", Result: ok}); err != nil {
		t.Fatal(err)
	}
	if task, _ := e.store().GetTask(ctx, ts[0].ID); strings.Contains(string(task.Params), c.AdminPassword) {
		t.Fatalf("admin password kept at rest: %s", task.Params)
	}
	audit, _ := e.store().ListAudit(ctx, store.AuditFilter{OrganizationID: store.DefaultOrganizationID, Limit: 200})
	for _, a := range audit {
		if b, _ := json.Marshal(a.Metadata); strings.Contains(string(b), c.AdminPassword) {
			t.Fatalf("admin password in audit: %s", b)
		}
	}
	list, err := e.fleet.ListCMS(ctx, auditor, id)
	if err != nil || len(list) != 1 || list[0].InstallStatus != store.TaskSucceeded {
		t.Fatalf("list: %+v %v", list, err)
	}

	// read-only operations are allowed for auditors, changes are not
	if _, err := e.fleet.QueueTask(ctx, auditor, id, agenttask.CMSWPInfo, json.RawMessage(`{"name":"blog"}`)); err != nil {
		t.Fatalf("auditor info: %v", err)
	}
	if _, err := e.fleet.QueueTask(ctx, auditor, id, agenttask.CMSWPUpdate, json.RawMessage(`{"name":"blog","target":"core"}`)); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor update: %v", err)
	}

	rm, err := e.fleet.DeleteCMS(ctx, e.owner, id, c.ID, true)
	if err != nil || rm.Type != agenttask.CMSRemove || !strings.Contains(string(rm.Params), `"delete_data":true`) {
		t.Fatalf("delete: %+v %v", rm, err)
	}
	if v, _ = e.fleet.Websites(ctx, e.owner, id); len(v.Sites) != 0 {
		t.Fatalf("website must go with the instance: %+v", v.Sites)
	}
}
