package gocent_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
)

// constant waits d between attempts.
func constant(d time.Duration) gocent.BroadcastAttemptErrFunc {
	return func(ctx context.Context, _ gocent.BroadcastAttempt) error { return gocent.Wait(ctx, d) }
}

// fast retries without waiting long between attempts.
var fast = gocent.BroadcastRetryOptions{OnAttemptErr: constant(time.Millisecond)}

// broadcastAttempts returns, for each broadcast request the fake got, its
// channels and idempotency key.
func broadcastAttempts(t *testing.T, f *fakeCentrifugo) (channels [][]string, keys []string) {
	t.Helper()
	for _, r := range f.recorded() {
		if !strings.HasSuffix(r.Path, "/broadcast") {
			continue
		}
		var req struct {
			Channels       []string `json:"channels"`
			IdempotencyKey string   `json:"idempotency_key"`
		}
		if err := json.Unmarshal(r.Body, &req); err != nil {
			t.Fatal(err)
		}
		channels = append(channels, req.Channels)
		keys = append(keys, req.IdempotencyKey)
	}
	return channels, keys
}

func TestBroadcastWithRetryRecovers(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 1
	c := newClient(t, f)
	res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "flaky", "b"}, Data: data}, fast)
	if err != nil {
		t.Fatalf("BroadcastWithRetry: %v", err)
	}
	for _, ch := range res.Channels {
		if ch.Err != nil || ch.Result.Offset != 1 {
			t.Errorf("%s: %+v", ch.Channel, ch)
		}
	}
	// Two requests; the flaky channel was sent twice, the others once.
	if res.Attempts != 2 || res.Channels[0].Attempts != 1 || res.Channels[1].Attempts != 2 || res.Channels[2].Attempts != 1 {
		t.Errorf("attempts: result %d, channels %d %d %d", res.Attempts, res.Channels[0].Attempts, res.Channels[1].Attempts, res.Channels[2].Attempts)
	}
	channels, keys := broadcastAttempts(t, f)
	if want := [][]string{{"a", "flaky", "b"}, {"flaky"}}; !slices.EqualFunc(channels, want, slices.Equal) {
		t.Errorf("attempts sent %v, want %v", channels, want)
	}
	// A key was made for the call, every attempt carried it, and the result
	// says which, for a retry later.
	if !strings.HasPrefix(keys[0], "gocent-") || keys[1] != keys[0] || res.IdempotencyKey != keys[0] {
		t.Errorf("idempotency keys %q, result's %q", keys, res.IdempotencyKey)
	}
}

// A channel which failed for good is not retried, and is the one left failed.
func TestBroadcastWithRetryLeavesPermanentFailures(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 1
	c := newClient(t, f)
	res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"bad", "flaky"}, Data: data}, fast)
	be, ok := errors.AsType[*gocent.BroadcastFailedChannelsError](err)
	if !ok || be.Total != 2 || len(be.Failed) != 1 || be.Failed[0].Channel != "bad" {
		t.Fatalf("got %v, want a BroadcastFailedChannelsError for bad only", err)
	}
	if gocent.Retryable(err) {
		t.Error("only a permanent failure is left, yet Retryable is true")
	}
	if !errors.Is(res.Channels[0].Err, gocent.ErrUnknownChannel) || res.Channels[1].Err != nil || res.Channels[1].Result.Offset != 1 {
		t.Errorf("result %+v", res.Channels)
	}
	if channels, _ := broadcastAttempts(t, f); len(channels) != 2 || !slices.Equal(channels[1], []string{"flaky"}) {
		t.Errorf("attempts sent %v", channels)
	}
}

