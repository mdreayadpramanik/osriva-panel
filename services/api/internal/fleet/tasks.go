package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agentproto"
	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
	"github.com/mdreayadpramanik/osriva-panel/pkg/metrics"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const (
	taskQueueTTL     = 2 * time.Minute
	metricsRetention = 24 * time.Hour
	maxTasksPerPoll  = 10
)

// notifier wakes long polls when a task is queued for their server.
type notifier struct {
	mu sync.Mutex
	ch map[string]chan struct{}
}

func (n *notifier) wait(serverID string) <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ch == nil {
		n.ch = map[string]chan struct{}{}
	}
	c, ok := n.ch[serverID]
	if !ok {
		c = make(chan struct{})
		n.ch[serverID] = c
	}
	return c
}

func (n *notifier) wake(serverID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if c, ok := n.ch[serverID]; ok {
		close(c)
		delete(n.ch, serverID)
	}
}

// QueueTask validates and queues a typed task for a server.
func (s *Service) QueueTask(ctx context.Context, actor *identity.Principal, serverID, typ string, params json.RawMessage) (*store.Task, error) {
	if err := agenttask.Validate(typ, params); err != nil {
		return nil, &identity.ValidationError{Field: "task", Message: strings.TrimPrefix(err.Error(), agenttask.ErrInvalid.Error()+": ")}
	}
	if err := s.id.Require(ctx, actor, classPermission[agenttask.Class(typ)]); err != nil {
		return nil, err
	}
	if agenttask.NeedsReauth(typ, params) {
		if err := s.id.RequireRecentReauth(actor); err != nil {
			return nil, err
		}
	}
	x, err := s.getInOrg(ctx, actor, serverID)
	if err != nil {
		return nil, err
	}
	if x.Status != store.ServerActive {
		return nil, &identity.PolicyError{Message: "this server's agent is not enrolled or has been revoked"}
	}
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	now := s.id.Now()
	t := &store.Task{ID: auth.NewID(), ServerID: x.ID, Type: typ, Params: params, RequestedBy: &actor.User.ID,
		CreatedAt: now, ExpiresAt: now.Add(taskQueueTTL), RunTimeout: agenttask.RunTimeout(typ)}
	if err := s.store.CreateTask(ctx, t); err != nil {
		return nil, err
	}
	s.id.Audit(ctx, actor.User, "servers.task", "server", x.ID, store.OutcomeSuccess,
		map[string]any{"task_id": t.ID, "type": typ, "params": RedactedParams(params), "server": x.Name})
	s.notify.wake(x.ID)
	return t, nil
}

// RedactedParams returns task parameters safe to log or display: file contents
// are replaced by their size, because configuration files often hold secrets.
func RedactedParams(params json.RawMessage) map[string]any {
	md := map[string]any{}
	_ = json.Unmarshal(params, &md)
	redactMap(md)
	return md
}

// needsRedaction reports whether stored parameters hold secrets to drop once the task ends.
func needsRedaction(params json.RawMessage) bool {
	raw, _ := json.Marshal(RedactedParams(params))
	var a, b any
	_ = json.Unmarshal(params, &a)
	_ = json.Unmarshal(raw, &b)
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) != string(y)
}

func redactMap(md map[string]any) {
	if c, ok := md["content"].(string); ok {
		delete(md, "content")
		md["content_bytes"] = len(c)
	}
	// Container environment variables often carry passwords: keep only the names.
	if env, ok := md["env"].(map[string]any); ok {
		names := make([]string, 0, len(env))
		for k := range env {
			names = append(names, k)
		}
		sort.Strings(names)
		delete(md, "env")
		md["env_names"] = names
	}
	for k := range md {
		if l := strings.ToLower(k); strings.Contains(l, "password") || strings.HasSuffix(l, "_key") {
			md[k] = "[redacted]"
		}
	}
	for _, v := range md {
		if sub, ok := v.(map[string]any); ok {
			redactMap(sub)
		}
	}
}

