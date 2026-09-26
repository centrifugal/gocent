package gocent_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
)

func autoBatch(ab gocent.AutoBatch) func(*gocent.Config) {
	return func(cfg *gocent.Config) { cfg.AutoBatch = ab }
}

func TestAutoBatchSequentialCallsAreNotBatched(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f, autoBatch(gocent.AutoBatch{MaxInFlight: 2}))
	for i := range 5 {
		if _, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	for _, r := range f.recorded() {
		if r.Path != "/api/publish" {
			t.Errorf("sequential call went to %s", r.Path)
		}
	}
}

func TestAutoBatchUnderLoad(t *testing.T) {
	f := newFake(t)
	f.delay = 30 * time.Millisecond
	c := newClient(t, f, autoBatch(gocent.AutoBatch{MaxInFlight: 2}))

	const calls = 60
	var wg sync.WaitGroup
	errs := make([]error, calls)
	results := make([]gocent.PublishResult, calls)
	for i := range calls {
		wg.Go(func() {
			ch := "ch" + strconv.Itoa(i)
			if i%10 == 0 {
				ch = "bad" + strconv.Itoa(i)
			}
			results[i], errs[i] = c.Publish(t.Context(), gocent.PublishRequest{Channel: ch, Data: data})
		})
	}
	wg.Wait()

	for i := range calls {
		if i%10 == 0 {
			if !errors.Is(errs[i], gocent.ErrUnknownChannel) {
				t.Errorf("call %d: error %v, want its own ErrUnknownChannel", i, errs[i])
			}
			continue
		}
		// Each call gets its own reply: the epoch names its channel.
		if errs[i] != nil || results[i].Epoch != "e-ch"+strconv.Itoa(i) {
			t.Errorf("call %d: %+v, %v", i, results[i], errs[i])
		}
	}
	if got := f.maxInFlight.Load(); got > 2 {
		t.Errorf("%d requests in flight at once, MaxInFlight is 2", got)
	}
	reqs := f.recorded()
	batches := 0
	for _, r := range reqs {
		if r.Path == "/api/batch" {
			batches++
		}
	}
	if batches == 0 || len(reqs) >= calls {
		t.Errorf("%d requests, %d of them batches, for %d calls: expected calls to be batched", len(reqs), batches, calls)
	}
}

func TestAutoBatchMaxBatchSize(t *testing.T) {
	f := newFake(t)
	f.delay = 20 * time.Millisecond
	c := newClient(t, f, autoBatch(gocent.AutoBatch{MaxInFlight: 1, MaxBatchSize: 5}))
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			if _, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, r := range f.recorded() {
		if r.Path != "/api/batch" {
			continue
		}
		var req struct {
			Commands []jsontext.Value `json:"commands"`
		}
		if err := json.Unmarshal(r.Body, &req); err != nil {
			t.Fatal(err)
		}
		if len(req.Commands) > 5 {
			t.Errorf("batch of %d commands, MaxBatchSize is 5", len(req.Commands))
		}
	}
}

func TestAutoBatchGroupPublications(t *testing.T) {
	f := newFake(t)
	f.delay = 20 * time.Millisecond
	c := newClient(t, f, autoBatch(gocent.AutoBatch{MaxInFlight: 1, GroupPublications: true}))
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for _, r := range f.recorded() {
		if r.Path == "/api/batch" && !strings.Contains(string(r.Body), `"group_publications":true`) {
			t.Errorf("automatic batch without group_publications: %s", r.Body)
		}
	}
}

func TestAutoBatchCallerLeavesQueue(t *testing.T) {
	f := newFake(t)
	f.delay = 200 * time.Millisecond
	c := newClient(t, f, autoBatch(gocent.AutoBatch{MaxInFlight: 1}))

	// Occupy the only slot.
	done := make(chan error, 1)
	go func() {
		_, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "first", Data: data})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Publish(ctx, gocent.PublishRequest{Channel: "abandoned", Data: data})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "before it was sent") {
		t.Fatalf("error %v, want DeadlineExceeded before sending", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The next call goes out directly: the abandoned one does not linger.
	if _, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "after", Data: data}); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.recorded() {
		if strings.Contains(string(r.Body), "abandoned") {
			t.Errorf("abandoned call was sent to %s", r.Path)
		}
	}
}

func TestAutoBatchBroadcastPartialFailure(t *testing.T) {
	f := newFake(t)
	f.delay = 20 * time.Millisecond
	c := newClient(t, f, autoBatch(gocent.AutoBatch{MaxInFlight: 1}))
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			res, err := c.Broadcast(t.Context(), gocent.BroadcastRequest{
				Channels: []string{"ok" + strconv.Itoa(i), "bad" + strconv.Itoa(i)}, Data: data,
			})
			var be *gocent.BroadcastError
			if !errors.As(err, &be) || len(be.Failed) != 1 || be.Failed[0].Channel != "bad"+strconv.Itoa(i) {
				t.Errorf("broadcast %d: %v", i, err)
			}
			if len(res.Channels) != 2 || res.Channels[0].Err != nil || res.Channels[1].Err == nil {
				t.Errorf("broadcast %d: result %+v", i, res.Channels)
			}
		})
	}
	wg.Wait()
}

// A caller whose context ends after its call went into a batch returns at
// once, while the batch is still on its way. The request it made must be sent
// as it was when the call was made, however the caller reuses its memory
// after returning.
func TestAutoBatchCallerMemoryIsNotReadAfterReturn(t *testing.T) {
	f := newFake(t)
	f.delay = 30 * time.Millisecond
	var calls atomic.Int32
	c := newClient(t, f, autoBatch(gocent.AutoBatch{MaxInFlight: 1}), func(cfg *gocent.Config) {
		cfg.APIEndpoint = ""
		cfg.APIEndpointFunc = func(context.Context) (string, error) {
			if calls.Add(1) > 1 {
				// The batch: hold it back between being taken and being
				// encoded, well past the caller's deadline.
				time.Sleep(60 * time.Millisecond)
			}
			return f.addr(), nil
		}
	})

	first := make(chan error, 1)
	go func() {
		_, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "first", Data: data})
		first <- err
	}()
	time.Sleep(10 * time.Millisecond) // the first call holds the only slot

	channels := []string{"user:1", "user:2"}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	_, err := c.Broadcast(ctx, gocent.BroadcastRequest{Channels: channels, Data: data})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Broadcast: %v, want the deadline to end it while batched", err)
	}
	channels[0] = "reused" // the caller's memory is its own again

	if err := <-first; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, r := range f.recorded() {
			if r.Path != "/api/batch" {
				continue
			}
			if !strings.Contains(string(r.Body), `"user:1"`) || strings.Contains(string(r.Body), "reused") {
				t.Fatalf("batch sent %s, want the channels as they were when the call was made", r.Body)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the batch was never sent")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
