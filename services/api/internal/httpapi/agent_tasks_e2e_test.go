package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/pkg/metrics"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func (e *agentEnv) principal(email, role string) *identity.Principal {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.svc.BootstrapUser(ctx, email, "U", pw, role, "test"); err != nil {
		e.t.Fatal(err)
	}
	res, err := e.svc.Login(ctx, identity.LoginInput{Email: email, Password: pw})
	if err != nil {
		e.t.Fatal(err)
	}
	return res.Principal
}

func TestTaskRoundTripOverMTLS(t *testing.T) {
	e := newAgentEnv(t)
	res, client := e.enroll(e.addServer("web-1"))
	id := res.Response.ServerID
	ctx := context.Background()

	// An idle long poll is woken as soon as a task is queued.
	type polled struct {
		tasks []agentproto.TaskEnvelope
		err   error
		after time.Duration
	}
	got := make(chan polled, 1)
	start := time.Now()
	go func() {
		ts, err := client.PollTasks(ctx)
		got <- polled{ts, err, time.Since(start)}
	}()
	time.Sleep(300 * time.Millisecond)
	task, err := e.fleet.QueueTask(ctx, e.owner, id, agenttask.ServicesList, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := <-got
	if p.err != nil || len(p.tasks) != 1 || p.tasks[0].ID != task.ID || p.tasks[0].Type != agenttask.ServicesList {
		t.Fatalf("poll: %+v", p)
	}
	if p.after > 5*time.Second {
		t.Fatalf("long poll was not woken promptly (%v)", p.after)
	}

	// The waiting reader sees the result as soon as the agent reports it.
	done := make(chan *store.Task, 1)
	go func() {
		x, _ := e.fleet.GetTask(ctx, e.owner, id, task.ID, 10*time.Second)
		done <- x
	}()
	result, _ := json.Marshal(agenttask.ServicesResult{Manager: "systemd", Services: []agenttask.Service{{Name: "nginx.service", State: "running", Startup: "enabled"}}})
	if err := client.SendResult(ctx, task.ID, agentproto.TaskResult{Status: "succeeded", Result: result}); err != nil {
		t.Fatal(err)
	}
	if x := <-done; x == nil || x.Status != store.TaskSucceeded || len(x.Result) == 0 {
		t.Fatalf("waited task: %+v", x)
	}
	// A result can only be delivered once.
	if err := client.SendResult(ctx, task.ID, agentproto.TaskResult{Status: "failed", Error: "x"}); apiCode(err) != "policy_violation" {
		t.Fatalf("second result: %v", err)
	}
	// Failed results must explain themselves.
	t2, _ := e.fleet.QueueTask(ctx, e.owner, id, agenttask.ProcessesList, nil)
	if ts, _ := client.PollTasks(ctx); len(ts) != 1 || ts[0].ID != t2.ID {
		t.Fatalf("second poll: %+v", ts)
	}
	if err := client.SendResult(ctx, t2.ID, agentproto.TaskResult{Status: "failed"}); apiCode(err) != "validation_failed" {
		t.Fatalf("failure without error text: %v", err)
	}
}

func TestTaskAuthorization(t *testing.T) {
	e := newAgentEnv(t)
	res, _ := e.enroll(e.addServer("db-1"))
	id := res.Response.ServerID
	ctx := context.Background()
	auditor := e.principal("auditor@example.com", rbac.RoleAuditor)
	restart := json.RawMessage(`{"name":"nginx.service","action":"restart"}`)
	stop := json.RawMessage(`{"name":"sshd.service","action":"stop"}`)

	if _, err := e.fleet.QueueTask(ctx, auditor, id, agenttask.ServicesList, nil); err != nil {
		t.Fatalf("auditor read-only task: %v", err)
	}
	if _, err := e.fleet.QueueTask(ctx, auditor, id, agenttask.ServiceAction, restart); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("auditor service action: %v", err)
	}
	var ve *identity.ValidationError
	for _, bad := range []struct {
		typ    string
		params string
	}{{"shell.exec", `{"cmd":"id"}`}, {agenttask.ServiceAction, `{"name":"x; reboot","action":"stop"}`}} {
		if _, err := e.fleet.QueueTask(ctx, e.owner, id, bad.typ, json.RawMessage(bad.params)); !errors.As(err, &ve) {
			t.Errorf("%s: %v", bad.typ, err)
		}
	}
	// Restart is allowed right away; stop needs a fresh password confirmation.
	if _, err := e.fleet.QueueTask(ctx, e.owner, id, agenttask.ServiceAction, restart); err != nil {
		t.Fatalf("restart: %v", err)
	}
	stale := *e.owner
	sess := *e.owner.Session
	old := time.Now().Add(-time.Hour)
	sess.ReauthenticatedAt = &old
	stale.Session = &sess
	if _, err := e.fleet.QueueTask(ctx, &stale, id, agenttask.ServiceAction, stop); !errors.Is(err, identity.ErrReauthRequired) {
		t.Fatalf("stop without reauth: %v", err)
	}

	// Revoked servers cannot receive tasks.
	if err := e.fleet.Revoke(ctx, e.owner, id); err != nil {
		t.Fatal(err)
	}
	var pe *identity.PolicyError
	if _, err := e.fleet.QueueTask(ctx, e.owner, id, agenttask.ServicesList, nil); !errors.As(err, &pe) {
		t.Fatalf("task for revoked server: %v", err)
	}
}

func TestAgentsCannotTouchEachOthersTasks(t *testing.T) {
	e := newAgentEnv(t)
	a, clientA := e.enroll(e.addServer("a"))
	_, clientB := e.enroll(e.addServer("b"))
	ctx := context.Background()
	task, _ := e.fleet.QueueTask(ctx, e.owner, a.Response.ServerID, agenttask.ServicesList, nil)

	pollCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	if ts, err := clientB.PollTasks(pollCtx); err == nil && len(ts) > 0 {
		t.Fatalf("agent B received A's task: %+v", ts)
	}
	if ts, _ := clientA.PollTasks(ctx); len(ts) != 1 {
		t.Fatal("agent A did not get its task")
	}
	if err := clientB.SendResult(ctx, task.ID, agentproto.TaskResult{Status: "succeeded"}); apiCode(err) != "policy_violation" {
		t.Fatalf("agent B completed A's task: %v", err)
	}
}

func TestMetricsIngestAndRead(t *testing.T) {
	e := newAgentEnv(t)
	res, client := e.enroll(e.addServer("m-1"))
	ctx := context.Background()
	now := time.Now().UTC()
	cpu, mem := 42.5, uint64(1<<30)
	samples := []metrics.Sample{
		{TS: now.Add(-20 * time.Second), CPUPercent: &cpu, MemUsedBytes: &mem},
		{TS: now.Add(-10 * time.Second), CPUPercent: &cpu},
		{TS: now.Add(time.Hour)}, // future (skewed clock): dropped
	}
	if err := client.SendMetrics(ctx, samples); err != nil {
		t.Fatal(err)
	}
	got, err := e.fleet.Metrics(ctx, e.owner, res.Response.ServerID, 10*time.Minute)
	if err != nil || len(got) != 2 || got[0].CPUPercent == nil || *got[0].CPUPercent != 42.5 || got[1].MemUsedBytes != nil {
		t.Fatalf("metrics: %+v %v", got, err)
	}
	big := make([]metrics.Sample, agentproto.MaxMetricsBatch+1)
	if err := client.SendMetrics(ctx, big); apiCode(err) != "validation_failed" {
		t.Fatalf("oversized batch: %v", err)
	}
}
