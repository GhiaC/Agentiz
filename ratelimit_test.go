package agentize

import (
	"testing"
	"time"
)

func TestIPRateLimiter_BurstThenRefill(t *testing.T) {
	// 60/min => 1 token/s; burst 10.
	l := newIPRateLimiter(60, 10)
	base := time.Unix(1_700_000_000, 0)
	ip := "1.2.3.4"

	// The full burst passes at the same instant.
	for i := 0; i < 10; i++ {
		if !l.allowAt(ip, base) {
			t.Fatalf("request %d should be allowed within the burst", i+1)
		}
	}
	// One more at the same instant is denied (bucket empty).
	if l.allowAt(ip, base) {
		t.Fatal("request beyond the burst should be denied")
	}
	// After 1s exactly one token refills (rate = 1/s) → one request allowed.
	if !l.allowAt(ip, base.Add(time.Second)) {
		t.Fatal("one request should be allowed after a 1s refill")
	}
	// ...but not a second one before the next refill.
	if l.allowAt(ip, base.Add(time.Second)) {
		t.Fatal("second request before the next refill should be denied")
	}
}

func TestIPRateLimiter_PerIPIsolation(t *testing.T) {
	l := newIPRateLimiter(10, 1) // burst 1
	now := time.Unix(1_700_000_000, 0)

	if !l.allowAt("a", now) {
		t.Fatal("first request for IP a should pass")
	}
	if l.allowAt("a", now) {
		t.Fatal("second request for IP a should be throttled")
	}
	// A different IP has its own bucket and is unaffected.
	if !l.allowAt("b", now) {
		t.Fatal("first request for IP b should pass independently")
	}
}

func TestIPRateLimiter_GCReclaimsIdleBuckets(t *testing.T) {
	l := newIPRateLimiter(60, 1) // rate 1/s, burst 1 → idleTTL = 1s + 1min
	now := time.Unix(1_700_000_000, 0)

	l.allowAt("x", now)
	// A request from another IP far in the future triggers GC, which reclaims
	// the long-idle bucket for x.
	l.allowAt("y", now.Add(2*time.Hour))

	l.mu.Lock()
	_, stillThere := l.buckets["x"]
	l.mu.Unlock()
	if stillThere {
		t.Fatal("idle bucket x should have been garbage-collected")
	}
}

func TestIPRateLimiter_ClampsNonPositiveArgs(t *testing.T) {
	l := newIPRateLimiter(0, 0) // clamps to 1/min, burst 1
	now := time.Unix(1_700_000_000, 0)
	if !l.allowAt("z", now) {
		t.Fatal("a limiter built from non-positive args must still permit some traffic")
	}
	if l.allowAt("z", now) {
		t.Fatal("burst clamps to 1, so the immediate second request is denied")
	}
}
