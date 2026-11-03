// Package jobs runs durable background jobs from the PostgreSQL job table
// with leases, retries and exponential back-off, and provides leases that let
// one API process run each periodic loop (docs/adr/0012-fleet-groups-queue.md).
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const (
	DefaultMaxAttempts = 6
	// Lease is how long a claimed job is reserved for its worker.
	Lease        = 2 * time.Minute
	firstBackoff = 30 * time.Second
	maxBackoff   = time.Hour
	retention    = 7 * 24 * time.Hour
	claimBatch   = 10
)

// Job is what a handler receives.
type Job struct {
	ID          string
	Payload     json.RawMessage
	Attempt     int // 1-based
	MaxAttempts int
}

// Handler runs one job. Returning an error retries it (unless Permanent).
type Handler interface {
	Run(ctx context.Context, j Job) error
	// GiveUp is called once when the job fails for good (permanent error or attempts exhausted).
	GiveUp(ctx context.Context, j Job, err error)
}

type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// Permanent marks err as not worth retrying.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanent{err}
}

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var p permanent
	return errors.As(err, &p)
}

// Backoff returns the delay before the next try after attempt (1-based) failed.
func Backoff(attempt int) time.Duration {
	d := firstBackoff
	for i := 1; i < attempt && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

type Queue struct {
	store    store.Store
	log      *slog.Logger
	now      func() time.Time
	mu       sync.RWMutex
	handlers map[string]Handler
	wake     chan struct{}
}

func New(st store.Store, log *slog.Logger, now func() time.Time) *Queue {
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Queue{store: st, log: log, now: now, handlers: map[string]Handler{}, wake: make(chan struct{}, 1)}
}

func (q *Queue) Register(kind string, h Handler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers[kind] = h
}

// Enqueue stores a job to run as soon as a worker is free.
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, maxAttempts int) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	j := &store.Job{ID: auth.NewID(), Kind: kind, Payload: raw, MaxAttempts: maxAttempts, RunAt: q.now().UTC()}
	if err := q.store.EnqueueJob(ctx, j); err != nil {
		return "", err
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return j.ID, nil
}

// RunOnce claims and runs the due jobs once; it returns how many ran.
func (q *Queue) RunOnce(ctx context.Context) int {
	now := q.now().UTC()
	claimed, err := q.store.ClaimJobs(ctx, now, Lease, claimBatch)
	if err != nil {
		q.log.Warn("claim jobs", "err", err)
		return 0
	}
	var wg sync.WaitGroup
	for i := range claimed {
		wg.Add(1)
		go func(sj store.Job) {
			defer wg.Done()
			q.run(ctx, sj)
		}(claimed[i])
	}
	wg.Wait()
	return len(claimed)
}

func (q *Queue) run(ctx context.Context, sj store.Job) {
	q.mu.RLock()
	h, ok := q.handlers[sj.Kind]
	q.mu.RUnlock()
	j := Job{ID: sj.ID, Payload: sj.Payload, Attempt: sj.Attempts, MaxAttempts: sj.MaxAttempts}
	var err error
	if !ok {
		err = Permanent(fmt.Errorf("no handler for job kind %q", sj.Kind))
	} else {
		rctx, cancel := context.WithTimeout(ctx, Lease-15*time.Second)
		err = safeRun(rctx, h, j)
		cancel()
	}
	now := q.now().UTC()
	switch {
	case err == nil:
		q.finish(ctx, sj.ID, store.JobSucceeded, time.Time{}, "", now)
	case IsPermanent(err) || sj.Attempts >= sj.MaxAttempts:
		q.finish(ctx, sj.ID, store.JobFailed, time.Time{}, truncate(err.Error()), now)
		if h != nil {
			h.GiveUp(ctx, j, err)
		}
		q.log.Warn("job failed", "id", sj.ID, "kind", sj.Kind, "attempts", sj.Attempts, "err", err)
	default:
		q.finish(ctx, sj.ID, store.JobQueued, now.Add(Backoff(sj.Attempts)), truncate(err.Error()), now)
	}
}

func safeRun(ctx context.Context, h Handler, j Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = Permanent(fmt.Errorf("job handler panicked: %v", r))
		}
	}()
	return h.Run(ctx, j)
}

func (q *Queue) finish(ctx context.Context, id string, st store.JobStatus, retryAt time.Time, msg string, now time.Time) {
	// A lost lease (ErrNotFound) means another worker took the job over; its result wins.
	if err := q.store.FinishJob(ctx, id, st, retryAt, msg, now); err != nil && !errors.Is(err, store.ErrNotFound) {
		q.log.Warn("finish job", "id", id, "err", err)
	}
}

func truncate(s string) string {
	if len(s) > 1000 {
		return s[:1000]
	}
	return s
}

// Run works the queue until ctx ends: it polls every interval, right after an
// Enqueue in this process, and prunes finished jobs hourly.
func (q *Queue) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	var lastPrune time.Time
	for {
		for q.RunOnce(ctx) == claimBatch && ctx.Err() == nil {
		}
		if now := q.now(); now.Sub(lastPrune) >= time.Hour {
			if n, err := q.store.PruneJobs(ctx, now.Add(-retention)); err != nil {
				q.log.Warn("prune jobs", "err", err)
			} else if n > 0 {
				q.log.Info("pruned finished jobs", "count", n)
			}
			lastPrune = now
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-q.wake:
		}
	}
}

// Lessor holds leases on behalf of this process.
type Lessor struct {
	store  store.Store
	holder string
	log    *slog.Logger
}

func NewLessor(st store.Store, log *slog.Logger) *Lessor {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	if log == nil {
		log = slog.Default()
	}
	return &Lessor{store: st, holder: hex.EncodeToString(b), log: log}
}

func (l *Lessor) Holder() string { return l.holder }

// Hold takes or renews the named lease; false means another process runs the loop.
func (l *Lessor) Hold(ctx context.Context, name string, now time.Time, ttl time.Duration) bool {
	ok, err := l.store.AcquireLease(ctx, name, l.holder, now, ttl)
	if err != nil {
		l.log.Warn("lease", "name", name, "err", err)
		return false
	}
	return ok
}

// Status is the queue's health for operators.
type Status struct {
	Counts map[store.JobStatus]int
	// Recent are the newest failed jobs (payloads hold ids only).
	Recent []store.Job
}

func (q *Queue) Status(ctx context.Context) (*Status, error) {
	counts, err := q.store.CountJobs(ctx)
	if err != nil {
		return nil, err
	}
	failed, err := q.store.ListJobs(ctx, store.JobFailed, 20)
	if err != nil {
		return nil, err
	}
	return &Status{Counts: counts, Recent: failed}, nil
}

// ReleaseAll gives up this process's leases on shutdown, so a restarted or
// remaining API process takes over the loops without waiting for them to expire.
func (l *Lessor) ReleaseAll(ctx context.Context) {
	if err := l.store.ReleaseLeases(ctx, l.holder); err != nil {
		l.log.Warn("release leases", "err", err)
	}
}