func TestBroadcastWithRetryGivesUp(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 100
	c := newClient(t, f)
	opts := fast
	opts.MaxAttempts = 3
	res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "flaky"}, Data: data}, opts)
	be, ok := errors.AsType[*gocent.BroadcastFailedChannelsError](err)
	if !ok || len(be.Failed) != 1 || be.Failed[0].Channel != "flaky" || !gocent.Retryable(err) {
		t.Fatalf("got %v, want a retryable BroadcastFailedChannelsError for flaky", err)
	}
	// The channel which succeeded in the first attempt keeps its result.
	if res.Channels[0].Err != nil || res.Channels[0].Result.Offset != 1 {
		t.Errorf("channel a: %+v", res.Channels[0])
	}
	if res.Attempts != 3 || res.Channels[0].Attempts != 1 || res.Channels[1].Attempts != 3 || be.Failed[0].Attempts != 3 {
		t.Errorf("attempts: result %d, a %d, flaky %d", res.Attempts, res.Channels[0].Attempts, res.Channels[1].Attempts)
	}
	if failed := res.Failed(); len(failed) != 1 || failed[0].Channel != "flaky" {
		t.Errorf("Failed() = %+v", failed)
	}
	if channels, _ := broadcastAttempts(t, f); len(channels) != 3 {
		t.Errorf("%d attempts, want 3", len(channels))
	}
}

// A broadcast which failed as a whole for a temporary reason is sent again,
// to every channel, with the same key.
func TestBroadcastWithRetryWholeFailure(t *testing.T) {
	f := newFake(t)
	f.unavailable.Store(1)
	c := newClient(t, f)
	res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "b"}, Data: data}, fast)
	if err != nil {
		t.Fatalf("BroadcastWithRetry: %v", err)
	}
	if res.Attempts != 2 || res.Channels[0].Attempts != 2 || res.Channels[1].Attempts != 2 || res.Failed() != nil {
		t.Errorf("attempts %d, channels %+v", res.Attempts, res.Channels)
	}
	channels, keys := broadcastAttempts(t, f)
	if len(channels) != 2 || !slices.Equal(channels[1], []string{"a", "b"}) || keys[1] != keys[0] {
		t.Errorf("attempts sent %v with keys %q", channels, keys)
	}
}

// When no attempt got a reply, the error is the last one, as from Broadcast:
// not a BroadcastFailedChannelsError.
func TestBroadcastWithRetryNoReply(t *testing.T) {
	f := newFake(t)
	f.unavailable.Store(10)
	c := newClient(t, f)
	opts := fast
	opts.MaxAttempts = 2
	res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "b"}, Data: data}, opts)
	httpErr, ok := errors.AsType[*gocent.HTTPError](err)
	if !ok || httpErr.StatusCode != http.StatusServiceUnavailable || res.Replied {
		t.Fatalf("got %v, want the 503 of the last attempt", err)
	}
	if len(res.Channels) != 2 || res.Channels[0].Err == nil || res.Channels[1].Err == nil {
		t.Errorf("result %+v", res.Channels)
	}
}

func TestBroadcastWithRetryKeepsCallersKey(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 1
	c := newClient(t, f)
	res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"flaky"}, Data: data, IdempotencyKey: "mine"}, fast)
	if err != nil || res.IdempotencyKey != "mine" {
		t.Fatalf("got key %q, %v", res.IdempotencyKey, err)
	}
	if _, keys := broadcastAttempts(t, f); !slices.Equal(keys, []string{"mine", "mine"}) {
		t.Errorf("idempotency keys %q, want the caller's on every attempt", keys)
	}
}

// When the context ends while waiting, the wait stops it at once: the error
// joins the context's with the broadcast's, and the result holds what the
// attempts so far achieved.
func TestBroadcastWithRetryContextEnds(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 100
	c := newClient(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := c.BroadcastWithRetry(ctx, gocent.BroadcastRequest{Channels: []string{"a", "flaky"}, Data: data}, gocent.BroadcastRetryOptions{OnAttemptErr: constant(time.Hour)})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("returned after %v: the backoff did not end with the context", elapsed)
	}
	if be, ok := errors.AsType[*gocent.BroadcastFailedChannelsError](err); !ok || len(be.Failed) != 1 || be.Failed[0].Channel != "flaky" || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want the context's error and a BroadcastFailedChannelsError for flaky", err)
	}
	if res.Channels[0].Err != nil || !res.Replied {
		t.Errorf("result: %+v", res)
	}
}

