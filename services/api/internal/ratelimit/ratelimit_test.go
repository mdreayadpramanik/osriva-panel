package ratelimit

import (
	"testing"
	"time"
)

func TestKeyedLimiterBurstThenRefill(t *testing.T) {
	now := time.Unix(1000, 0)
	k := NewKeyed(1, 3)
	k.now = func() time.Time { return now }
	for i := range 3 {
		if !k.Allow("ip") {
			t.Fatalf("request %d within burst denied", i)
		}
	}
	if k.Allow("ip") {
		t.Fatal("request beyond burst allowed")
	}
	if !k.Allow("other-ip") {
		t.Fatal("keys must be independent")
	}
	now = now.Add(time.Second)
	if !k.Allow("ip") {
		t.Fatal("token should refill after 1s")
	}
}

func TestLockout(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLockout(3, time.Minute, 5*time.Minute)
	l.SetClock(func() time.Time { return now })

	l.Fail("a")
	l.Fail("a")
	if l.Locked("a") {
		t.Fatal("locked before threshold")
	}
	if !l.Fail("a") || !l.Locked("a") {
		t.Fatal("not locked at threshold")
	}
	if l.Locked("b") {
		t.Fatal("unrelated key locked")
	}
	now = now.Add(5*time.Minute + time.Second)
	if l.Locked("a") {
		t.Fatal("lock did not expire")
	}
}

func TestLockoutWindowSlides(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLockout(3, time.Minute, time.Hour)
	l.SetClock(func() time.Time { return now })
	l.Fail("a")
	l.Fail("a")
	now = now.Add(2 * time.Minute) // earlier failures fall out of the window
	if l.Fail("a") {
		t.Fatal("stale failures counted")
	}
	l.Reset("a")
	if l.Locked("a") {
		t.Fatal("reset did not clear")
	}
}
