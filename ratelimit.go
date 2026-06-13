package agentize

import (
	"sync"
	"time"
)

// ipRateLimiter is a small per-IP token-bucket limiter used to throttle
// sensitive endpoints (e.g. raw user-file downloads) even for authenticated
// admins. It needs no external dependency and is safe for concurrent use.
//
// Each IP gets a bucket that refills at `rate` tokens/second up to `burst`
// tokens; each allowed request costs one token. Idle buckets are reclaimed so
// the map cannot grow without bound under fileID/IP enumeration.
type ipRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // tokens added per second
	burst   float64 // bucket capacity (max tokens)
	lastGC  time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// newIPRateLimiter builds a limiter that allows an initial burst of `burst`
// requests, then `perMinute` sustained requests per minute, per IP. Non-positive
// arguments are clamped to 1 so the limiter always permits some traffic.
func newIPRateLimiter(perMinute, burst int) *ipRateLimiter {
	if perMinute < 1 {
		perMinute = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &ipRateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    float64(perMinute) / 60.0,
		burst:   float64(burst),
	}
}

// allow reports whether a request from ip may proceed now, consuming one token
// if so. It uses the current wall clock; allowAt is the testable core.
func (l *ipRateLimiter) allow(ip string) bool {
	return l.allowAt(ip, time.Now())
}

// allowAt is allow with an injectable clock, for deterministic tests.
func (l *ipRateLimiter) allowAt(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.gc(now)

	b := l.buckets[ip]
	if b == nil {
		// First request from this IP: start with a full bucket, consume one.
		l.buckets[ip] = &tokenBucket{tokens: l.burst - 1, last: now}
		return true
	}

	// Refill based on elapsed time since last seen, capped at burst.
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// gc drops buckets idle long enough to have fully refilled (indistinguishable
// from a fresh bucket), keeping the map bounded by recently-active IPs. It runs
// at most once a minute. Caller must hold l.mu.
func (l *ipRateLimiter) gc(now time.Time) {
	if !l.lastGC.IsZero() && now.Sub(l.lastGC) < time.Minute {
		return
	}
	l.lastGC = now

	idleTTL := time.Hour
	if l.rate > 0 {
		// Time to refill from empty to full, plus a minute of slack.
		idleTTL = time.Duration((l.burst/l.rate)*float64(time.Second)) + time.Minute
	}
	for ip, b := range l.buckets {
		if now.Sub(b.last) > idleTTL {
			delete(l.buckets, ip)
		}
	}
}
