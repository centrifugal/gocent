package gocent

import (
	"context"
	"errors"
	"math"
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

// The waits double from base up to the limit.
func TestBackoffStep(t *testing.T) {
	const ms = time.Millisecond
	for attempt, want := range map[int]time.Duration{1: 10 * ms, 2: 20 * ms, 3: 40 * ms, 4: 50 * ms, 10: 50 * ms, 1000: 50 * ms} {
		if got := backoffStep(10*ms, 50*ms, attempt); got != want {
			t.Errorf("attempt %d: %v, want %v", attempt, got, want)
		}
	}
}

// ExponentialBackoff's waits are the steps with jitter, from attempt 1 being
// the first failure. A limit below base counts as base.
func TestExponentialBackoff(t *testing.T) {
	const ms = time.Millisecond
	backoff := ExponentialBackoff(10*ms, 40*ms)
	for attempt, step := range map[int]time.Duration{1: 10 * ms, 2: 20 * ms, 3: 40 * ms, 100: 40 * ms} {
		for range 100 {
			if w := backoff(attempt); w < step/2 || w > step {
				t.Fatalf("attempt %d: %v, want between %v and %v", attempt, w, step/2, step)
			}
		}
	}
	if w := ExponentialBackoff(10*ms, ms)(5); w < 5*ms || w > 10*ms {
		t.Errorf("limit below base: %v", w)
	}
	if w := ExponentialBackoff(-time.Second, time.Second)(1); w != 0 {
		t.Errorf("negative base: %v, want 0", w)
	}
	if w := ExponentialBackoff(10*ms, 40*ms)(-5); w < 5*ms || w > 10*ms {
		t.Errorf("attempt below 1: %v, want the first wait", w)
	}
	// Doubling towards the largest Duration must not overflow into a short
	// or negative wait.
	for _, attempt := range []int{40, 64, 1000} {
		if w := ExponentialBackoff(time.Hour, math.MaxInt64)(attempt); w < math.MaxInt64/2 {
			t.Errorf("attempt %d with the largest limit: %v", attempt, w)
		}
	}
}

// Wait returns when the time is up, or with the context's error when the
// context ends first.
func TestWait(t *testing.T) {
	if err := Wait(t.Context(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("ended context: %v", err)
	}
	if err := Wait(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("ended context, no wait: %v", err)
	}
}

// Whatever the inputs, ExponentialBackoff does not panic, and its wait is
// between half of the step and the step, which grows with the attempt up to
// the limit.
func FuzzExponentialBackoff(f *testing.F) {
	f.Add(int64(100*time.Millisecond), int64(5*time.Second), 3)
	f.Add(int64(-time.Second), int64(time.Second), 1)
	f.Add(int64(time.Hour), int64(math.MaxInt64), 100)
	f.Add(int64(0), int64(0), 0)
	f.Add(int64(math.MaxInt64), int64(math.MinInt64), math.MaxInt)
	f.Fuzz(func(t *testing.T, base, limit int64, attempt int) {
		b, l := time.Duration(base), time.Duration(limit)
		w := ExponentialBackoff(b, l)(attempt)
		step := backoffStep(max(b, 0), max(l, b, 0), attempt)
		if step < 0 || step > max(l, b, 0) {
			t.Fatalf("step %v for base %v, limit %v, attempt %d", step, b, l, attempt)
		}
		if w < step/2 || w > step {
			t.Fatalf("wait %v for step %v", w, step)
		}
		if attempt < math.MaxInt {
			if next := backoffStep(max(b, 0), max(l, b, 0), attempt+1); next < step {
				t.Fatalf("step %v after %v: it shrank", next, step)
			}
		}
	})
}