// A channel listed twice keeps two entries, each with its own outcome.
func TestBroadcastWithRetryRepeatedChannel(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 1
	c := newClient(t, f)
	res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"flaky", "flaky"}, Data: data}, fast)
	if err != nil || len(res.Channels) != 2 || res.Channels[0].Err != nil || res.Channels[1].Err != nil {
		t.Fatalf("got %+v, %v", res.Channels, err)
	}
	if channels, _ := broadcastAttempts(t, f); len(channels) != 2 || !slices.Equal(channels[1], []string{"flaky"}) {
		t.Errorf("attempts sent %v", channels)
	}
}

func TestBroadcastWithRetryInvalid(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	for _, opts := range []gocent.BroadcastRetryOptions{{MaxAttempts: -1}} {
		if _, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"a"}, Data: data}, opts); !errors.Is(err, gocent.ErrInvalidRequest) {
			t.Errorf("%+v: got %v, want ErrInvalidRequest", opts, err)
		}
	}
	// An invalid request is not retried: it is not sent at all, so it was
	// not attempted.
	res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"a"}}, fast)
	if !errors.Is(err, gocent.ErrInvalidRequest) || res.Attempts != 0 || res.Channels[0].Attempts != 0 {
		t.Errorf("no data: got %v, attempts %d", err, res.Attempts)
	}
	if n := len(f.recorded()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
}

// Broadcast's result carries the request's key too, whatever the outcome.
func TestBroadcastResultIdempotencyKey(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	for _, chs := range [][]string{{"a"}, {"bad"}, {"reject-all"}} {
		res, _ := c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: chs, Data: data, IdempotencyKey: "k"})
		if res.IdempotencyKey != "k" {
			t.Errorf("%v: result's key %q, want the request's", chs, res.IdempotencyKey)
		}
	}
	b := c.NewBatch(gocent.BatchOptions{})
	p := b.Broadcast(gocent.BroadcastRequest{Channels: []string{"a"}, Data: data, IdempotencyKey: "k"})
	_, _ = b.Send(t.Context())
	if res, _ := p.Result(); res.IdempotencyKey != "k" {
		t.Errorf("in a batch: result's key %q", res.IdempotencyKey)
	}
}

// Broadcast is one attempt, and a request refused before sending none.
func TestBroadcastAttempts(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	for _, chs := range [][]string{{"a"}, {"bad"}, {"reject-all"}} {
		res, _ := c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: chs, Data: data})
		if res.Attempts != 1 || res.Channels[0].Attempts != 1 {
			t.Errorf("%v: attempts %d, channel %d", chs, res.Attempts, res.Channels[0].Attempts)
		}
	}
	res, err := c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: []string{"a"}})
	if !errors.Is(err, gocent.ErrInvalidRequest) || res.Attempts != 0 {
		t.Errorf("invalid: %v, attempts %d", err, res.Attempts)
	}
	if res.Failed() == nil {
		t.Error("invalid: Failed() is nil, want every channel")
	}
}

type ctxKey2 struct{}

