//go:build integration

package gocent_test

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
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
// testdata/centrifugo.json configures: history and presence for channels
// without a namespace, a "map" and a "poll" namespace, client tokens and the
// unidirectional HTTP stream for a real connection, and no namespace named
// "unknown". CI configures the same through environment variables.

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
	if !errors.Is(be.Failed[0].Err, gocent.ErrUnknownChannel) {
		t.Errorf("channel error: %v", be.Failed[0].Err)
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

func TestIntegrationPublishOptions(t *testing.T) {
	c := realClient(t, gocent.AutoBatch{})
	ch := uniqueChannel(t)
	first, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: ch, Data: jsontext.Value(`{"n":1}`), IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	// The same key again: answered with the first result, not published.
	again, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: ch, Data: jsontext.Value(`{"n":1}`), IdempotencyKey: "k1"})
	if err != nil || again != first {
		t.Fatalf("repeated key: %+v, %v; first %+v", again, err, first)
	}
	if _, b64Err := c.Publish(t.Context(), gocent.PublishRequest{Channel: ch, B64Data: base64.StdEncoding.EncodeToString([]byte(`{"n":2}`))}); b64Err != nil {
		t.Fatal(b64Err)
	}
	if _, skipErr := c.Publish(t.Context(), gocent.PublishRequest{Channel: ch, Data: jsontext.Value(`{"n":3}`), SkipHistory: true}); skipErr != nil {
		t.Fatal(skipErr)
	}
	h, err := c.History(t.Context(), gocent.HistoryRequest{Channel: ch, Limit: -1})
	if err != nil || len(h.Publications) != 2 || string(h.Publications[1].Data) != `{"n":2}` || h.Offset != 2 || h.Epoch != first.Epoch {
		t.Fatalf("history: %+v, %v", h, err)
	}
	since, err := c.History(t.Context(), gocent.HistoryRequest{Channel: ch, Limit: -1, Since: gocent.StreamPosition{Offset: 1, Epoch: first.Epoch}})
	if err != nil || len(since.Publications) != 1 || since.Publications[0].Offset != 2 {
		t.Fatalf("history since offset 1: %+v, %v", since, err)
	}
	last, err := c.History(t.Context(), gocent.HistoryRequest{Channel: ch, Limit: 1, Reverse: true})
	if err != nil || len(last.Publications) != 1 || last.Publications[0].Offset != 2 {
		t.Fatalf("history reversed: %+v, %v", last, err)
	}
}

// connect opens a real client connection for user over Centrifugo's
// unidirectional HTTP streaming, with info in its token, and keeps it until
// the test ends or the server closes it. It returns a channel closed when the
// connection ends.
func connect(t *testing.T, user, info string) <-chan struct{} {
	t.Helper()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims := enc.EncodeToString([]byte(`{"sub":"` + user + `","exp":` + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + `,"info":` + info + `}`))
	mac := hmac.New(sha256.New, []byte("client_secret"))
	mac.Write([]byte(header + "." + claims))
	token := header + "." + claims + "." + enc.EncodeToString(mac.Sum(nil))

	addr := os.Getenv("GOCENT_TEST_ADDR")
	if addr == "" {
		addr = "http://localhost:8100/api"
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// The test's own Centrifugo, from GOCENT_TEST_ADDR.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(addr, "/api")+"/connection/uni_http_stream", strings.NewReader(`{"token":"`+token+`"}`)) //nolint:gosec // see above
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // the test's own Centrifugo
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	lines := bufio.NewScanner(resp.Body)
	if !lines.Scan() || !strings.Contains(lines.Text(), `"connect"`) {
		t.Fatalf("connecting: got %q", lines.Text())
	}
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for lines.Scan() {
		}
	}()
	return closed
}

