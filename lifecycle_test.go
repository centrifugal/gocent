package gocent_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
)

// waitForRequests waits until the fake has n requests in progress.
func waitForRequests(t *testing.T, f *fakeCentrifugo, n int64) {
	t.Helper()
	for range 500 {
		if f.inFlight.Load() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the fake never had %d requests in progress", n)
}

// Close waits for a call in progress, which succeeds, and refuses calls made
// after it.
func TestCloseWaitsForCalls(t *testing.T) {
	f := newFake(t)
	f.delay = 150 * time.Millisecond
	c := newClient(t, f)
	done := make(chan error, 1)
	go func() {
		_, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data})
		done <- err
	}()
	waitForRequests(t, f, 1)
	if err := c.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the call in progress: %v", err)
		}
	default:
		t.Fatal("Close returned before the call in progress ended")
	}
	if _, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data}); !errors.Is(err, gocent.ErrClosed) || gocent.Retryable(err) {
		t.Errorf("after Close: %v", err)
	}
	b := c.NewBatch(gocent.BatchOptions{})
	p := b.Publish(gocent.PublishRequest{Channel: "news", Data: data})
	res, err := b.Send(t.Context())
	if !errors.Is(err, gocent.ErrClosed) || res.Replied || len(res.Failed()) != 1 {
		t.Errorf("batch after Close: %+v, %v", res, err)
	}
	if _, err := p.Result(); !errors.Is(err, gocent.ErrClosed) {
		t.Errorf("command of a batch after Close: %v", err)
	}
	// Again: nothing left to wait for.
	if err := c.Close(t.Context()); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// Calls queued for an automatic batch when Close is called are still sent.
func TestCloseDrainsAutoBatch(t *testing.T) {
	f := newFake(t)
	f.delay = 50 * time.Millisecond
	c := newClient(t, f, autoBatch(gocent.AutoBatch{MaxInFlight: 1}))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data})
			errs <- err
		})
	}
	waitForRequests(t, f, 1)
	time.Sleep(10 * time.Millisecond) // the others queue meanwhile
	if err := c.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a call accepted before Close: %v", err)
		}
	}
}

// When its context ends first, Close returns, and the call in progress goes
// on.
func TestCloseContextEnds(t *testing.T) {
	f := newFake(t)
	f.delay = 300 * time.Millisecond
	c := newClient(t, f)
	done := make(chan error, 1)
	go func() {
		_, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data})
		done <- err
	}()
	waitForRequests(t, f, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := c.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close: %v, want the context's error", err)
	}
	if err := <-done; err != nil {
		t.Errorf("the call in progress: %v", err)
	}
}

// A broadcast being retried stops at its next attempt once the client is
// closed, rather than keeping Close waiting.
func TestCloseStopsRetries(t *testing.T) {
	f := newFake(t)
	f.flakyFailures = 100
	c := newClient(t, f)
	closed := make(chan error, 1)
	opts := gocent.BroadcastRetryOptions{MaxAttempts: 10, OnAttemptErr: func(ctx context.Context, _ gocent.BroadcastAttempt) error {
		go func() { closed <- c.Close(ctx) }()
		return gocent.Wait(ctx, 20*time.Millisecond)
	}}
	res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: []string{"flaky"}, Data: data}, opts)
	if res.Attempts > 2 {
		t.Errorf("%d attempts after Close", res.Attempts)
	}
	if !errors.Is(res.Channels[0].Err, gocent.ErrClosed) || gocent.Retryable(err) {
		t.Errorf("got %v, channel %v", err, res.Channels[0].Err)
	}
	if err := <-closed; err != nil {
		t.Errorf("Close: %v", err)
	}
}

