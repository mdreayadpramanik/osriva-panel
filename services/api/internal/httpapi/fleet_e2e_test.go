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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/pkg/metrics"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/fleet"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/jobs"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/monitor"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func (e *agentEnv) markOnline(id string) {
	e.t.Helper()
	if err := e.store().RecordReport(context.Background(), id, store.ServerFacts{}, "127.0.0.1", time.Now(), nil); err != nil {
		e.t.Fatal(err)
	}
}

func TestServerGroupsAndBatches(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	resA, agentA := e.enroll(e.addServer("fleet-a"))
	resB, agentB := e.enroll(e.addServer("fleet-b"))
	resC, _ := e.enroll(e.addServer("fleet-c")) // enrolled, never reports: offline
	a, b, c := resA.Response.ServerID, resB.Response.ServerID, resC.Response.ServerID
	e.markOnline(a)
	e.markOnline(b)
	pending, _, _ := e.fleet.CreateServer(ctx, e.owner, "fleet-pending")
	auditor := e.principal("auditor-fl@example.com", rbac.RoleAuditor)

	// groups
	if _, err := e.fleet.CreateGroup(ctx, auditor, fleet.GroupInput{Name: "web"}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor created a group: %v", err)
	}
	var ve *identity.ValidationError
	if _, err := e.fleet.CreateGroup(ctx, e.owner, fleet.GroupInput{Name: "a\nb"}); !errors.As(err, &ve) {
		t.Fatalf("multi-line name: %v", err)
	}
	g, err := e.fleet.CreateGroup(ctx, e.owner, fleet.GroupInput{Name: " web ", Description: "front ends"})
	if err != nil || g.Name != "web" {
		t.Fatalf("CreateGroup: %+v %v", g, err)
	}
	if _, err := e.fleet.CreateGroup(ctx, e.owner, fleet.GroupInput{Name: "web"}); !errors.As(err, &ve) || ve.Field != "name" {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := e.fleet.SetGroupMembers(ctx, e.owner, g.ID, []string{a, "not-a-server"}); !errors.As(err, &ve) {
		t.Fatalf("unknown member: %v", err)
	}
	g, err = e.fleet.SetGroupMembers(ctx, e.owner, g.ID, []string{a, b, c, pending.Server.ID, a})
	if err != nil || len(g.ServerIDs) != 4 {
		t.Fatalf("members: %+v %v", g, err)
	}
	if list, err := e.fleet.ListGroups(ctx, auditor); err != nil || len(list) != 1 || len(list[0].ServerIDs) != 4 {
		t.Fatalf("auditor lists groups: %+v %v", list, err)
	}

	// batches: only allowlisted types, same permission classes as single tasks
	for _, typ := range []string{agenttask.FilesRead, agenttask.DockerContainerRun, agenttask.HostnameSet, "shell.exec"} {
		if _, err := e.fleet.RunBatch(ctx, e.owner, fleet.BatchInput{Type: typ, Params: json.RawMessage(`{}`), GroupID: &g.ID}); !errors.As(err, &ve) {
			t.Errorf("%s accepted in a batch: %v", typ, err)
		}
	}
	restart := json.RawMessage(`{"name":"nginx.service","action":"restart"}`)
	if _, err := e.fleet.RunBatch(ctx, auditor, fleet.BatchInput{Type: agenttask.ServiceAction, Params: restart, GroupID: &g.ID}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor restarted services fleet-wide: %v", err)
	}
	key := json.RawMessage(`{"user":"deploy","key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb6m1lDjp0dmKt8/a2QvYgDphMwHsUeyGnUjjZyyrnD ci"}`)
	stale := *e.owner
	sess := *e.owner.Session
	old := time.Now().Add(-time.Hour)
	sess.ReauthenticatedAt = &old
	stale.Session = &sess
	if _, err := e.fleet.RunBatch(ctx, &stale, fleet.BatchInput{Type: agenttask.SSHKeysAdd, Params: key, GroupID: &g.ID}); !errors.Is(err, identity.ErrReauthRequired) {
		t.Fatalf("SSH key batch without re-authentication: %v", err)
	}
	if _, err := e.fleet.RunBatch(ctx, e.owner, fleet.BatchInput{Type: agenttask.ServicesList, GroupID: &g.ID, ServerIDs: []string{a}}); !errors.As(err, &ve) {
		t.Fatalf("group and list together: %v", err)
	}
	if _, err := e.fleet.RunBatch(ctx, e.owner, fleet.BatchInput{Type: agenttask.ServiceAction, Params: json.RawMessage(`{"name":"x;y","action":"restart"}`), GroupID: &g.ID}); !errors.As(err, &ve) {
		t.Fatalf("invalid params: %v", err)
	}

	v, err := e.fleet.RunBatch(ctx, e.owner, fleet.BatchInput{Type: agenttask.ServiceAction, Params: restart, GroupID: &g.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Tasks) != 2 || len(v.Skipped) != 2 || v.Counts["queued"] != 2 || v.Batch.Target != "group web" {
		t.Fatalf("batch: %+v", v)
	}
	reasons := map[string]string{}
	for _, s := range v.Skipped {
		reasons[s.Name] = s.Reason
	}
	if reasons["fleet-c"] != "agent offline" || reasons["fleet-pending"] != "agent not enrolled" {
		t.Fatalf("skip reasons: %v", reasons)
	}

	// Both agents receive their task and answer; the batch reflects it.
	for i, ag := range []interface {
		PollTasks(context.Context) ([]agentproto.TaskEnvelope, error)
		SendResult(context.Context, string, agentproto.TaskResult) error
	}{agentA, agentB} {
		tasks, err := ag.PollTasks(ctx)
		if err != nil || len(tasks) != 1 || tasks[0].Type != agenttask.ServiceAction {
			t.Fatalf("agent %d poll: %+v %v", i, tasks, err)
		}
		r := agentproto.TaskResult{Status: "succeeded", Result: json.RawMessage(`{"ok":true}`)}
		if i == 1 {
			r = agentproto.TaskResult{Status: "failed", Error: "unit nginx.service not found"}
		}
		if err := ag.SendResult(ctx, tasks[0].ID, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.fleet.GetBatch(ctx, auditor, v.Batch.ID)
	if err != nil || got.Counts["succeeded"] != 1 || got.Counts["failed"] != 1 || got.Tasks[0].ServerName != "fleet-a" ||
		got.Tasks[1].Task.Error != "unit nginx.service not found" || len(got.Skipped) != 2 {
		t.Fatalf("batch after results: %+v %v", got, err)
	}
	if list, _ := e.fleet.ListBatches(ctx, auditor, 10); len(list) != 1 || list[0].Batch.ID != v.Batch.ID {
		t.Fatalf("ListBatches: %+v", list)
	}

	// explicit list; nobody online → refused without a batch
	if _, err := e.fleet.RunBatch(ctx, e.owner, fleet.BatchInput{Type: agenttask.TimeInfo, ServerIDs: []string{c}}); err == nil {
		t.Fatal("a batch with no online server must be refused")
	}
	if list, _ := e.fleet.ListBatches(ctx, auditor, 10); len(list) != 1 {
		t.Fatalf("refused batch stored: %d", len(list))
	}

	// audit: one fleet.batch entry plus the usual per-server entries
	audit, _ := e.store().ListAudit(ctx, store.AuditFilter{OrganizationID: store.DefaultOrganizationID, Limit: 500})
	batchEntries, taskEntries := 0, 0
	for _, x := range audit {
		switch x.Action {
		case "fleet.batch":
			batchEntries++
		case "servers.task":
			if x.Metadata["batch_id"] == v.Batch.ID {
				taskEntries++
			}
		}
	}
	if batchEntries != 1 || taskEntries != 2 {
		t.Fatalf("audit: %d batch, %d task entries", batchEntries, taskEntries)
	}

	// alert rules can target the group; the group then cannot be deleted
	keys, _ := secrets.NewKeyring(bytes.Repeat([]byte{9}, 32))
	mon := monitor.New(e.store(), e.svc, keys, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if _, err := mon.CreateRule(ctx, e.owner, monitor.RuleInput{Name: "x", ServerID: &a, GroupID: &g.ID, Metric: "cpu", Threshold: 50, DurationMinutes: 1}); !errors.As(err, &ve) {
		t.Fatalf("server and group together: %v", err)
	}
	rule, err := mon.CreateRule(ctx, e.owner, monitor.RuleInput{Name: "web cpu", GroupID: &g.ID, Metric: "cpu", Threshold: 50, DurationMinutes: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var pe *identity.PolicyError
	if err := e.fleet.DeleteGroup(ctx, e.owner, g.ID); !errors.As(err, &pe) {
		t.Fatalf("deleting a group used by a rule: %v", err)
	}
	// A server outside the group must not fire the group's rule.
	outside, _ := e.enroll(e.addServer("fleet-outside"))
	o := outside.Response.ServerID
	now := time.Now().UTC()
	for _, id := range []string{a, o} {
		var s []store.MetricSample
		for i := 0; i <= 7; i++ {
			cpu := 95.0
			ts := now.Add(-70*time.Second + time.Duration(i)*10*time.Second)
			d, _ := json.Marshal(metrics.Sample{TS: ts, CPUPercent: &cpu})
			s = append(s, store.MetricSample{ServerID: id, TS: ts, Data: d})
		}
		_ = e.store().InsertMetrics(ctx, s)
	}
	mon.Evaluate(ctx, now)
	evs, _ := mon.ListEvents(ctx, e.owner, 10)
	if len(evs) != 1 || evs[0].ServerName != "fleet-a" || evs[0].State != "firing" {
		t.Fatalf("group rule events: %+v", evs)
	}
	if err := mon.DeleteRule(ctx, e.owner, rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.fleet.DeleteGroup(ctx, e.owner, g.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.fleet.GetBatch(ctx, e.owner, v.Batch.ID); got.Batch.GroupID != nil || got.Batch.Target != "group web" {
		t.Fatalf("batch history survives the group: %+v", got.Batch)
	}
}

func TestQueuedNotificationsRetry(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	res, _ := e.enroll(e.addServer("queue-1"))
	id := res.Response.ServerID
	keys, _ := secrets.NewKeyring(bytes.Repeat([]byte{9}, 32))
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	clock := time.Now().UTC()
	var cmu sync.Mutex
	now := func() time.Time { cmu.Lock(); defer cmu.Unlock(); return clock }
	advance := func(d time.Duration) { cmu.Lock(); clock = clock.Add(d); cmu.Unlock() }
	q := jobs.New(e.store(), quiet, now)
	mon := monitor.New(e.store(), e.svc, keys, quiet, nil)
	mon.UseQueue(q)

	var mu sync.Mutex
	var deliveries []string
	answers := []int{503, 502, 200}
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		deliveries = append(deliveries, r.Header.Get("X-Osriva-Delivery"))
		code := 200
		if len(answers) > 0 {
			code, answers = answers[0], answers[1:]
		}
		w.WriteHeader(code)
	}))
	defer flaky.Close()
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusGone) }))
	defer gone.Close()

	hookA, err := mon.CreateChannel(ctx, e.owner, monitor.ChannelInput{Name: "flaky", Kind: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"` + flaky.URL + `/a"}`)})
	if err != nil {
		t.Fatal(err)
	}
	hookB, _ := mon.CreateChannel(ctx, e.owner, monitor.ChannelInput{Name: "gone", Kind: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"` + gone.URL + `/b"}`)})
	if _, err := mon.CreateRule(ctx, e.owner, monitor.RuleInput{Name: "offline", Metric: "offline", Threshold: 5, DurationMinutes: 1,
		ChannelIDs: []string{hookA.ID, hookB.ID}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e.markOnline(id)
	mon.Evaluate(ctx, time.Now().Add(10*time.Minute))
	evs, _ := mon.ListEvents(ctx, e.owner, 5)
	if len(evs) != 1 || evs[0].Pending != 2 || evs[0].Delivered != 0 {
		t.Fatalf("the event waits for the queue: %+v", evs)
	}

	q.RunOnce(ctx) // flaky: 503 (retry); gone: 410 (permanent)
	evs, _ = mon.ListEvents(ctx, e.owner, 5)
	if evs[0].Pending != 1 || evs[0].Failed != 1 || !strings.Contains(evs[0].DeliveryError, "gone: webhook answered HTTP 410") {
		t.Fatalf("after the first run: %+v", evs[0])
	}
	advance(30 * time.Second)
	q.RunOnce(ctx) // 502
	advance(time.Minute)
	q.RunOnce(ctx) // 200
	evs, _ = mon.ListEvents(ctx, e.owner, 5)
	if evs[0].Pending != 0 || evs[0].Delivered != 1 || evs[0].Failed != 1 {
		t.Fatalf("delivered on the third attempt: %+v", evs[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deliveries) != 3 || deliveries[0] != evs[0].ID || deliveries[2] != deliveries[0] {
		t.Fatalf("every attempt carries the same delivery id: %v", deliveries)
	}
	st, _ := q.Status(ctx)
	if st.Counts[store.JobSucceeded] != 1 || st.Counts[store.JobFailed] != 1 || len(st.Recent) != 1 {
		t.Fatalf("queue status: %+v", st)
	}
}

func TestFleetHTTPRoutes(t *testing.T) {
	e := newEnv(t)
	fl, err := fleet.New(context.Background(), fleet.Options{Store: e.store, Identity: e.svc, Keys: e.keys})
	if err != nil {
		t.Fatal(err)
	}
	e.h = New(Options{Identity: e.svc, Fleet: fl, DB: e.db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test", CookieSecure: true})
	c := e.login("owner@example.com")
	rec := e.do("POST", "/api/v1/server-groups", map[string]string{"name": "db", "description": "databases"}, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", rec.Code, rec.Body)
	}
	var g groupDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &g)
	if g.ServerIDs == nil || g.Name != "db" {
		t.Fatalf("group: %+v", g)
	}
	if rec := e.do("PUT", "/api/v1/server-groups/"+g.ID, map[string]string{"name": "databases"}, c); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("PUT", "/api/v1/server-groups/"+g.ID+"/members", map[string]any{"server_ids": []string{}}, c); rec.Code != http.StatusOK {
		t.Fatalf("members: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/api/v1/server-groups", nil, c); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"databases"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("GET", "/api/v1/fleet/task-types", nil, c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"service.action"`) || strings.Contains(rec.Body.String(), `"files.read"`) {
		t.Fatalf("task types: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("POST", "/api/v1/fleet/batches", map[string]any{"type": "services.list", "group_id": g.ID}, c)
	if rec.Code != http.StatusConflict && rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Fatalf("batch on an empty group: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/api/v1/fleet/batches", nil, c); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"batches":[]`) {
		t.Fatalf("batches: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/api/v1/jobs", nil, c); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("jobs without a queue: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("DELETE", "/api/v1/server-groups/"+g.ID, nil, c); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
}
