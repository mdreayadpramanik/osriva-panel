// Package ratelimit provides in-process rate limiting. State is per API
// instance; multi-instance deployments need a shared backend (planned: Redis).
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxKeys bounds memory use; idle entries are pruned when it is exceeded.
const maxKeys = 50_000

// KeyedLimiter is a token bucket per key (e.g. client IP).
type KeyedLimiter struct {
	mu      sync.Mutex
	limit   rate.Limit
	burst   int
	idle    time.Duration
	entries map[string]*keyedEntry
	now     func() time.Time
}

type keyedEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func NewKeyed(perSecond float64, burst int) *KeyedLimiter {
	return &KeyedLimiter{
		limit: rate.Limit(perSecond), burst: burst, idle: 10 * time.Minute,
		entries: map[string]*keyedEntry{}, now: time.Now,
	}
}

func (k *KeyedLimiter) Allow(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	e, ok := k.entries[key]
	if !ok {
		if len(k.entries) >= maxKeys {
			k.pruneLocked(now)
		}
		e = &keyedEntry{lim: rate.NewLimiter(k.limit, k.burst)}
		k.entries[key] = e
	}
	e.seen = now
	return e.lim.AllowN(now, 1)
}

func (k *KeyedLimiter) pruneLocked(now time.Time) {
	for key, e := range k.entries {
		if now.Sub(e.seen) > k.idle {
			delete(k.entries, key)
		}
	}
}

// Lockout counts failures per key in a sliding window and locks the key once
// the threshold is reached.
type Lockout struct {
	mu        sync.Mutex
	threshold int
	window    time.Duration
	lockFor   time.Duration
	entries   map[string]*lockEntry
	now       func() time.Time
}

type lockEntry struct {
	failures    []time.Time
	lockedUntil time.Time
}

func NewLockout(threshold int, window, lockFor time.Duration) *Lockout {
	return &Lockout{threshold: threshold, window: window, lockFor: lockFor, entries: map[string]*lockEntry{}, now: time.Now}
}

// SetClock overrides the time source (tests).
func (l *Lockout) SetClock(now func() time.Time) { l.now = now }

// Locked reports whether key is currently locked out.
func (l *Lockout) Locked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	return ok && l.now().Before(e.lockedUntil)
}

// Fail records a failure and reports whether the key is now locked.
func (l *Lockout) Fail(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[key]
	if !ok {
		if len(l.entries) >= maxKeys {
			l.pruneLocked(now)
		}
		e = &lockEntry{}
		l.entries[key] = e
	}
	cut := now.Add(-l.window)
	kept := e.failures[:0]
	for _, t := range e.failures {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	e.failures = append(kept, now)
	if len(e.failures) >= l.threshold {
		e.lockedUntil = now.Add(l.lockFor)
		e.failures = nil
	}
	return now.Before(e.lockedUntil)
}

// Reset clears failures for key (after a successful login).
func (l *Lockout) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

func (l *Lockout) pruneLocked(now time.Time) {
	for key, e := range l.entries {
		if now.After(e.lockedUntil) && (len(e.failures) == 0 || now.Sub(e.failures[len(e.failures)-1]) > l.window) {
			delete(l.entries, key)
		}
	}
}
