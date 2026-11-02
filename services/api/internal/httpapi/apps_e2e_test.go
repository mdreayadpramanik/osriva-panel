package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func TestAppsDeployRollbackAndSecrets(t *testing.T) {
	e := newAgentEnv(t)
	res, client := e.enroll(e.addServer("apps-1"))
	id := res.Response.ServerID
	ctx := context.Background()
	auditor := e.principal("auditor-apps@example.com", rbac.RoleAuditor)

	in := fleet.AppInput{Name: "web", GitURL: "https://example.com/web.git", GitRef: "main", BuildMode: "static",
		ContainerPort: 80, HostPort: 8080, Env: map[string]string{"DB_PASSWORD": "hunter4", "MODE": "prod"}}
	for name, bad := range map[string]func(*fleet.AppInput){
		"credentials in URL": func(a *fleet.AppInput) { a.GitURL = "https://user:token@example.com/web.git" },
		"file URL":           func(a *fleet.AppInput) { a.GitURL = "file:///etc" },
		"ssh URL":            func(a *fleet.AppInput) { a.GitURL = "ext::sh -c touch% /tmp/pwned" },
		"bad name":           func(a *fleet.AppInput) { a.Name = "Web_App" },
		"option as ref":      func(a *fleet.AppInput) { a.GitRef = "--upload-pack=touch /tmp/x" },
		"escaping context":   func(a *fleet.AppInput) { a.BuildContext = "../etc" },
		"bad mode":           func(a *fleet.AppInput) { a.BuildMode = "compose" },
	} {
		x := in
		bad(&x)
		var ve *identity.ValidationError
		if _, err := e.fleet.CreateApp(ctx, e.owner, id, x); !errors.As(err, &ve) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	if _, err := e.fleet.CreateApp(ctx, auditor, id, in); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor created an app: %v", err)
	}
	app, err := e.fleet.CreateApp(ctx, e.owner, id, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(app.EnvNames, ",") != "DB_PASSWORD,MODE" || app.EnvEnc != nil {
		t.Fatalf("view must list env names only: %+v", app)
	}
	dup := in
	dup.Name = "other"
	if _, err := e.fleet.CreateApp(ctx, e.owner, id, dup); err == nil {
		t.Fatal("a second app on the same host port was accepted")
	}
	if list, err := e.fleet.ListApps(ctx, auditor, id); err != nil || len(list) != 1 {
		t.Fatalf("auditor lists apps: %v %v", list, err)
	}
	if _, err := e.fleet.Deploy(ctx, auditor, id, app.ID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor deployed: %v", err)
	}
	stale := *e.owner
	sess := *e.owner.Session
	old := time.Now().Add(-time.Hour)
	sess.ReauthenticatedAt = &old
	stale.Session = &sess
	if _, err := e.fleet.Deploy(ctx, &stale, id, app.ID); !errors.Is(err, identity.ErrReauthRequired) {
		t.Fatalf("deploy without reauth: %v", err)
	}

	// deploy → the agent receives the real values, then reports failure with logs
	d1, err := e.fleet.Deploy(ctx, e.owner, id, app.ID)
	if err != nil || d1.Status != store.TaskQueued || d1.Image != "osriva-app/web:"+d1.ID {
		t.Fatalf("deploy: %+v %v", d1, err)
	}
	task, _ := e.store().GetTask(ctx, *d1.TaskID)
	if task.RunTimeout != 20*time.Minute {
		t.Fatalf("deploy run timeout: %v", task.RunTimeout)
	}
	ts, err := client.PollTasks(ctx)
	if err != nil || len(ts) != 1 || !strings.Contains(string(ts[0].Params), "hunter4") {
		t.Fatalf("agent must receive the environment: %+v %v", ts, err)
	}
	fail, _ := json.Marshal(agenttask.AppDeployResult{App: "web", Logs: "==> building\nnpm ERR!"})
	if err := client.SendResult(ctx, ts[0].ID, agentproto.TaskResult{Status: "failed", Error: "the image build failed", Result: fail}); err != nil {
		t.Fatal(err)
	}
	got, err := e.fleet.GetDeployment(ctx, auditor, id, app.ID, d1.ID)
	if err != nil || got.Status != store.TaskFailed || got.Result == nil || !strings.Contains(got.Result.Logs, "npm ERR!") {
		t.Fatalf("failed deployment keeps its logs: %+v %v", got, err)
	}
	if task, _ = e.store().GetTask(ctx, *d1.TaskID); strings.Contains(string(task.Params), "hunter4") {
		t.Fatalf("secret kept at rest after the task finished: %s", task.Params)
	}
	audit, _ := e.store().ListAudit(ctx, store.AuditFilter{OrganizationID: store.DefaultOrganizationID, Limit: 200})
	for _, a := range audit {
		if b, _ := json.Marshal(a.Metadata); strings.Contains(string(b), "hunter4") {
			t.Fatalf("secret in audit log: %s %s", a.Action, b)
		}
	}
	var pe *identity.PolicyError
	if _, err := e.fleet.Rollback(ctx, e.owner, id, app.ID, d1.ID); !errors.As(err, &pe) {
		t.Fatalf("rollback to a failed deployment: %v", err)
	}

	// a successful deployment becomes current; rolling back reuses its image
	d2, _ := e.fleet.Deploy(ctx, e.owner, id, app.ID)
	ts, _ = client.PollTasks(ctx)
	ok, _ := json.Marshal(agenttask.AppDeployResult{App: "web", Image: d2.Image, Commit: "abc123", State: "running", Logs: "done"})
	if err := client.SendResult(ctx, ts[0].ID, agentproto.TaskResult{Status: "succeeded", Result: ok}); err != nil {
		t.Fatal(err)
	}
	v, _ := e.fleet.GetApp(ctx, e.owner, id, app.ID)
	if v.Current == nil || v.Current.ID != d2.ID || v.Current.Result.Commit != "abc123" || len(v.Deployments) != 2 || v.Deployments[0].Result.Logs != "" {
		t.Fatalf("current deployment (list views carry no logs): %+v", v)
	}
	rb, err := e.fleet.Rollback(ctx, e.owner, id, app.ID, d2.ID)
	if err != nil || rb.Kind != "rollback" || rb.Image != d2.Image || rb.RollbackOf == nil || *rb.RollbackOf != d2.ID {
		t.Fatalf("rollback: %+v %v", rb, err)
	}
	ts, _ = client.PollTasks(ctx)
	var p agenttask.AppDeployParams
	_ = json.Unmarshal(ts[0].Params, &p)
	if p.Image != d2.Image || p.Source != nil || p.Run.Env["DB_PASSWORD"] != "hunter4" {
		t.Fatalf("rollback params: %+v", p)
	}

	// updates: nil env keeps the variables, an empty map clears them
	upd := in
	upd.Env, upd.GitRef = nil, "v2"
	if v, err = e.fleet.UpdateApp(ctx, e.owner, id, app.ID, upd); err != nil || v.GitRef != "v2" || len(v.EnvNames) != 2 {
		t.Fatalf("update keeping env: %+v %v", v, err)
	}
	upd.Env = map[string]string{}
	if v, _ = e.fleet.UpdateApp(ctx, e.owner, id, app.ID, upd); len(v.EnvNames) != 0 {
		t.Fatalf("update clearing env: %+v", v.EnvNames)
	}

	if _, err := e.fleet.DeleteApp(ctx, auditor, id, app.ID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor deleted: %v", err)
	}
	rm, err := e.fleet.DeleteApp(ctx, e.owner, id, app.ID)
	if err != nil || rm.Type != agenttask.AppRemove {
		t.Fatalf("delete: %+v %v", rm, err)
	}
	if _, err := e.fleet.GetApp(ctx, e.owner, id, app.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("app still there: %v", err)
	}
}
