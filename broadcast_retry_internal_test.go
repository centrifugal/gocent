package gocent

import (
	"testing"
	"time"
)

// A wait is between half of the backoff and all of it, and not always the
// same: clients which failed together spread their retries.
func TestJitter(t *testing.T) {
	const d = 100 * time.Millisecond
	seen := map[time.Duration]bool{}
	for range 1000 {
		w := jitter(d)
		if w < d/2 || w > d {
			t.Fatalf("jitter(%v) = %v, want between %v and %v", d, w, d/2, d)
		}
		seen[w] = true
	}
	if len(seen) < 10 {
		t.Errorf("%d distinct waits in 1000, want them spread", len(seen))
	}
	if w := jitter(0); w != 0 {
		t.Errorf("jitter(0) = %v", w)
	}
}
