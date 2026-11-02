package storetest

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func newTask(t *testing.T, s store.Store, serverID, typ string, expires time.Time) *store.Task {
	t.Helper()
	x := &store.Task{ID: auth.NewID(), ServerID: serverID, Type: typ, Params: json.RawMessage(`{"a":1}`), ExpiresAt: expires}
	if err := s.CreateTask(ctx(), x); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // distinct created_at
	return x
}

func testTaskQueue(t *testing.T, s store.Store) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a, _ := newServer(t, s, "q-a", now)
	b, _ := newServer(t, s, "q-b", now)

	t1 := newTask(t, s, a.ID, "services.list", now.Add(time.Hour))
	t2 := newTask(t, s, a.ID, "processes.list", now.Add(time.Hour))
	t3 := newTask(t, s, a.ID, "services.list", now.Add(time.Hour))
	newTask(t, s, b.ID, "services.list", now.Add(time.Hour))
	expired := newTask(t, s, a.ID, "services.list", now.Add(-time.Second))

	if err := s.CreateTask(ctx(), &store.Task{ID: auth.NewID(), ServerID: auth.NewID(), Type: "services.list", ExpiresAt: now}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("task for missing server: %v", err)
	}
	got, err := s.GetTask(ctx(), t1.ID)
	if err != nil || got.Status != store.TaskQueued || string(got.Params) == "" || got.CreatedAt.IsZero() {
		t.Fatalf("GetTask: %+v %v", got, err)
	}

	claimed, err := s.ClaimTasks(ctx(), a.ID, now, 2)
	if err != nil || len(claimed) != 2 || claimed[0].ID != t1.ID || claimed[1].ID != t2.ID {
		t.Fatalf("claim oldest first, limit 2: %+v %v", claimed, err)
	}
	if claimed[0].Status != store.TaskRunning || claimed[0].StartedAt == nil {
		t.Fatalf("claimed task not running: %+v", claimed[0])
	}
	again, _ := s.ClaimTasks(ctx(), a.ID, now, 10)
	if len(again) != 1 || again[0].ID != t3.ID {
		t.Fatalf("second claim must not return claimed or expired tasks: %+v", again)
	}

	res := json.RawMessage(`{"services":[]}`)
	if err := s.CompleteTask(ctx(), b.ID, t1.ID, store.TaskSucceeded, res, "", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another server completed the task: %v", err)
	}
	if err := s.CompleteTask(ctx(), a.ID, t1.ID, store.TaskSucceeded, res, "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(ctx(), a.ID, t1.ID, store.TaskFailed, nil, "x", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("completed twice: %v", err)
	}
	if err := s.CompleteTask(ctx(), a.ID, t2.ID, store.TaskFailed, nil, "boom", now); err != nil {
		t.Fatal(err)
	}
	done, _ := s.GetTask(ctx(), t1.ID)
	var m map[string]any
	if done.Status != store.TaskSucceeded || done.FinishedAt == nil || json.Unmarshal(done.Result, &m) != nil {
		t.Fatalf("succeeded task: %+v", done)
	}
	failed, _ := s.GetTask(ctx(), t2.ID)
	if failed.Status != store.TaskFailed || failed.Error != "boom" || len(failed.Result) != 0 {
		t.Fatalf("failed task: %+v", failed)
	}

	// A long task (20 min run timeout) survives the 5-minute default.
	long := &store.Task{ID: auth.NewID(), ServerID: a.ID, Type: "app.deploy", ExpiresAt: now.Add(time.Hour), RunTimeout: 20 * time.Minute}
	if err := s.CreateTask(ctx(), long); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.ClaimTasks(ctx(), a.ID, now, 10); len(c) != 1 || c[0].RunTimeout != 20*time.Minute {
		t.Fatalf("claim long task: %+v", c)
	}
	if n, err := s.ExpireTasks(ctx(), now.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("after 1 min only the unclaimed expired task goes: %d %v", n, err)
	}
	// t3 (5 min default) is now past its run timeout; the long task is not.
	n, err := s.ExpireTasks(ctx(), now.Add(6*time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("ExpireTasks = %d, %v (want expired + timed-out)", n, err)
	}
	if x, _ := s.GetTask(ctx(), expired.ID); x.Status != store.TaskExpired || x.FinishedAt == nil {
		t.Fatalf("expired: %+v", x)
	}
	if x, _ := s.GetTask(ctx(), t3.ID); x.Status != store.TaskFailed || x.Error == "" {
		t.Fatalf("timed out: %+v", x)
	}

	if x, _ := s.GetTask(ctx(), long.ID); x.Status != store.TaskRunning {
		t.Fatalf("long task expired early: %+v", x)
	}
	if err := s.RedactTaskParams(ctx(), t1.ID, json.RawMessage(`{"env_names":["A"]}`)); err != nil {
		t.Fatal(err)
	}
	if x, _ := s.GetTask(ctx(), t1.ID); string(x.Params) != `{"env_names": ["A"]}` && string(x.Params) != `{"env_names":["A"]}` {
		t.Fatalf("redacted params: %s", x.Params)
	}

	list, _ := s.ListTasks(ctx(), a.ID, 2)
	if len(list) != 2 || list[0].ID != long.ID {
		t.Fatalf("ListTasks newest first with limit: %+v", list)
	}
}

func testMetrics(t *testing.T, s store.Store) {
	now := time.Now().UTC().Truncate(time.Second)
	a, _ := newServer(t, s, "m-a", now)
	var samples []store.MetricSample
	for i := 0; i < 5; i++ {
		samples = append(samples, store.MetricSample{ServerID: a.ID, TS: now.Add(time.Duration(i) * 10 * time.Second),
			Data: json.RawMessage(fmt.Sprintf(`{"cpu_percent":%d}`, i))})
	}
	if err := s.InsertMetrics(ctx(), samples); err != nil {
		t.Fatal(err)
	}
	// Re-sending an overlapping batch (agent retry) must not duplicate or fail.
	if err := s.InsertMetrics(ctx(), samples[3:]); err != nil {
		t.Fatalf("duplicate batch: %v", err)
	}
	all, _ := s.ListMetrics(ctx(), a.ID, now.Add(-time.Second), 0)
	if len(all) != 5 || !all[0].TS.Equal(now) {
		t.Fatalf("list all oldest first: %d", len(all))
	}
	recent, _ := s.ListMetrics(ctx(), a.ID, now.Add(-time.Second), 2)
	var last map[string]float64
	if len(recent) != 2 || json.Unmarshal(recent[1].Data, &last) != nil || last["cpu_percent"] != 4 {
		t.Fatalf("newest two: %+v", recent)
	}
	since, _ := s.ListMetrics(ctx(), a.ID, now.Add(25*time.Second), 0)
	if len(since) != 2 {
		t.Fatalf("since filter: %d", len(since))
	}
	n, err := s.PruneMetrics(ctx(), now.Add(15*time.Second))
	if err != nil || n != 2 {
		t.Fatalf("prune = %d %v", n, err)
	}
	if left, _ := s.ListMetrics(ctx(), a.ID, time.Time{}, 0); len(left) != 3 {
		t.Fatalf("after prune: %d", len(left))
	}
	if err := s.InsertMetrics(ctx(), []store.MetricSample{{ServerID: auth.NewID(), TS: now, Data: json.RawMessage(`{}`)}}); err == nil {
		t.Fatal("metrics for a missing server accepted")
	}
}
