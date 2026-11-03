package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store/memstore"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func newClock() *clock               { return &clock{t: time.Now().UTC().Add(time.Hour)} }
func ctx() context.Context           { return context.Background() }
func (h *handler) GiveUp(_ context.Context, j Job, err error) {
	h.mu.Lock()
	h.gaveUp = append(h.gaveUp, err)
	h.mu.Unlock()
}

type handler struct {
	mu       sync.Mutex
	errs     []error // returned in order; nil after the list ends
	attempts []int
	gaveUp   []error
}

func (h *handler) Run(_ context.Context, j Job) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attempts = append(h.attempts, j.Attempt)
	if len(h.errs) == 0 {
		return nil
	}
	err := h.errs[0]
	h.errs = h.errs[1:]
	return err
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	for i, w := range want {
		if got := Backoff(i + 1); got != w {
			t.Errorf("Backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if Backoff(40) != time.Hour {
		t.Errorf("Backoff is capped at one hour: %v", Backoff(40))
	}
}

func TestRetryThenSucceed(t *testing.T) {
	st, c := memstore.New(), newClock()
	q := New(st, nil, c.now)
	h := &handler{errs: []error{errors.New("HTTP 503"), errors.New("timeout")}}
	q.Register("k", h)
	id, err := q.Enqueue(ctx(), "k", map[string]string{"x": "y"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := q.RunOnce(ctx()); n != 1 {
		t.Fatalf("first run: %d", n)
	}
	j, _ := st.GetJob(ctx(), id)
	if j.Status != store.JobQueued || j.LastError != "HTTP 503" || !j.RunAt.Equal(c.now().Add(30*time.Second)) {
		t.Fatalf("after a failure the job waits 30 s: %+v", j)
	}
	if n := q.RunOnce(ctx()); n != 0 {
		t.Fatal("the job must not run before its back-off")
	}
	c.add(30 * time.Second)
	q.RunOnce(ctx())
	c.add(time.Minute)
	q.RunOnce(ctx())
	j, _ = st.GetJob(ctx(), id)
	if j.Status != store.JobSucceeded || j.Attempts != 3 || len(h.gaveUp) != 0 {
		t.Fatalf("third attempt succeeds: %+v %v", j, h.gaveUp)
	}
	if len(h.attempts) != 3 || h.attempts[2] != 3 {
		t.Fatalf("handler sees the attempt number: %v", h.attempts)
	}
}

func TestGiveUpAfterMaxAttempts(t *testing.T) {
	st, c := memstore.New(), newClock()
	q := New(st, nil, c.now)
	boom := errors.New("connection refused")
	h := &handler{errs: []error{boom, boom, boom}}
	q.Register("k", h)
	id, _ := q.Enqueue(ctx(), "k", nil, 2)
	q.RunOnce(ctx())
	c.add(time.Minute)
	q.RunOnce(ctx())
	j, _ := st.GetJob(ctx(), id)
	if j.Status != store.JobFailed || j.Attempts != 2 || len(h.gaveUp) != 1 || !errors.Is(h.gaveUp[0], boom) {
		t.Fatalf("gives up after 2 attempts: %+v %v", j, h.gaveUp)
	}
	c.add(time.Hour)
	if q.RunOnce(ctx()) != 0 {
		t.Fatal("a failed job never runs again")
	}
}

func TestPermanentErrorStopsRetries(t *testing.T) {
	st, c := memstore.New(), newClock()
	q := New(st, nil, c.now)
	h := &handler{errs: []error{Permanent(errors.New("HTTP 404"))}}
	q.Register("k", h)
	id, _ := q.Enqueue(ctx(), "k", nil, 0)
	q.RunOnce(ctx())
	j, _ := st.GetJob(ctx(), id)
	if j.Status != store.JobFailed || j.Attempts != 1 || len(h.gaveUp) != 1 || !IsPermanent(h.gaveUp[0]) {
		t.Fatalf("permanent error: %+v %v", j, h.gaveUp)
	}
}

type panicker struct{ handler }

func (p *panicker) Run(context.Context, Job) error { panic("bug") }

func TestPanicAndUnknownKind(t *testing.T) {
	st, c := memstore.New(), newClock()
	q := New(st, nil, c.now)
	p := &panicker{}
	q.Register("p", p)
	a, _ := q.Enqueue(ctx(), "p", nil, 0)
	b, _ := q.Enqueue(ctx(), "nobody", nil, 0)
	q.RunOnce(ctx())
	for _, id := range []string{a, b} {
		if j, _ := st.GetJob(ctx(), id); j.Status != store.JobFailed || j.LastError == "" {
			t.Fatalf("job %s: %+v", id, j)
		}
	}
	if len(p.gaveUp) != 1 {
		t.Fatalf("panicking handler is told: %v", p.gaveUp)
	}
}

func TestLessor(t *testing.T) {
	st, c := memstore.New(), newClock()
	a, b := NewLessor(st, nil), NewLessor(st, nil)
	if a.Holder() == b.Holder() {
		t.Fatal("holders must differ")
	}
	if !a.Hold(ctx(), "monitor", c.now(), time.Minute) || b.Hold(ctx(), "monitor", c.now(), time.Minute) {
		t.Fatal("only one process holds the lease")
	}
	c.add(2 * time.Minute)
	if !b.Hold(ctx(), "monitor", c.now(), time.Minute) || a.Hold(ctx(), "monitor", c.now(), time.Minute) {
		t.Fatal("the lease moves when the holder stops renewing")
	}
}
