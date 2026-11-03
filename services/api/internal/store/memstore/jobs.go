package memstore

import (
	"bytes"
	"context"
	"sort"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

type lease struct {
	holder  string
	expires time.Time
}

func copyJob(j *store.Job) *store.Job {
	c := *j
	c.Payload = bytes.Clone(j.Payload)
	c.LockedUntil = timePtr(j.LockedUntil)
	return &c
}

func (s *Store) EnqueueJob(_ context.Context, j *store.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.jobs[j.ID]; dup {
		return store.ErrConflict
	}
	now := time.Now().UTC()
	j.Status, j.Attempts, j.LockedUntil, j.CreatedAt, j.UpdatedAt = store.JobQueued, 0, nil, now, now
	if len(j.Payload) == 0 {
		j.Payload = []byte("{}")
	}
	if j.RunAt.IsZero() {
		j.RunAt = now
	}
	s.jobs[j.ID] = copyJob(j)
	return nil
}

func jobDue(j *store.Job, now time.Time) bool {
	switch j.Status {
	case store.JobQueued:
		return !j.RunAt.After(now)
	case store.JobRunning:
		return j.LockedUntil != nil && !j.LockedUntil.After(now)
	}
	return false
}

func (s *Store) ClaimJobs(_ context.Context, now time.Time, lease time.Duration, limit int) ([]store.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []*store.Job
	for _, j := range s.jobs {
		if jobDue(j, now) {
			due = append(due, j)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].RunAt.Before(due[j].RunAt) })
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	out := make([]store.Job, 0, len(due))
	until := now.Add(lease)
	for _, j := range due {
		j.Status, j.Attempts, j.LockedUntil, j.UpdatedAt = store.JobRunning, j.Attempts+1, &until, now
		out = append(out, *copyJob(j))
	}
	return out, nil
}

func (s *Store) FinishJob(_ context.Context, id string, status store.JobStatus, retryAt time.Time, errMsg string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok || j.Status != store.JobRunning {
		return store.ErrNotFound
	}
	j.Status, j.LockedUntil, j.LastError, j.UpdatedAt = status, nil, errMsg, now
	if status == store.JobQueued {
		j.RunAt = retryAt
	}
	return nil
}

func (s *Store) GetJob(_ context.Context, id string) (*store.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyJob(j), nil
}

func (s *Store) ListJobs(_ context.Context, status store.JobStatus, limit int) ([]store.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Job{}
	for _, j := range s.jobs {
		if status == "" || j.Status == status {
			out = append(out, *copyJob(j))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) CountJobs(context.Context) (map[store.JobStatus]int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[store.JobStatus]int{}
	for _, j := range s.jobs {
		out[j.Status]++
	}
	return out, nil
}

func (s *Store) PruneJobs(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, j := range s.jobs {
		if (j.Status == store.JobSucceeded || j.Status == store.JobFailed) && j.UpdatedAt.Before(before) {
			delete(s.jobs, id)
			n++
		}
	}
	return n, nil
}

func (s *Store) AcquireLease(_ context.Context, name, holder string, now time.Time, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.leases[name]; ok && l.holder != holder && l.expires.After(now) {
		return false, nil
	}
	s.leases[name] = lease{holder: holder, expires: now.Add(ttl)}
	return true, nil
}