// OnRequest learns of every request: the method, the commands it carried and
// how many failed, whether it was an automatic batch, its status and error.
func TestOnRequest(t *testing.T) {
	f := newFake(t)
	var mu sync.Mutex
	var infos []gocent.RequestInfo
	c := newClient(t, f, func(cfg *gocent.Config) {
		cfg.OnRequest = func(ctx context.Context, info gocent.RequestInfo) {
			if ctx.Value(ctxKey{}) == nil && !info.AutoBatch {
				t.Errorf("%s: OnRequest did not get the caller's context", info.Method)
			}
			mu.Lock()
			infos = append(infos, info)
			mu.Unlock()
		}
	})
	ctx := context.WithValue(t.Context(), ctxKey{}, "caller")
	_, _ = c.Publish(ctx, gocent.PublishRequest{Channel: "news", Data: data})
	_, _ = c.Publish(ctx, gocent.PublishRequest{Channel: "bad", Data: data})
	b := c.NewBatch(gocent.BatchOptions{})
	b.Publish(gocent.PublishRequest{Channel: "a", Data: data})
	b.Publish(gocent.PublishRequest{Channel: "bad", Data: data})
	b.PresenceStats(gocent.PresenceStatsRequest{Channel: "a"})
	_, _ = b.Send(ctx)
	f.unavailable.Store(1)
	_, unavailableErr := c.Publish(ctx, gocent.PublishRequest{Channel: "news", Data: data})

	want := []gocent.RequestInfo{
		{Method: "publish", Commands: 1, StatusCode: http.StatusOK},
		{Method: "publish", Commands: 1, Failed: 1, StatusCode: http.StatusOK},
		{Method: "batch", Commands: 3, Failed: 1, StatusCode: http.StatusOK},
		{Method: "publish", Commands: 1, StatusCode: http.StatusServiceUnavailable, Err: unavailableErr},
	}
	if len(infos) != len(want) {
		t.Fatalf("%d requests reported, want %d: %+v", len(infos), len(want), infos)
	}
	for i, got := range infos {
		w := want[i]
		if got.Method != w.Method || got.Commands != w.Commands || got.Failed != w.Failed || got.AutoBatch || got.StatusCode != w.StatusCode ||
			got.Duration <= 0 || (w.Err == nil) != (got.Err == nil) {
			t.Errorf("request %d: %+v, want %+v", i, got, w)
		}
	}
	if _, ok := errors.AsType[*gocent.HTTPError](infos[3].Err); !ok {
		t.Errorf("the 503 reported %v", infos[3].Err)
	}
}

// An automatic batch is reported once, as one, and BatchContext makes its
// request's context from its callers'.
func TestBatchContext(t *testing.T) {
	f := newFake(t)
	f.delay = 30 * time.Millisecond
	var mu sync.Mutex
	var batches []gocent.RequestInfo
	var seen []any
	c := newClient(t, f, autoBatch(gocent.AutoBatch{MaxInFlight: 1}), func(cfg *gocent.Config) {
		cfg.BatchContext = func(ctx context.Context, callers []context.Context) context.Context {
			ids := make([]any, len(callers))
			for i, cc := range callers {
				ids[i] = cc.Value(ctxKey{})
			}
			return context.WithValue(ctx, ctxKey{}, ids)
		}
		cfg.OnRequest = func(ctx context.Context, info gocent.RequestInfo) {
			if !info.AutoBatch {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			batches = append(batches, info)
			seen = append(seen, ctx.Value(ctxKey{}))
			if _, ok := ctx.Deadline(); !ok {
				t.Error("the batch's context lost its deadline")
			}
		}
	})
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Go(func() {
			ctx := context.WithValue(t.Context(), ctxKey{}, i)
			if _, err := c.Publish(ctx, gocent.PublishRequest{Channel: "news", Data: data}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(batches) == 0 {
		t.Fatal("no automatic batch was sent")
	}
	for i, info := range batches {
		ids, ok := seen[i].([]any)
		if info.Method != "batch" || info.Commands < 1 || !ok || len(ids) != info.Commands {
			t.Errorf("batch %d: %+v, context value %v", i, info, seen[i])
		}
	}
}