// OnAttemptErr learns of each failed attempt a retry may fix: with the call's
// own context, the attempt, its error, the channels the next attempt sends,
// every channel's outcome so far and the key. It is not asked after the last
// attempt.
func TestBroadcastWithRetryOnAttemptErr(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 2
	c := newClient(t, f)
	var attempts []gocent.BroadcastAttempt
	opts := gocent.BroadcastRetryOptions{
		MaxAttempts: 4,
		OnAttemptErr: func(ctx context.Context, a gocent.BroadcastAttempt) error {
			if ctx.Value(ctxKey2{}) != "request-7" {
				t.Error("OnAttemptErr did not get the call's context")
			}
			attempts = append(attempts, a)
			return nil
		},
	}
	ctx := context.WithValue(t.Context(), ctxKey2{}, "request-7")
	res, err := c.BroadcastWithRetry(ctx, gocent.BroadcastRequest{Channels: []string{"a", "bad", "flaky"}, Data: data}, opts)
	if be, ok := errors.AsType[*gocent.BroadcastFailedChannelsError](err); !ok || len(be.Failed) != 1 || be.Failed[0].Channel != "bad" {
		t.Fatalf("got %v", err)
	}
	// The flaky channel failed twice: two retries, each of it alone - the
	// unknown channel failed for good and is not sent again.
	if len(attempts) != 2 {
		t.Fatalf("called %d times, want 2", len(attempts))
	}
	for i, a := range attempts {
		_, partial := errors.AsType[*gocent.BroadcastFailedChannelsError](a.Err)
		if a.Attempt != i+1 || !partial || len(a.Retry) != 1 || a.Retry[0].Channel != "flaky" ||
			!gocent.Retryable(a.Retry[0].Err) || a.Retry[0].Attempts != i+1 || a.IdempotencyKey != res.IdempotencyKey {
			t.Errorf("attempt %d: %+v", i, a)
		}
		// The outcome so far: a got it in the first attempt.
		if !a.Result.Replied || a.Result.Channels[0].Err != nil || a.Result.Channels[0].Result.Offset != 1 {
			t.Errorf("attempt %d: result so far %+v", i, a.Result)
		}
	}
}

// An error from OnAttemptErr stops the retries: the call returns it joined
// with the broadcast's error.
func TestBroadcastWithRetryStop(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 100
	c := newClient(t, f)
	errStop := errors.New("circuit open")
	opts := gocent.BroadcastRetryOptions{
		MaxAttempts:  10,
		OnAttemptErr: func(context.Context, gocent.BroadcastAttempt) error { return errStop },
	}
	res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "flaky"}, Data: data}, opts)
	be, ok := errors.AsType[*gocent.BroadcastFailedChannelsError](err)
	if !errors.Is(err, errStop) || !ok || len(be.Failed) != 1 || be.Failed[0].Channel != "flaky" {
		t.Fatalf("got %v, want errStop and a BroadcastFailedChannelsError for flaky", err)
	}
	if res.Attempts != 1 || !res.Replied || res.Channels[0].Err != nil {
		t.Errorf("result %+v", res)
	}
	// Stopped by the caller, the broadcast may still succeed later.
	if !gocent.Retryable(err) {
		t.Errorf("Retryable(%v) = false, want true: flaky failed temporarily", err)
	}
}

// A failure no retry can fix never reaches OnAttemptErr: the call stops by
// itself, and the default allows a single retry.
func TestBroadcastWithRetryOnlyRetryable(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 100
	c := newClient(t, f)
	called := 0
	count := gocent.BroadcastRetryOptions{OnAttemptErr: func(context.Context, gocent.BroadcastAttempt) error { called++; return nil }}
	if _, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"bad"}, Data: data}, count); err == nil || called != 0 {
		t.Errorf("a permanent failure: called %d times, %v", called, err)
	}
	res, _ := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"flaky"}, Data: data}, count)
	if res.Attempts != 2 || called != 1 {
		t.Errorf("default: %d attempts, called %d times; want 2 attempts, 1 call", res.Attempts, called)
	}
}

