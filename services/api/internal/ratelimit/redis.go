package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisTimeout bounds each Redis call so a slow Redis never stalls a login.
const redisTimeout = 300 * time.Millisecond

// NewRedisClient parses a redis:// or rediss:// URL.
func NewRedisClient(rawURL string) (*redis.Client, error) {
	opt, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("OSRIVA_REDIS_URL: %w", err)
	}
	opt.DialTimeout, opt.ReadTimeout, opt.WriteTimeout = 2*time.Second, time.Second, time.Second
	return redis.NewClient(opt), nil
}

// keyHash keeps client IPs and e-mail addresses out of Redis and makes the
// key safe to use inside a cluster hash tag.
func keyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

// fallbackWarner logs at most once a minute while Redis is unavailable.
type fallbackWarner struct {
	mu   sync.Mutex
	last time.Time
	log  *slog.Logger
	name string
}

func (w *fallbackWarner) warn(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if time.Since(w.last) < time.Minute {
		return
	}
	w.last = time.Now()
	w.log.Warn("redis unavailable: using this instance's in-memory limiter", "limiter", w.name, "err", err)
}

// GCRA (token bucket) with Redis' own clock, so API hosts with skewed clocks agree.
var gcraScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local interval = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local tat = tonumber(redis.call('GET', KEYS[1]) or '0')
if tat < now then tat = now end
local new_tat = tat + interval
if new_tat - now > interval * burst then return 0 end
redis.call('SET', KEYS[1], string.format('%.0f', new_tat), 'PX', math.ceil((new_tat - now) / 1000) + 1)
return 1`)

// RedisLimiter is a token bucket per key shared by every API instance.
type RedisLimiter struct {
	rdb        redis.Scripter
	prefix     string
	intervalUS int64
	burst      int
	fallback   Limiter
	warn       *fallbackWarner
}

// NewRedisLimiter allows perSecond events per key with the given burst.
// fallback is used for a request when Redis cannot be reached.
func NewRedisLimiter(rdb redis.Scripter, name string, perSecond float64, burst int, fallback Limiter, log *slog.Logger) *RedisLimiter {
	if log == nil {
		log = slog.Default()
	}
	return &RedisLimiter{rdb: rdb, prefix: "osriva:rl:" + name + ":", intervalUS: int64(1e6 / perSecond), burst: burst,
		fallback: fallback, warn: &fallbackWarner{log: log, name: name}}
}

func (l *RedisLimiter) Allow(key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
	defer cancel()
	n, err := gcraScript.Run(ctx, l.rdb, []string{l.prefix + keyHash(key)}, l.intervalUS, l.burst).Int()
	if err != nil {
		l.warn.warn(err)
		return l.fallback.Allow(key)
	}
	return n == 1
}

var lockoutFailScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 1 then return 1 end
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[2]) end
if n >= tonumber(ARGV[1]) then
  redis.call('SET', KEYS[2], '1', 'PX', ARGV[3])
  redis.call('DEL', KEYS[1])
  return 1
end
return 0`)

// RedisLockout counts failures in a fixed window and locks the key for lockFor
// once threshold failures were seen, shared by every API instance.
type RedisLockout struct {
	rdb       redis.Cmdable
	prefix    string
	threshold int
	window    time.Duration
	lockFor   time.Duration
	fallback  Locker
	warn      *fallbackWarner
}

func NewRedisLockout(rdb redis.Cmdable, threshold int, window, lockFor time.Duration, fallback Locker, log *slog.Logger) *RedisLockout {
	if log == nil {
		log = slog.Default()
	}
	return &RedisLockout{rdb: rdb, prefix: "osriva:lockout:", threshold: threshold, window: window, lockFor: lockFor,
		fallback: fallback, warn: &fallbackWarner{log: log, name: "lockout"}}
}

func (l *RedisLockout) keys(key string) (fail, lock string) {
	h := "{" + keyHash(key) + "}"
	return l.prefix + "fail:" + h, l.prefix + "lock:" + h
}

func (l *RedisLockout) Locked(key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
	defer cancel()
	_, lock := l.keys(key)
	n, err := l.rdb.Exists(ctx, lock).Result()
	if err != nil {
		l.warn.warn(err)
		return l.fallback.Locked(key)
	}
	return n == 1
}

func (l *RedisLockout) Fail(key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
	defer cancel()
	fail, lock := l.keys(key)
	n, err := lockoutFailScript.Run(ctx, l.rdb, []string{fail, lock}, l.threshold,
		strconv.FormatInt(l.window.Milliseconds(), 10), strconv.FormatInt(l.lockFor.Milliseconds(), 10)).Int()
	if err != nil {
		l.warn.warn(err)
		return l.fallback.Fail(key)
	}
	return n == 1
}

func (l *RedisLockout) Reset(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
	defer cancel()
	fail, lock := l.keys(key)
	if err := l.rdb.Del(ctx, fail, lock).Err(); err != nil {
		l.warn.warn(err)
	}
	l.fallback.Reset(key)
}
