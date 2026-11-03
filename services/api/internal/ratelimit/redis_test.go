package ratelimit

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// testRedis returns a client for OSRIVA_TEST_REDIS_URL (CI runs a Redis service).
func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	u := os.Getenv("OSRIVA_TEST_REDIS_URL")
	if u == "" {
		t.Skip("OSRIVA_TEST_REDIS_URL not set")
	}
	c, err := NewRedisClient(u)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func uniqueName(t *testing.T) string {
	return t.Name() + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func TestRedisLimiterSharedBurst(t *testing.T) {
	c := testRedis(t)
	name := uniqueName(t)
	// Two API instances share one bucket: 5 requests in total, not 5 each.
	a := NewRedisLimiter(c, name, 0.5, 5, NewKeyed(100, 100), nil)
	b := NewRedisLimiter(c, name, 0.5, 5, NewKeyed(100, 100), nil)
	allowed := 0
	for i := 0; i < 10; i++ {
		l := a
		if i%2 == 1 {
			l = b
		}
		if l.Allow("203.0.113.7") {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("allowed %d of 10, want the burst of 5", allowed)
	}
	if !a.Allow("203.0.113.8") {
		t.Fatal("keys are independent")
	}
	// Refill: one token per 2 s.
	fast := NewRedisLimiter(c, name+"f", 20, 1, NewKeyed(100, 100), nil)
	if !fast.Allow("k") || fast.Allow("k") {
		t.Fatal("burst of 1")
	}
	time.Sleep(80 * time.Millisecond)
	if !fast.Allow("k") {
		t.Fatal("a token is refilled after 1/20 s")
	}
	keys, _ := c.Keys(context.Background(), "osriva:rl:"+name+":*").Result()
	for _, k := range keys {
		if len(k) > 0 && strings.Contains(k, "203.0.113") {
			t.Fatalf("client IPs must not appear in Redis keys: %s", k)
		}
		if ttl, _ := c.PTTL(context.Background(), k).Result(); ttl <= 0 {
			t.Fatalf("limiter keys expire: %s %v", k, ttl)
		}
	}
}

func TestRedisLockoutShared(t *testing.T) {
	c := testRedis(t)
	key := uniqueName(t) + "@example.com"
	a := NewRedisLockout(c, 3, time.Minute, 300*time.Millisecond, NewLockout(100, time.Minute, time.Minute), nil)
	b := NewRedisLockout(c, 3, time.Minute, 300*time.Millisecond, NewLockout(100, time.Minute, time.Minute), nil)
	if a.Fail(key) || b.Fail(key) {
		t.Fatal("not locked before the threshold")
	}
	if !a.Fail(key) {
		t.Fatal("third failure (across instances) locks the account")
	}
	if !b.Locked(key) {
		t.Fatal("the lock is visible to every instance")
	}
	time.Sleep(400 * time.Millisecond)
	if a.Locked(key) {
		t.Fatal("the lock expires")
	}
	a.Fail(key)
	a.Reset(key)
	if a.Fail(key) || a.Fail(key) {
		t.Fatal("Reset clears the failures")
	}
}

func TestRedisUnavailableFallsBack(t *testing.T) {
	// Nothing listens on port 1: every call fails fast and the local limiter decides.
	c, err := NewRedisClient("redis://127.0.0.1:1/0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	l := NewRedisLimiter(c, "fb", 0.1, 2, NewKeyed(0.1, 2), nil)
	got := []bool{l.Allow("k"), l.Allow("k"), l.Allow("k")}
	if !got[0] || !got[1] || got[2] {
		t.Fatalf("fallback limiter must still limit: %v", got)
	}
	lo := NewRedisLockout(c, 2, time.Minute, time.Minute, NewLockout(2, time.Minute, time.Minute), nil)
	lo.Fail("u")
	if !lo.Fail("u") || !lo.Locked("u") {
		t.Fatal("fallback lockout must still lock")
	}
	if _, err := NewRedisClient("http://example.com"); err == nil {
		t.Fatal("non-redis URL accepted")
	}
}
