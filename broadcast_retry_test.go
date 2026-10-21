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

// fast retries without waiting long between attempts.
var fast = gocent.RetryOptions{Backoff: time.Millisecond}

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
	be, ok := errors.AsType[*gocent.BroadcastError](err)
	if !ok || be.Total != 2 || len(be.Failed) != 1 || be.Failed[0].Channel != "bad" {
		t.Fatalf("got %v, want a BroadcastError for bad only", err)
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
	opts.Attempts = 3
	res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "flaky"}, Data: data}, opts)
	be, ok := errors.AsType[*gocent.BroadcastError](err)
	if !ok || len(be.Failed) != 1 || be.Failed[0].Channel != "flaky" || !gocent.Retryable(err) {
		t.Fatalf("got %v, want a retryable BroadcastError for flaky", err)
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
// not a BroadcastError.
func TestBroadcastWithRetryNoReply(t *testing.T) {
	f := newFake(t)
	f.unavailable.Store(10)
	c := newClient(t, f)
	opts := fast
	opts.Attempts = 2
	res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "b"}, Data: data}, opts)
	httpErr, ok := errors.AsType[*gocent.HTTPError](err)
	if !ok || httpErr.StatusCode != http.StatusServiceUnavailable || gocent.WholeFailure(err) == nil {
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

// When the context ends while backing off, it returns at once, with what the
// attempts so far achieved.
func TestBroadcastWithRetryContextEnds(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 100
	c := newClient(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := c.BroadcastWithRetry(ctx, gocent.BroadcastRequest{Channels: []string{"a", "flaky"}, Data: data}, gocent.RetryOptions{Backoff: time.Hour})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("returned after %v: the backoff did not end with the context", elapsed)
	}
	if be, ok := errors.AsType[*gocent.BroadcastError](err); !ok || len(be.Failed) != 1 || be.Failed[0].Channel != "flaky" {
		t.Errorf("got %v, want a BroadcastError for flaky", err)
	}
	if res.Channels[0].Err != nil {
		t.Errorf("channel a lost its result: %+v", res.Channels[0])
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
	for _, opts := range []gocent.RetryOptions{{Attempts: -1}, {Backoff: -time.Second}, {MaxBackoff: -time.Second}} {
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
	_ = b.Send(t.Context())
	if res, _ := p.Result(); res.IdempotencyKey != "k" {
		t.Errorf("in a batch: result's key %q", res.IdempotencyKey)
	}
}

// The waits grow up to MaxBackoff, and no further.
func TestBroadcastWithRetryMaxBackoff(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 100
	c := newClient(t, f)
	start := time.Now()
	_, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"flaky"}, Data: data},
		gocent.RetryOptions{Attempts: 7, Backoff: 20 * time.Millisecond, MaxBackoff: 20 * time.Millisecond})
	if err == nil {
		t.Fatal("want the flaky channel to fail")
	}
	// Six waits of at most 20ms. Doubling without the cap would wait at least
	// 630ms, half of 20+40+...+640, even with the jitter.
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Errorf("took %v: the waits were not capped", elapsed)
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

// OnRetry learns of each retry: the call's own context, the attempt which
// failed, the channels to send again with their errors, the wait and the key.
func TestBroadcastWithRetryOnRetry(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 2
	c := newClient(t, f)
	var events []gocent.RetryEvent
	opts := gocent.RetryOptions{
		Attempts: 4,
		Backoff:  10 * time.Millisecond,
		OnRetry: func(ctx context.Context, e gocent.RetryEvent) {
			if ctx.Value(ctxKey2{}) != "request-7" {
				t.Error("OnRetry did not get the call's context")
			}
			events = append(events, e)
		},
	}
	ctx := context.WithValue(t.Context(), ctxKey2{}, "request-7")
	res, err := c.BroadcastWithRetry(ctx, gocent.BroadcastRequest{Channels: []string{"a", "bad", "flaky"}, Data: data}, opts)
	if be, ok := errors.AsType[*gocent.BroadcastError](err); !ok || len(be.Failed) != 1 || be.Failed[0].Channel != "bad" {
		t.Fatalf("got %v", err)
	}
	// The flaky channel failed twice: two retries, each of it alone - the
	// unknown channel failed for good and is not sent again.
	if len(events) != 2 {
		t.Fatalf("%d events, want 2", len(events))
	}
	for i, e := range events {
		if e.Attempt != i+1 || len(e.Failed) != 1 || e.Failed[0].Channel != "flaky" || !gocent.Retryable(e.Failed[0].Err) ||
			e.Failed[0].Attempts != i+1 || e.IdempotencyKey != res.IdempotencyKey ||
			e.Wait < 5*time.Millisecond<<i || e.Wait > 10*time.Millisecond<<i {
			t.Errorf("event %d: %+v", i, e)
		}
	}
}

// A hook which cancels the call's context stops the retries before the next
// attempt, and the cause tells why.
func TestBroadcastWithRetryStoppedFromOnRetry(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 100
	c := newClient(t, f)
	errStop := errors.New("circuit open")
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	opts := gocent.RetryOptions{
		Backoff: time.Hour,
		OnRetry: func(context.Context, gocent.RetryEvent) { cancel(errStop) },
	}
	start := time.Now()
	res, err := c.BroadcastWithRetry(ctx, gocent.BroadcastRequest{Channels: []string{"flaky"}, Data: data}, opts)
	if time.Since(start) > 5*time.Second || res.Attempts != 1 {
		t.Fatalf("did not stop: attempts %d after %v", res.Attempts, time.Since(start))
	}
	if !errors.Is(context.Cause(ctx), errStop) || err == nil {
		t.Errorf("cause %v, err %v", context.Cause(ctx), err)
	}
}