func (s *Service) taskInOrg(ctx context.Context, actor *identity.Principal, serverID, taskID string) (*store.Task, error) {
	if err := s.id.Require(ctx, actor, rbac.ServersRead); err != nil {
		return nil, err
	}
	if _, err := s.getInOrg(ctx, actor, serverID); err != nil {
		return nil, err
	}
	t, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if t.ServerID != serverID {
		return nil, store.ErrNotFound
	}
	// File task results can contain file contents.
	if agenttask.FileTask(t.Type) {
		if err := s.id.Require(ctx, actor, rbac.ServersFiles); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func finished(t *store.Task) bool {
	return t.Status == store.TaskSucceeded || t.Status == store.TaskFailed || t.Status == store.TaskExpired
}

// GetTask returns a task, optionally waiting up to wait for it to finish.
// It polls storage, so it works across API instances.
func (s *Service) GetTask(ctx context.Context, actor *identity.Principal, serverID, taskID string, wait time.Duration) (*store.Task, error) {
	t, err := s.taskInOrg(ctx, actor, serverID, taskID)
	if err != nil || wait <= 0 {
		return t, err
	}
	deadline := time.Now().Add(wait)
	for !finished(t) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return t, nil
		case <-time.After(250 * time.Millisecond):
		}
		if t, err = s.store.GetTask(ctx, taskID); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (s *Service) ListTasks(ctx context.Context, actor *identity.Principal, serverID string, limit int) ([]store.Task, error) {
	if err := s.id.Require(ctx, actor, rbac.ServersRead); err != nil {
		return nil, err
	}
	if _, err := s.getInOrg(ctx, actor, serverID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	return s.store.ListTasks(ctx, serverID, limit)
}

// PollTasks hands queued tasks to an agent, holding the request for up to wait.
func (s *Service) PollTasks(ctx context.Context, x *store.Server, wait time.Duration) ([]store.Task, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		woken := s.notify.wait(x.ID) // register before claiming so no wake-up is missed
		tasks, err := s.store.ClaimTasks(ctx, x.ID, s.id.Now(), maxTasksPerPoll)
		if err != nil || len(tasks) > 0 {
			return tasks, err
		}
		select {
		case <-woken:
		case <-deadline.C:
			return nil, nil
		case <-ctx.Done():
			return nil, nil
		}
	}
}

// CompleteTask records an agent's result.
func (s *Service) CompleteTask(ctx context.Context, x *store.Server, taskID string, r agentproto.TaskResult) error {
	var status store.TaskStatus
	switch r.Status {
	case "succeeded":
		status = store.TaskSucceeded
	case "failed":
		status = store.TaskFailed
		if r.Error == "" {
			return &identity.ValidationError{Field: "error", Message: "is required when status is failed"}
		}
	default:
		return &identity.ValidationError{Field: "status", Message: "must be succeeded or failed"}
	}
	if len(r.Result) > agentproto.MaxResultBytes {
		return &identity.ValidationError{Field: "result", Message: "is too large"}
	}
	if len(r.Result) > 0 && !json.Valid(r.Result) {
		return &identity.ValidationError{Field: "result", Message: "must be JSON"}
	}
	err := s.store.CompleteTask(ctx, x.ID, taskID, status, r.Result, truncate(r.Error, 2000), s.id.Now())
	if errors.Is(err, store.ErrNotFound) {
		return &identity.PolicyError{Message: "no running task with this id for this server"}
	}
	if err != nil {
		return err
	}
	t, _ := s.store.GetTask(ctx, taskID)
	md := map[string]any{"task_id": taskID, "status": r.Status}
	if t != nil {
		md["type"] = t.Type
		// The agent has the secrets it needed; do not keep them at rest.
		if needsRedaction(t.Params) {
			if red, err := json.Marshal(RedactedParams(t.Params)); err == nil {
				if err := s.store.RedactTaskParams(ctx, taskID, red); err != nil {
					s.log.Warn("redact task params", "task_id", taskID, "err", err)
				}
			}
		}
	}
	outcome := store.OutcomeSuccess
	if status == store.TaskFailed {
		outcome, md["error"] = store.OutcomeFailure, truncate(r.Error, 300)
	}
	s.id.Audit(ctx, nil, "agent.task_result", "server", x.ID, outcome, md)
	return nil
}

// IngestMetrics stores a batch of samples from an agent.
func (s *Service) IngestMetrics(ctx context.Context, x *store.Server, req agentproto.MetricsRequest) error {
	if len(req.Samples) > agentproto.MaxMetricsBatch {
		return &identity.ValidationError{Field: "samples", Message: fmt.Sprintf("at most %d per batch", agentproto.MaxMetricsBatch)}
	}
	now := s.id.Now()
	out := make([]store.MetricSample, 0, len(req.Samples))
	for _, m := range req.Samples {
		// Reject samples from a badly skewed agent clock rather than storing misleading history.
		if m.TS.After(now.Add(2*time.Minute)) || m.TS.Before(now.Add(-metricsRetention)) {
			continue
		}
		m.TS = m.TS.UTC().Truncate(time.Second)
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		out = append(out, store.MetricSample{ServerID: x.ID, TS: m.TS, Data: raw})
	}
	return s.store.InsertMetrics(ctx, out)
}

// Metrics returns the samples of the last `window` (at most 24 h).
func (s *Service) Metrics(ctx context.Context, actor *identity.Principal, serverID string, window time.Duration) ([]metrics.Sample, error) {
	if err := s.id.Require(ctx, actor, rbac.ServersRead); err != nil {
		return nil, err
	}
	if _, err := s.getInOrg(ctx, actor, serverID); err != nil {
		return nil, err
	}
	if window <= 0 || window > metricsRetention {
		window = time.Hour
	}
	rows, err := s.store.ListMetrics(ctx, serverID, s.id.Now().Add(-window), 9000)
	if err != nil {
		return nil, err
	}
	out := make([]metrics.Sample, 0, len(rows))
	for _, r := range rows {
		var m metrics.Sample
		if json.Unmarshal(r.Data, &m) == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// RunJanitor expires stale tasks and prunes old metrics until ctx ends.
func (s *Service) RunJanitor(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		now := s.id.Now()
		if n, err := s.store.ExpireTasks(ctx, now); err != nil {
			s.log.Warn("expire tasks", "err", err)
		} else if n > 0 {
			s.log.Info("expired agent tasks", "count", n)
		}
		if _, err := s.store.PruneMetrics(ctx, now.Add(-metricsRetention)); err != nil {
			s.log.Warn("prune metrics", "err", err)
		}
		s.RunBackupSchedule(ctx, now)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// classPermission maps task permission classes to RBAC permissions.
var classPermission = map[string]string{
	agenttask.ClassRead: rbac.ServersRead,
	// File contents can hold secrets, so even reading needs its own permission.
	agenttask.ClassFiles:   rbac.ServersFiles,
	agenttask.ClassOperate: rbac.ServersOperate,
	// Adding or removing SSH keys grants or revokes login access to the server.
	agenttask.ClassAccess: rbac.ServersAccess,
}

// MetricRollups returns hourly aggregates for windows longer than the 24 h of raw samples (up to 90 days).
func (s *Service) MetricRollups(ctx context.Context, actor *identity.Principal, serverID string, window time.Duration) ([]json.RawMessage, error) {
	if err := s.id.Require(ctx, actor, rbac.ServersRead); err != nil {
		return nil, err
	}
	if _, err := s.getInOrg(ctx, actor, serverID); err != nil {
		return nil, err
	}
	if window > 90*24*time.Hour {
		window = 90 * 24 * time.Hour
	}
	rows, err := s.store.ListRollups(ctx, serverID, s.id.Now().Add(-window))
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, len(rows))
	for i := range rows {
		out[i] = rows[i].Data
	}
	return out, nil
}