func TestIntegrationConnection(t *testing.T) {
	c := realClient(t, gocent.AutoBatch{})
	ch := uniqueChannel(t)
	user := "user-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	closed := connect(t, user, `{"name":"Alice"}`)

	if _, err := c.Subscribe(t.Context(), gocent.SubscribeRequest{User: user, Channel: ch, Info: jsontext.Value(`{"role":"admin"}`)}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	p, err := c.Presence(t.Context(), gocent.PresenceRequest{Channel: ch})
	if err != nil || len(p.Presence) != 1 {
		t.Fatalf("Presence: %+v, %v", p, err)
	}
	for _, ci := range p.Presence {
		if ci.User != user || string(ci.ConnInfo) != `{"name":"Alice"}` || string(ci.ChanInfo) != `{"role":"admin"}` {
			t.Errorf("client info %+v", ci)
		}
	}
	stats, err := c.PresenceStats(t.Context(), gocent.PresenceStatsRequest{Channel: ch})
	if err != nil || stats.NumClients != 1 || stats.NumUsers != 1 {
		t.Errorf("PresenceStats: %+v, %v", stats, err)
	}
	channels, err := c.Channels(t.Context(), gocent.ChannelsRequest{Pattern: ch})
	if err != nil || channels.Channels[ch].NumClients != 1 {
		t.Errorf("Channels: %+v, %v", channels, err)
	}
	if _, err := c.Refresh(t.Context(), gocent.RefreshRequest{User: user, ExpireAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Errorf("Refresh: %v", err)
	}
	if _, err := c.Unsubscribe(t.Context(), gocent.UnsubscribeRequest{User: user, Channel: ch}); err != nil {
		t.Errorf("Unsubscribe: %v", err)
	}
	if _, err := c.Disconnect(t.Context(), gocent.DisconnectRequest{User: user}); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Error("the connection was not closed by Disconnect")
	}
}

func TestIntegrationMap(t *testing.T) {
	c := realClient(t, gocent.AutoBatch{})
	ch := "map:" + uniqueChannel(t)
	first, err := c.MapPublish(t.Context(), gocent.MapPublishRequest{Channel: ch, Key: "a", Data: jsontext.Value(`{"x":1}`)})
	if err != nil || first.Offset != 1 {
		t.Fatalf("MapPublish: %+v, %v", first, err)
	}
	if _, secondErr := c.MapPublish(t.Context(), gocent.MapPublishRequest{Channel: ch, Key: "b", Data: jsontext.Value(`{"x":2}`)}); secondErr != nil {
		t.Fatal(secondErr)
	}
	state, err := c.MapReadState(t.Context(), gocent.MapReadStateRequest{Channel: ch, Limit: 10})
	if err != nil || len(state.Entries) != 2 {
		t.Fatalf("MapReadState: %+v, %v", state, err)
	}
	for _, e := range state.Entries {
		if e.Key == "a" && string(e.Data) != `{"x":1}` {
			t.Errorf("entry a: %+v", e)
		}
	}
	stream, err := c.MapReadStream(t.Context(), gocent.MapReadStreamRequest{Channel: ch, Limit: 10})
	if err != nil || len(stream.Entries) != 2 || stream.Offset != 2 {
		t.Errorf("MapReadStream: %+v, %v", stream, err)
	}
	if _, err := c.MapRemove(t.Context(), gocent.MapRemoveRequest{Channel: ch, Key: "a"}); err != nil {
		t.Fatal(err)
	}
	if stats, err := c.MapStats(t.Context(), gocent.MapStatsRequest{Channel: ch}); err != nil || stats.NumKeys != 1 {
		t.Errorf("MapStats after remove: %+v, %v", stats, err)
	}
	if _, err := c.MapClear(t.Context(), gocent.MapClearRequest{Channel: ch}); err != nil {
		t.Fatal(err)
	}
	if stats, err := c.MapStats(t.Context(), gocent.MapStatsRequest{Channel: ch}); err != nil || stats.NumKeys != 0 {
		t.Errorf("MapStats after clear: %+v, %v", stats, err)
	}
}

func TestIntegrationSharedPollPublish(t *testing.T) {
	c := realClient(t, gocent.AutoBatch{})
	if _, err := c.SharedPollPublish(t.Context(), gocent.SharedPollPublishRequest{Channel: "poll:" + uniqueChannel(t), Key: "k", Version: 1, Data: jsontext.Value(`{"v":1}`)}); err != nil {
		t.Fatal(err)
	}
}

// A Centrifugo PRO method succeeds against PRO, and against OSS fails with a
// 404 which says the server is OSS.
func TestIntegrationProMethod(t *testing.T) {
	c := realClient(t, gocent.AutoBatch{})
	_, err := c.Connections(t.Context(), gocent.ConnectionsRequest{User: "nobody"})
	if err == nil {
		return // Centrifugo PRO
	}
	var httpErr *gocent.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound || !strings.Contains(err.Error(), "Centrifugo PRO method") {
		t.Errorf("got %v", err)
	}
}
