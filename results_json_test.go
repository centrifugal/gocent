package gocent_test

import (
	encjson "encoding/json"
	"encoding/json/v2"
	"errors"
	"testing"

	"github.com/centrifugal/gocent/v4"
)

// A broadcast's error and result agree on every channel, attempts included.
func TestBroadcastErrorAgreesWithResult(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	res, err := c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "bad:1"}, Data: data})
	be, ok := errors.AsType[*gocent.BroadcastFailedChannelsError](err)
	if !ok {
		t.Fatalf("error %v", err)
	}
	if be.Failed[0].Attempts != 1 || res.Channels[1].Attempts != 1 {
		t.Errorf("attempts: error %d, result %d", be.Failed[0].Attempts, res.Channels[1].Attempts)
	}
}

// Results render as JSON by their wire names, with encoding/json and
// encoding/json/v2 alike, errors as {"code", "message"}.
func TestResultsRenderAsJSON(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	res, _ := c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "bad:1"}, Data: data, IdempotencyKey: "k"})
	want := `{"channels":[{"channel":"a","result":{"offset":1,"epoch":"e-a"},"attempts":1},` +
		`{"channel":"bad:1","error":{"code":102,"message":"unknown channel"},"attempts":1}],` +
		`"idempotency_key":"k","attempts":1,"replied":true}`
	for name, marshal := range map[string]func(any) ([]byte, error){
		"v2": func(v any) ([]byte, error) { return json.Marshal(v) },
		"v1": encjson.Marshal,
	} {
		got, err := marshal(res)
		if err != nil || string(got) != want {
			t.Errorf("%s: %s, %v\nwant %s", name, got, err, want)
		}
	}

	// A broadcast failed as a whole: its error has no code.
	f.unavailable.Store(1)
	whole, _ := c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: []string{"a"}, Data: data})
	got, err := json.Marshal(whole.Channels[0])
	if err != nil || !jsonHasKeys(t, got, "channel", "error", "attempts") {
		t.Errorf("whole failure: %s, %v", got, err)
	}

	b := c.NewBatch(gocent.BatchOptions{})
	b.Publish(gocent.PublishRequest{Channel: "a", Data: data})
	b.Publish(gocent.PublishRequest{Channel: "bad:2", Data: data})
	br, _ := b.Send(t.Context())
	got, err = json.Marshal(br)
	wantBatch := `{"replied":true,"failed":[{"index":1,"method":"publish","error":{"code":102,"message":"unknown channel"}}]}`
	if err != nil || string(got) != wantBatch {
		t.Errorf("batch: %s, %v\nwant %s", got, err, wantBatch)
	}
}

func jsonHasKeys(t *testing.T, b []byte, keys ...string) bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}

// Test doubles of code which sends batches can make the results it reads.
func TestBatchResultsForTestDoubles(t *testing.T) {
	failed := []gocent.CommandError{{Index: 1, Method: "publish", Err: gocent.ErrUnknownChannel}}
	res := gocent.NewBatchResult(true, failed)
	if !res.Replied || len(res.Failed()) != 1 {
		t.Errorf("result %+v", res)
	}
	p := gocent.NewPending(gocent.PublishResult{Offset: 7}, nil)
	if r, err := p.Result(); err != nil || r.Offset != 7 {
		t.Errorf("pending %v, %v", r, err)
	}
	if _, err := gocent.NewPending(gocent.PublishResult{}, gocent.ErrUnknownChannel).Result(); !errors.Is(err, gocent.ErrUnknownChannel) {
		t.Errorf("pending error %v", err)
	}
}
