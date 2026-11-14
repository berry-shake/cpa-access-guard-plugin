package policy

import (
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	limiter := NewRateLimiterWithClock(func() time.Time { return now })
	if !limiter.Allow("team-a", 2) {
		t.Fatal("first request denied")
	}
	if !limiter.Allow("team-a", 2) {
		t.Fatal("second request denied")
	}
	if limiter.Allow("team-a", 2) {
		t.Fatal("third request allowed")
	}
	now = now.Add(time.Minute)
	if !limiter.Allow("team-a", 2) {
		t.Fatal("request after window denied")
	}
}

// Snapshot returns every bucket's in-minute count (test helper).
func (l *RateLimiter) Snapshot() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]int, len(l.buckets))
	for key, bucket := range l.buckets {
		out[key] = bucket.count
	}
	return out
}
