package memstore

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func copyTask(t *store.Task) *store.Task {
	c := *t
	c.Params, c.Result = bytes.Clone(t.Params), bytes.Clone(t.Result)
	c.StartedAt, c.FinishedAt = timePtr(t.StartedAt), timePtr(t.FinishedAt)
	return &c
}

func (s *Store) CreateTask(_ context.Context, t *store.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servers[t.ServerID]; !ok {
		return store.ErrNotFound
	}
	if _, dup := s.tasks[t.ID]; dup {
		return store.ErrConflict
	}
	if t.Status == "" {
		t.Status = store.TaskQueued
	}
	if len(t.Params) == 0 {
		t.Params = []byte("{}")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	s.tasks[t.ID] = copyTask(t)
	return nil
}

func (s *Store) GetTask(_ context.Context, id string) (*store.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyTask(t), nil
}

func sortTasks(ts []store.Task, newestFirst bool) {
	sort.Slice(ts, func(i, j int) bool {
		if !ts[i].CreatedAt.Equal(ts[j].CreatedAt) {
			return ts[i].CreatedAt.After(ts[j].CreatedAt) == newestFirst
		}
		return ts[i].ID < ts[j].ID
	})
}

func (s *Store) ListTasks(_ context.Context, serverID string, limit int) ([]store.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Task{}
	for _, t := range s.tasks {
		if t.ServerID == serverID {
			out = append(out, *copyTask(t))
		}
	}
	sortTasks(out, true)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) ClaimTasks(_ context.Context, serverID string, now time.Time, limit int) ([]store.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var queued []*store.Task
	for _, t := range s.tasks {
		if t.ServerID == serverID && t.Status == store.TaskQueued && now.Before(t.ExpiresAt) {
			queued = append(queued, t)
		}
	}
	sort.Slice(queued, func(i, j int) bool { return queued[i].CreatedAt.Before(queued[j].CreatedAt) })
	out := []store.Task{}
	for _, t := range queued {
		if limit > 0 && len(out) >= limit {
			break
		}
		at := now
		t.Status, t.StartedAt = store.TaskRunning, &at
		out = append(out, *copyTask(t))
	}
	return out, nil
}

func (s *Store) CompleteTask(_ context.Context, serverID, taskID string, status store.TaskStatus, result json.RawMessage, errMsg string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok || t.ServerID != serverID || t.Status != store.TaskRunning {
		return store.ErrNotFound
	}
	at := now
	t.Status, t.Result, t.Error, t.FinishedAt = status, bytes.Clone(result), errMsg, &at
	return nil
}

func (s *Store) ExpireTasks(_ context.Context, now, runningBefore time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range s.tasks {
		at := now
		switch {
		case t.Status == store.TaskQueued && !now.Before(t.ExpiresAt):
			t.Status, t.FinishedAt, t.Error = store.TaskExpired, &at, "not picked up by the agent in time"
			n++
		case t.Status == store.TaskRunning && t.StartedAt != nil && t.StartedAt.Before(runningBefore):
			t.Status, t.FinishedAt, t.Error = store.TaskFailed, &at, "timeout: the agent did not report a result"
			n++
		}
	}
	return n, nil
}

func (s *Store) InsertMetrics(_ context.Context, samples []store.MetricSample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range samples {
		if _, ok := s.servers[m.ServerID]; !ok {
			return store.ErrNotFound
		}
		list := s.metrics[m.ServerID]
		i := sort.Search(len(list), func(i int) bool { return !list[i].TS.Before(m.TS) })
		if i < len(list) && list[i].TS.Equal(m.TS) {
			continue // duplicate timestamp: keep the first, like ON CONFLICT DO NOTHING
		}
		c := m
		c.Data = bytes.Clone(m.Data)
		list = append(list, store.MetricSample{})
		copy(list[i+1:], list[i:])
		list[i] = c
		s.metrics[m.ServerID] = list
	}
	return nil
}

func (s *Store) ListMetrics(_ context.Context, serverID string, since time.Time, limit int) ([]store.MetricSample, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.MetricSample{}
	for _, m := range s.metrics[serverID] {
		if m.TS.After(since) {
			c := m
			c.Data = bytes.Clone(m.Data)
			out = append(out, c)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:] // keep the newest
	}
	return out, nil
}

func (s *Store) PruneMetrics(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, list := range s.metrics {
		kept := list[:0]
		for _, m := range list {
			if m.TS.Before(before) {
				n++
				continue
			}
			kept = append(kept, m)
		}
		s.metrics[id] = kept
	}
	return n, nil
}
