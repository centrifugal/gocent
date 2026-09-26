//go:build integration

package gocent_test

import (
	"encoding/json/jsontext"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
)

// These tests run against a real Centrifugo:
//
//	go test -tags integration ./...
//
// The server is found through GOCENT_TEST_ADDR and GOCENT_TEST_API_KEY, which
// default to http://localhost:8100/api and "api_key", which is what
// testdata/centrifugo.json configures. It needs history and
// presence enabled for channels without a namespace, and no namespace named
// "unknown" - see testdata/centrifugo.json.

func realClient(t *testing.T, ab gocent.AutoBatch) *gocent.Client {
	t.Helper()
	addr := os.Getenv("GOCENT_TEST_ADDR")
	if addr == "" {
		addr = "http://localhost:8100/api"
	}
	key := os.Getenv("GOCENT_TEST_API_KEY")
	if key == "" {
		key = "api_key"
	}
	c, err := gocent.New(gocent.Config{APIEndpoint: addr, APIKey: key, AutoBatch: ab})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Info(t.Context()); err != nil {
		t.Fatalf("Centrifugo not reachable at %s: %v", addr, err)
	}
	return c
}

func uniqueChannel(t *testing.T) string {
	return "gocent-" + t.Name() + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func TestIntegrationPublishAndHistory(t *testing.T) {
	c := realClient(t, gocent.AutoBatch{})
	ch := uniqueChannel(t)
	for i := range 3 {
		res, err := c.Publish(t.Context(), gocent.PublishRequest{
			Channel: ch,
			Data:    jsontext.Value(`{"i":` + strconv.Itoa(i) + `}`),
			Tags:    map[string]string{"n": strconv.Itoa(i)},
		})
		if err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
		if res.Offset != uint64(i+1) || res.Epoch == "" {
			t.Fatalf("Publish %d: %+v", i, res)
		}
	}
	h, err := c.History(t.Context(), gocent.HistoryRequest{Channel: ch, Limit: -1})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(h.Publications) != 3 || string(h.Publications[2].Data) != `{"i":2}` || h.Publications[2].Tags["n"] != "2" {
		t.Fatalf("History: %+v", h)
	}
	if _, rmErr := c.HistoryRemove(t.Context(), gocent.HistoryRemoveRequest{Channel: ch}); rmErr != nil {
		t.Fatalf("HistoryRemove: %v", rmErr)
	}

	_, err = c.Publish(t.Context(), gocent.PublishRequest{Channel: "unknown:x", Data: jsontext.Value(`{}`)})
	if !errors.Is(err, gocent.ErrUnknownChannel) {
		t.Fatalf("publish into an unknown namespace: %v", err)
	}
}

func TestIntegrationBroadcast(t *testing.T) {
	c := realClient(t, gocent.AutoBatch{})
	a, b := uniqueChannel(t)+"-a", uniqueChannel(t)+"-b"
	res, err := c.Broadcast(t.Context(), gocent.BroadcastRequest{
		Channels: []string{a, "unknown:x", b},
		Data:     jsontext.Value(`{"text":"hello"}`),
	})
	var be *gocent.BroadcastError
	if !errors.As(err, &be) {
		t.Fatalf("Broadcast: %v", err)
	}
	if len(be.Failed) != 1 || be.Failed[0].Channel != "unknown:x" {
		t.Fatalf("failed channels %+v", be.Failed)
	}
	if !errors.Is(err, gocent.ErrUnknownChannel) {
		t.Errorf("errors.Is does not see the channel error: %v", err)
	}
	if res.Channels[0].Channel != a || res.Channels[0].Err != nil || res.Channels[0].Result.Offset != 1 {
		t.Errorf("channel a: %+v", res.Channels[0])
	}
	if res.Channels[2].Result.Offset != 1 {
		t.Errorf("channel b: %+v", res.Channels[2])
	}
}

func TestIntegrationBatch(t *testing.T) {
	c := realClient(t, gocent.AutoBatch{})
	ch := uniqueChannel(t)
	b := c.NewBatch(gocent.BatchOptions{})
	pub := b.Publish(gocent.PublishRequest{Channel: ch, Data: jsontext.Value(`{}`)})
	bad := b.Publish(gocent.PublishRequest{Channel: "unknown:x", Data: jsontext.Value(`{}`)})
	bc := b.Broadcast(gocent.BroadcastRequest{Channels: []string{ch}, Data: jsontext.Value(`{}`)})
	stats := b.PresenceStats(gocent.PresenceStatsRequest{Channel: ch})
	hist := b.History(gocent.HistoryRequest{Channel: ch, Limit: -1})

	err := b.Send(t.Context())
	var batchErr *gocent.BatchError
	if !errors.As(err, &batchErr) || len(batchErr.Failed) != 1 || batchErr.Failed[0].Index != 1 {
		t.Fatalf("Send: %v", err)
	}
	if res, err := pub.Result(); err != nil || res.Offset != 1 {
		t.Errorf("publish: %+v, %v", res, err)
	}
	if _, err := bad.Result(); !errors.Is(err, gocent.ErrUnknownChannel) {
		t.Errorf("bad publish: %v", err)
	}
	if res, err := bc.Result(); err != nil || res.Channels[0].Result.Offset != 2 {
		t.Errorf("broadcast: %+v, %v", res, err)
	}
	if _, err := stats.Result(); err != nil {
		t.Errorf("presence stats: %v", err)
	}
	// The batch ran in order: history sees both publications.
	if res, err := hist.Result(); err != nil || len(res.Publications) != 2 {
		t.Errorf("history: %+v, %v", res, err)
	}
}

func TestIntegrationServerSideSubscriptions(t *testing.T) {
	c := realClient(t, gocent.AutoBatch{})
	ch := uniqueChannel(t)
	// Nobody is connected: these succeed with nothing to act on.
	if _, err := c.Subscribe(t.Context(), gocent.SubscribeRequest{Channel: ch, User: "42"}); err != nil {
		t.Errorf("Subscribe: %v", err)
	}
	if _, err := c.Unsubscribe(t.Context(), gocent.UnsubscribeRequest{Channel: ch, User: "42"}); err != nil {
		t.Errorf("Unsubscribe: %v", err)
	}
	if _, err := c.Disconnect(t.Context(), gocent.DisconnectRequest{User: "42"}); err != nil {
		t.Errorf("Disconnect: %v", err)
	}
	if _, err := c.Presence(t.Context(), gocent.PresenceRequest{Channel: ch}); err != nil {
		t.Errorf("Presence: %v", err)
	}
	if _, err := c.Channels(t.Context(), gocent.ChannelsRequest{}); err != nil {
		t.Errorf("Channels: %v", err)
	}
	info, err := c.Info(t.Context())
	if err != nil || len(info.Nodes) == 0 || info.Nodes[0].Version == "" {
		t.Errorf("Info: %+v, %v", info, err)
	}
}

func TestIntegrationAutoBatch(t *testing.T) {
	c := realClient(t, gocent.AutoBatch{MaxInFlight: 4})
	ch := uniqueChannel(t)
	const calls = 500
	var wg sync.WaitGroup
	offsets := make([]uint64, calls)
	for i := range calls {
		wg.Go(func() {
			res, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: ch, Data: jsontext.Value(`{}`)})
			if err != nil {
				t.Error(err)
				return
			}
			offsets[i] = res.Offset
		})
	}
	wg.Wait()
	// Every publication got its own, distinct offset.
	seen := make(map[uint64]bool, calls)
	for _, o := range offsets {
		if o == 0 || seen[o] {
			t.Fatalf("offset %d missing or repeated", o)
		}
		seen[o] = true
	}
}
