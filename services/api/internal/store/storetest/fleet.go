package storetest

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func testServerGroups(t *testing.T, s store.Store) {
	now := time.Now().UTC().Truncate(time.Second)
	org := store.DefaultOrganizationID
	a, _ := newServer(t, s, "grp-a", now)
	b, _ := newServer(t, s, "grp-b", now)

	g := &store.ServerGroup{ID: auth.NewID(), OrganizationID: org, Name: "web", Description: "front ends"}
	if err := s.CreateServerGroup(ctx(), g); err != nil || g.CreatedAt.IsZero() {
		t.Fatalf("CreateServerGroup: %v", err)
	}
	if err := s.CreateServerGroup(ctx(), &store.ServerGroup{ID: auth.NewID(), OrganizationID: org, Name: "web"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate group name: %v", err)
	}
	if err := s.SetServerGroupMembers(ctx(), g.ID, []string{b.ID, a.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetServerGroup(ctx(), g.ID)
	want := []string{a.ID, b.ID}
	if b.ID < a.ID {
		want = []string{b.ID, a.ID}
	}
	if err != nil || len(got.ServerIDs) != 2 || got.ServerIDs[0] != want[0] || got.ServerIDs[1] != want[1] || got.Description != "front ends" {
		t.Fatalf("group members: %+v %v", got, err)
	}
	if err := s.SetServerGroupMembers(ctx(), g.ID, []string{a.ID, auth.NewID()}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown member: %v", err)
	}
	if got, _ := s.GetServerGroup(ctx(), g.ID); len(got.ServerIDs) != 2 {
		t.Fatalf("a failed update must keep the members: %v", got.ServerIDs)
	}
	if err := s.SetServerGroupMembers(ctx(), auth.NewID(), nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown group: %v", err)
	}

	other := &store.ServerGroup{ID: auth.NewID(), OrganizationID: org, Name: "db"}
	if err := s.CreateServerGroup(ctx(), other); err != nil {
		t.Fatal(err)
	}
	ren := &store.ServerGroup{ID: other.ID, Name: "web", Description: "x"}
	if err := s.UpdateServerGroup(ctx(), ren); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("rename onto a taken name: %v", err)
	}
	ren.Name = "databases"
	if err := s.UpdateServerGroup(ctx(), ren); err != nil || ren.OrganizationID != org || ren.ServerIDs == nil {
		t.Fatalf("UpdateServerGroup: %+v %v", ren, err)
	}
	list, _ := s.ListServerGroups(ctx(), org)
	if len(list) != 2 || list[0].Name != "databases" || list[1].Name != "web" || len(list[1].ServerIDs) != 2 || list[0].ServerIDs == nil {
		t.Fatalf("ListServerGroups: %+v", list)
	}

	rule := &store.AlertRule{ID: auth.NewID(), OrganizationID: org, Name: "web cpu", GroupID: &g.ID, Metric: "cpu", Threshold: 90,
		DurationMinutes: 5, Enabled: true}
	if err := s.CreateAlertRule(ctx(), rule); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAlertRule(ctx(), rule.ID); got.GroupID == nil || *got.GroupID != g.ID || got.ServerID != nil {
		t.Fatalf("rule group: %+v", got)
	}
	bad := &store.AlertRule{ID: auth.NewID(), OrganizationID: org, Name: "ghost", GroupID: strPtr(auth.NewID()), Metric: "cpu", Threshold: 1, DurationMinutes: 1}
	if err := s.CreateAlertRule(ctx(), bad); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rule for an unknown group: %v", err)
	}
	if err := s.DeleteServerGroup(ctx(), g.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a group used by a rule must not be deleted: %v", err)
	}
	if err := s.DeleteAlertRule(ctx(), rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteServerGroup(ctx(), g.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteServerGroup(ctx(), g.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func strPtr(s string) *string { return &s }

func testTaskBatches(t *testing.T, s store.Store) {
	now := time.Now().UTC().Truncate(time.Second)
	org := store.DefaultOrganizationID
	a, _ := newServer(t, s, "batch-a", now)
	b, _ := newServer(t, s, "batch-b", now)
	g := &store.ServerGroup{ID: auth.NewID(), OrganizationID: org, Name: "all"}
	if err := s.CreateServerGroup(ctx(), g); err != nil {
		t.Fatal(err)
	}

	batch := &store.TaskBatch{ID: auth.NewID(), OrganizationID: org, Type: "services.list", Params: json.RawMessage(`{}`), GroupID: &g.ID,
		Target: "group all", Skipped: json.RawMessage(`[{"server_id":"x","name":"old","reason":"offline"}]`)}
	var tasks []*store.Task
	for _, x := range []*store.Server{a, b} {
		tasks = append(tasks, &store.Task{ID: auth.NewID(), ServerID: x.ID, Type: "services.list", Params: json.RawMessage(`{}`),
			ExpiresAt: now.Add(time.Minute)})
	}
	if err := s.CreateTaskBatch(ctx(), batch, tasks); err != nil || batch.CreatedAt.IsZero() {
		t.Fatalf("CreateTaskBatch: %v", err)
	}
	got, err := s.GetTaskBatch(ctx(), batch.ID)
	if err != nil || got.Target != "group all" || got.GroupID == nil || !strings.Contains(string(got.Skipped), "offline") {
		t.Fatalf("GetTaskBatch: %+v %v", got, err)
	}
	bt, _ := s.ListBatchTasks(ctx(), batch.ID)
	if len(bt) != 2 || bt[0].BatchID == nil || *bt[0].BatchID != batch.ID || bt[0].Status != store.TaskQueued {
		t.Fatalf("ListBatchTasks: %+v", bt)
	}
	if one, _ := s.GetTask(ctx(), tasks[0].ID); one.BatchID == nil || *one.BatchID != batch.ID {
		t.Fatalf("task batch id: %+v", one)
	}
	claimed, _ := s.ClaimTasks(ctx(), a.ID, now, 10)
	if len(claimed) != 1 || claimed[0].BatchID == nil {
		t.Fatalf("batch tasks are ordinary queued tasks: %+v", claimed)
	}

	// All or nothing: one unknown server leaves no batch and no task behind.
	broken := &store.TaskBatch{ID: auth.NewID(), OrganizationID: org, Type: "services.list", Target: "2 servers"}
	bad := []*store.Task{
		{ID: auth.NewID(), ServerID: a.ID, Type: "services.list", ExpiresAt: now.Add(time.Minute)},
		{ID: auth.NewID(), ServerID: auth.NewID(), Type: "services.list", ExpiresAt: now.Add(time.Minute)},
	}
	if err := s.CreateTaskBatch(ctx(), broken, bad); err == nil {
		t.Fatal("a batch with an unknown server must fail")
	}
	if _, err := s.GetTaskBatch(ctx(), broken.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed batch stored: %v", err)
	}
	if _, err := s.GetTask(ctx(), bad[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("task of a failed batch stored: %v", err)
	}

	second := &store.TaskBatch{ID: auth.NewID(), OrganizationID: org, Type: "time.info", Target: "1 server"}
	if err := s.CreateTaskBatch(ctx(), second, []*store.Task{{ID: auth.NewID(), ServerID: b.ID, Type: "time.info", ExpiresAt: now.Add(time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListTaskBatches(ctx(), org, 1)
	if len(list) != 1 {
		t.Fatalf("ListTaskBatches limit: %d", len(list))
	}
	if all, _ := s.ListTaskBatches(ctx(), org, 0); len(all) != 2 || string(all[1].Skipped) == "" {
		t.Fatalf("ListTaskBatches: %+v", all)
	}
	if err := s.DeleteServerGroup(ctx(), g.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTaskBatch(ctx(), batch.ID); got.GroupID != nil {
		t.Fatalf("deleting a group keeps its batches: %+v", got)
	}
}

func testJobs(t *testing.T, s store.Store) {
	now := time.Now().UTC().Truncate(time.Second)
	j := &store.Job{ID: auth.NewID(), Kind: "notify", Payload: json.RawMessage(`{"event_id":"e"}`), MaxAttempts: 3, RunAt: now}
	later := &store.Job{ID: auth.NewID(), Kind: "notify", MaxAttempts: 3, RunAt: now.Add(time.Hour)}
	for _, x := range []*store.Job{j, later} {
		if err := s.EnqueueJob(ctx(), x); err != nil || x.Status != store.JobQueued {
			t.Fatalf("EnqueueJob: %v", err)
		}
	}
	claimed, err := s.ClaimJobs(ctx(), now, time.Minute, 10)
	if err != nil || len(claimed) != 1 || claimed[0].ID != j.ID || claimed[0].Attempts != 1 || claimed[0].Status != store.JobRunning || claimed[0].LockedUntil == nil {
		t.Fatalf("ClaimJobs: %+v %v", claimed, err)
	}
	if again, _ := s.ClaimJobs(ctx(), now.Add(30*time.Second), time.Minute, 10); len(again) != 0 {
		t.Fatalf("a leased job must not be claimed twice: %+v", again)
	}
	// The worker died: the lease expires and the job is claimed again.
	again, _ := s.ClaimJobs(ctx(), now.Add(2*time.Minute), time.Minute, 10)
	if len(again) != 1 || again[0].Attempts != 2 {
		t.Fatalf("expired lease: %+v", again)
	}
	retry := now.Add(5 * time.Minute)
	if err := s.FinishJob(ctx(), j.ID, store.JobQueued, retry, "HTTP 503", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetJob(ctx(), j.ID); got.Status != store.JobQueued || !got.RunAt.Equal(retry) || got.LastError != "HTTP 503" || got.LockedUntil != nil {
		t.Fatalf("retry: %+v", got)
	}
	if err := s.FinishJob(ctx(), j.ID, store.JobSucceeded, time.Time{}, "", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("finishing a job that is not running: %v", err)
	}
	c3, _ := s.ClaimJobs(ctx(), retry, time.Minute, 10)
	if len(c3) != 1 || c3[0].Attempts != 3 {
		t.Fatalf("claim after back-off: %+v", c3)
	}
	if err := s.FinishJob(ctx(), j.ID, store.JobFailed, time.Time{}, "gave up", retry); err != nil {
		t.Fatal(err)
	}
	counts, _ := s.CountJobs(ctx())
	if counts[store.JobFailed] != 1 || counts[store.JobQueued] != 1 {
		t.Fatalf("CountJobs: %v", counts)
	}
	failed, _ := s.ListJobs(ctx(), store.JobFailed, 10)
	if len(failed) != 1 || failed[0].LastError != "gave up" {
		t.Fatalf("ListJobs failed: %+v", failed)
	}
	if all, _ := s.ListJobs(ctx(), "", 10); len(all) != 2 {
		t.Fatalf("ListJobs: %d", len(all))
	}
	if n, err := s.PruneJobs(ctx(), retry.Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("PruneJobs: %d %v", n, err)
	}
	if _, err := s.GetJob(ctx(), later.ID); err != nil {
		t.Fatalf("queued jobs are never pruned: %v", err)
	}
}

func testLeases(t *testing.T, s store.Store) {
	now := time.Now().UTC().Truncate(time.Second)
	ok, err := s.AcquireLease(ctx(), "monitor", "a", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("first acquire: %v %v", ok, err)
	}
	if ok, _ := s.AcquireLease(ctx(), "monitor", "b", now.Add(30*time.Second), time.Minute); ok {
		t.Fatal("another holder must not take an unexpired lease")
	}
	if ok, _ := s.AcquireLease(ctx(), "monitor", "a", now.Add(50*time.Second), time.Minute); !ok {
		t.Fatal("the holder renews its lease")
	}
	if ok, _ := s.AcquireLease(ctx(), "monitor", "b", now.Add(100*time.Second), time.Minute); ok {
		t.Fatal("the renewed lease is still held")
	}
	if ok, _ := s.AcquireLease(ctx(), "monitor", "b", now.Add(111*time.Second), time.Minute); !ok {
		t.Fatal("an expired lease is taken over")
	}
	if ok, _ := s.AcquireLease(ctx(), "janitor", "a", now, time.Minute); !ok {
		t.Fatal("leases are independent")
	}
	// "b" holds monitor, "a" holds janitor: releasing "a" frees only janitor.
	if err := s.ReleaseLeases(ctx(), "a"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.AcquireLease(ctx(), "janitor", "c", now.Add(time.Second), time.Minute); !ok {
		t.Fatal("a released lease is taken at once")
	}
	if ok, _ := s.AcquireLease(ctx(), "monitor", "c", now.Add(112*time.Second), time.Minute); ok {
		t.Fatal("other holders keep their leases")
	}
}

func testAlertDelivery(t *testing.T, s store.Store) {
	org := store.DefaultOrganizationID
	ev := &store.AlertEvent{ID: auth.NewID(), OrganizationID: org, RuleName: "r", ServerName: "s", Metric: "cpu", State: "firing", Pending: 3}
	if err := s.InsertAlertEvent(ctx(), ev); err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct {
		ok  bool
		err string
	}{{true, ""}, {false, "hook: HTTP 404"}, {false, "mail: 550 no such user"}} {
		if err := s.RecordAlertDelivery(ctx(), ev.ID, d.ok, d.err); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.GetAlertEvent(ctx(), ev.ID); err != nil || got.Pending != 0 || got.Delivered != 1 || got.RuleName != "r" {
		t.Fatalf("GetAlertEvent: %+v %v", got, err)
	}
	evs, _ := s.ListAlertEvents(ctx(), org, 1)
	if len(evs) != 1 || evs[0].Pending != 0 || evs[0].Delivered != 1 || evs[0].Failed != 2 ||
		evs[0].DeliveryError != "hook: HTTP 404; mail: 550 no such user" {
		t.Fatalf("RecordAlertDelivery: %+v", evs)
	}
	if err := s.RecordAlertDelivery(ctx(), auth.NewID(), true, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown event: %v", err)
	}
}
