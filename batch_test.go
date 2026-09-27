package gocent_test

import (
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/centrifugal/gocent/v4"
)

var data = jsontext.Value(`{"text":"hello"}`)

func TestBroadcast(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	res, err := c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "b"}, Data: data})
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if len(res.Channels) != 2 {
		t.Fatalf("%d channel results, want 2", len(res.Channels))
	}
	for i, ch := range []string{"a", "b"} {
		r := res.Channels[i]
		if r.Channel != ch || r.Err != nil || r.Result.Epoch != "e-"+ch {
			t.Errorf("channel %d: %+v", i, r)
		}
	}
}

func TestBroadcastPartialFailure(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	res, err := c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "bad:1", "b", "full:1"}, Data: data})

	var be *gocent.BroadcastError
	if !errors.As(err, &be) {
		t.Fatalf("error %v, want a *BroadcastError", err)
	}
	if be.Total != 4 || len(be.Failed) != 2 || be.Failed[0].Channel != "bad:1" || be.Failed[1].Channel != "full:1" {
		t.Fatalf("broadcast error %+v", be)
	}
	// errors.Is sees each failed channel's error.
	if !errors.Is(err, gocent.ErrUnknownChannel) || !errors.Is(err, gocent.ErrNotAvailable) {
		t.Error("errors.Is does not see the channel errors")
	}
	if errors.Is(err, gocent.ErrBadRequest) {
		t.Error("errors.Is matched a code no channel failed with")
	}
	want := "gocent: broadcast: 2 of 4 channels failed: bad:1: unknown channel (code 102); full:1: not available (code 108)"
	if err.Error() != want {
		t.Errorf("message %q\nwant    %q", err, want)
	}
	// The result holds every channel's outcome despite the error.
	if len(res.Channels) != 4 || res.Channels[0].Err != nil || res.Channels[1].Err == nil || res.Channels[2].Result.Offset != 1 {
		t.Errorf("result %+v", res.Channels)
	}
}

func TestBroadcastErrorMessageIsBounded(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	channels := make([]string, 100)
	for i := range channels {
		channels[i] = "bad:" + strings.Repeat("x", i)
	}
	_, err := c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: channels, Data: data})
	if !strings.Contains(err.Error(), "100 of 100 channels failed") || !strings.HasSuffix(err.Error(), "and 97 more") {
		t.Errorf("message %q", err)
	}
}

// A broadcast which failed as a whole still has an outcome for every channel:
// the error of the whole broadcast.
func TestBroadcastWholeFailure(t *testing.T) {
	f := newFake(t)
	for _, tc := range []struct {
		name string
		c    *gocent.Client
		want error
	}{
		{"refused", newClient(t, f), gocent.ErrNotAvailable},
		{"credentials", newClient(t, f, func(cfg *gocent.Config) { cfg.APIKey = "wrong" }), gocent.ErrUnauthorized},
	} {
		res, err := tc.c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: []string{"reject-all", "a"}, Data: data})
		var be *gocent.BroadcastError
		if errors.As(err, &be) {
			t.Fatalf("%s: a whole failure reported per channel", tc.name)
		}
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: error %v, want %v", tc.name, err, tc.want)
		}
		if len(res.Channels) != 2 || res.Channels[0].Channel != "reject-all" || res.Channels[1].Channel != "a" ||
			!errors.Is(res.Channels[0].Err, err) || !errors.Is(res.Channels[1].Err, err) {
			t.Errorf("%s: result %+v", tc.name, res.Channels)
		}
	}
}

func TestBroadcastMismatchedReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"responses":[{"result":{}}]}}`))
	}))
	defer srv.Close()
	c, err := gocent.New(gocent.Config{APIEndpoint: srv.URL + "/api", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "b"}, Data: data})
	var decErr *gocent.DecodeError
	if !errors.As(err, &decErr) {
		t.Fatalf("error %v, want a DecodeError", err)
	}
	if len(res.Channels) != 2 || !errors.Is(res.Channels[0].Err, err) || !errors.Is(res.Channels[1].Err, err) {
		t.Errorf("result %+v", res.Channels)
	}
}

func TestBatch(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	b := c.NewBatch(gocent.BatchOptions{})
	pub := b.Publish(gocent.PublishRequest{Channel: "news", Data: data})
	stats := b.PresenceStats(gocent.PresenceStatsRequest{Channel: "news"})
	info := b.Info()
	if b.Len() != 3 {
		t.Fatalf("Len %d", b.Len())
	}
	if _, err := pub.Result(); !errors.Is(err, gocent.ErrBatchNotSent) {
		t.Fatalf("Result before Send: %v", err)
	}
	if err := b.Send(t.Context()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res, err := pub.Result(); err != nil || res.Offset != 1 {
		t.Errorf("publish: %+v, %v", res, err)
	}
	if res, err := stats.Result(); err != nil || res.NumClients != 3 || res.NumUsers != 2 {
		t.Errorf("presence stats: %+v, %v", res, err)
	}
	if res, err := info.Result(); err != nil || len(res.Nodes) != 1 || res.Nodes[0].Name != "node" {
		t.Errorf("info: %+v, %v", res, err)
	}
	if err := b.Send(t.Context()); !errors.Is(err, gocent.ErrBatchSent) {
		t.Errorf("second Send: %v", err)
	}
	reqs := f.recorded()
	if len(reqs) != 1 || reqs[0].Path != "/api/batch" {
		t.Fatalf("requests %+v, want one to /api/batch", reqs)
	}
}

func TestBatchOptionsOnTheWire(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	b := c.NewBatch(gocent.BatchOptions{Parallel: true})
	b.Publish(gocent.PublishRequest{Channel: "news", Data: data})
	if err := b.Send(t.Context()); err != nil {
		t.Fatal(err)
	}
	body := string(f.recorded()[0].Body)
	if !strings.Contains(body, `"parallel":true`) {
		t.Errorf("body %s", body)
	}
}

func TestBatchPartialFailure(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	b := c.NewBatch(gocent.BatchOptions{})
	ok := b.Publish(gocent.PublishRequest{Channel: "news", Data: data})
	bad := b.Publish(gocent.PublishRequest{Channel: "bad:1", Data: data})
	bc := b.Broadcast(gocent.BroadcastRequest{Channels: []string{"a", "full:1"}, Data: data})

	err := b.Send(t.Context())
	var batchErr *gocent.BatchError
	if !errors.As(err, &batchErr) {
		t.Fatalf("error %v, want a *BatchError", err)
	}
	if batchErr.Total != 3 || len(batchErr.Failed) != 2 {
		t.Fatalf("batch error %+v", batchErr)
	}
	if f0 := batchErr.Failed[0]; f0.Index != 1 || f0.Method != "publish" || !errors.Is(f0.Err, gocent.ErrUnknownChannel) {
		t.Errorf("first failure %+v", f0)
	}
	// The broadcast failed in one channel, so it failed as a command, and
	// errors.Is sees through both levels.
	var be *gocent.BroadcastError
	if f1 := batchErr.Failed[1]; f1.Index != 2 || !errors.As(f1.Err, &be) {
		t.Errorf("second failure %+v", f1)
	}
	if !errors.Is(err, gocent.ErrNotAvailable) {
		t.Error("errors.Is does not reach the broadcast's channel error")
	}

	if _, okErr := ok.Result(); okErr != nil {
		t.Errorf("successful command: %v", okErr)
	}
	if _, badErr := bad.Result(); !errors.Is(badErr, gocent.ErrUnknownChannel) {
		t.Errorf("failed command: %v", badErr)
	}
	// The broadcast's Pending returns what a direct broadcast would.
	res, err := bc.Result()
	if !errors.As(err, &be) || len(res.Channels) != 2 || res.Channels[0].Err != nil || res.Channels[1].Err == nil {
		t.Errorf("broadcast in batch: %+v, %v", res, err)
	}
}

// A broadcast whose every channel failed still reports per channel: the
// channels can fail for different reasons, and the caller needs each.
func TestBroadcastEveryChannelFailed(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	res, err := c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: []string{"bad:1", "full:1"}, Data: data})

	var be *gocent.BroadcastError
	if !errors.As(err, &be) || be.Total != 2 || len(be.Failed) != 2 {
		t.Fatalf("error %v, want a *BroadcastError for both channels", err)
	}
	if len(res.Channels) != 2 ||
		!errors.Is(res.Channels[0].Err, gocent.ErrUnknownChannel) ||
		!errors.Is(res.Channels[1].Err, gocent.ErrNotAvailable) {
		t.Errorf("result %+v", res)
	}
}

// A batch whose every command failed still gives each command its own error.
func TestBatchEveryCommandFailed(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	b := c.NewBatch(gocent.BatchOptions{})
	pub := b.Publish(gocent.PublishRequest{Channel: "bad:1", Data: data})
	bc := b.Broadcast(gocent.BroadcastRequest{Channels: []string{"reject-all", "a"}, Data: data})

	err := b.Send(t.Context())
	var batchErr *gocent.BatchError
	if !errors.As(err, &batchErr) {
		t.Fatalf("error %v, want a *BatchError", err)
	}
	if batchErr.Total != 2 || len(batchErr.Failed) != 2 {
		t.Fatalf("batch error %+v", batchErr)
	}
	if _, pubErr := pub.Result(); !errors.Is(pubErr, gocent.ErrUnknownChannel) {
		t.Errorf("publish: %v", pubErr)
	}
	// A broadcast refused as a whole: every channel has that error.
	res, bcErr := bc.Result()
	if !errors.Is(bcErr, gocent.ErrNotAvailable) || len(res.Channels) != 2 || !errors.Is(res.Channels[1].Err, bcErr) {
		t.Errorf("broadcast: %+v, %v", res, bcErr)
	}
}

func TestBatchWholeFailure(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f, func(cfg *gocent.Config) { cfg.APIKey = "wrong" })
	b := c.NewBatch(gocent.BatchOptions{})
	pub := b.Publish(gocent.PublishRequest{Channel: "news", Data: data})
	bc := b.Broadcast(gocent.BroadcastRequest{Channels: []string{"a", "b"}, Data: data})
	if res, err := bc.Result(); !errors.Is(err, gocent.ErrBatchNotSent) || len(res.Channels) != 2 || !errors.Is(res.Channels[0].Err, err) {
		t.Errorf("broadcast before Send: %+v, %v", res, err)
	}
	err := b.Send(t.Context())
	if !errors.Is(err, gocent.ErrUnauthorized) {
		t.Fatalf("Send: %v", err)
	}
	var batchErr *gocent.BatchError
	if errors.As(err, &batchErr) {
		t.Error("a whole failure reported per command")
	}
	if _, err := pub.Result(); !errors.Is(err, gocent.ErrUnauthorized) {
		t.Errorf("Result after a failed Send: %v", err)
	}
	// A broadcast has an outcome for every channel even then.
	if res, err := bc.Result(); !errors.Is(err, gocent.ErrUnauthorized) || len(res.Channels) != 2 || !errors.Is(res.Channels[1].Err, err) {
		t.Errorf("broadcast after a failed Send: %+v, %v", res, err)
	}
}

func TestEmptyBatch(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	if err := c.NewBatch(gocent.BatchOptions{}).Send(t.Context()); err != nil {
		t.Fatalf("empty Send: %v", err)
	}
	if n := len(f.recorded()); n != 0 {
		t.Errorf("empty batch sent %d requests", n)
	}
}

func TestAddAfterSendPanics(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	b := c.NewBatch(gocent.BatchOptions{})
	b.Publish(gocent.PublishRequest{Channel: "news", Data: data})
	if err := b.Send(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Error("adding to a sent batch did not panic")
		}
	}()
	b.Publish(gocent.PublishRequest{Channel: "news", Data: data})
}
