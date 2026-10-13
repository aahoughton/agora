package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	l := New(3, time.Minute, 2, func() time.Time { return now })
	for i := range 3 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("event %d refused", i)
		}
	}
	ok, wait := l.Allow("a")
	if ok || wait != 20*time.Second {
		t.Fatalf("fourth event: %v, wait %v", ok, wait)
	}
	// Check does not take an event.
	if ok, _ := l.Check("b"); !ok {
		t.Fatal("check refused a fresh key")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("b refused")
	}
	// Refill over time.
	now = now.Add(20 * time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("a not refilled")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("a refilled too much")
	}
	// A third key evicts the least recently used one (b), which starts
	// over with a full bucket.
	l.Allow("c")
	if _, tracked := l.keys["b"]; tracked {
		t.Fatal("b was not evicted")
	}
	if len(l.keys) != 2 {
		t.Fatalf("tracking %d keys", len(l.keys))
	}
}