// AttemptTimeout bounds each attempt: one which hangs times out, and its
// channels are retried within the call's deadline.
func TestBroadcastWithRetryAttemptTimeout(t *testing.T) {
	f := newFake(t)
	f.hang.Store(1)
	c := newClient(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	opts := fast
	opts.AttemptTimeout = 50 * time.Millisecond
	res, err := c.BroadcastWithRetry(ctx, gocent.BroadcastRequest{Channels: []string{"a", "b"}, Data: data}, opts)
	if err != nil || res.Attempts != 2 || res.Channels[0].Attempts != 2 || !res.Replied {
		t.Fatalf("got %v, result %+v; want the hung attempt retried", err, res)
	}
	if ctx.Err() != nil {
		t.Errorf("the call's deadline ended: the attempt was not bounded on its own")
	}
}

// Without AttemptTimeout the call's context bounds each attempt: a hung one
// takes all of it, and once it ends nothing is retried - OnAttemptErr is not
// even called.
func TestBroadcastWithRetryContextEnded(t *testing.T) {
	f := newFake(t)
	f.hang.Store(1)
	c := newClient(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	called := 0
	opts := gocent.BroadcastRetryOptions{MaxAttempts: 5, OnAttemptErr: func(context.Context, gocent.BroadcastAttempt) error {
		called++
		return nil
	}}
	res, err := c.BroadcastWithRetry(ctx, gocent.BroadcastRequest{Channels: []string{"a"}, Data: data}, opts)
	if !errors.Is(err, context.DeadlineExceeded) || res.Attempts != 1 || called != 0 {
		t.Errorf("got %v, %d attempts, OnAttemptErr called %d times", err, res.Attempts, called)
	}
}

func TestBroadcastWithRetryNegativeAttemptTimeout(t *testing.T) {
	c := newClient(t, newFake(t))
	_, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"a"}, Data: data},
		gocent.BroadcastRetryOptions{AttemptTimeout: -time.Second})
	if !errors.Is(err, gocent.ErrInvalidRequest) {
		t.Errorf("got %v, want ErrInvalidRequest", err)
	}
}

// The response's headers, a Retry-After say, reach OnAttemptErr in the
// attempt's *HTTPError.
func TestBroadcastWithRetryResponseHeader(t *testing.T) {
	f := newFake(t)
	f.unavailable.Store(1)
	f.retryAfter = "7"
	c := newClient(t, f)
	var got string
	opts := gocent.BroadcastRetryOptions{OnAttemptErr: func(_ context.Context, a gocent.BroadcastAttempt) error {
		if httpErr, ok := errors.AsType[*gocent.HTTPError](a.Err); ok {
			got = httpErr.Header.Get("Retry-After")
		}
		return nil
	}}
	if _, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"a"}, Data: data}, opts); err != nil {
		t.Fatal(err)
	}
	if got != "7" {
		t.Errorf("Retry-After %q, want 7", got)
	}
}

// A stop error the broadcast's error already holds - the context's, after an
// attempt which failed on the same context - is not named twice.
func TestBroadcastWithRetryStopNotRepeated(t *testing.T) {
	f := newFake(t)
	f.unavailable.Store(1)
	c := newClient(t, f)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opts := gocent.BroadcastRetryOptions{OnAttemptErr: func(context.Context, gocent.BroadcastAttempt) error {
		// The broadcast's error is a 503, which does not hold this one:
		// joined.
		return context.Canceled
	}}
	_, err := c.BroadcastWithRetry(ctx, gocent.BroadcastRequest{Channels: []string{"a"}, Data: data}, opts)
	if !errors.Is(err, context.Canceled) || strings.Count(err.Error(), "context canceled") != 1 {
		t.Errorf("got %q", err)
	}
	f.hang.Store(1)
	stopped := 0
	opts = gocent.BroadcastRetryOptions{AttemptTimeout: 20 * time.Millisecond, OnAttemptErr: func(context.Context, gocent.BroadcastAttempt) error {
		stopped++
		return context.DeadlineExceeded
	}}
	_, err = c.BroadcastWithRetry(ctx, gocent.BroadcastRequest{Channels: []string{"a"}, Data: data}, opts)
	if !errors.Is(err, context.DeadlineExceeded) || stopped != 1 || strings.Count(err.Error(), "deadline exceeded") != 1 {
		t.Errorf("got %q", err)
	}
}
