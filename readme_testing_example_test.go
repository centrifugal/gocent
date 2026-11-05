package gocent_test

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/centrifugal/gocent/v4"
)

// The README's Testing section, as written.
func TestReadmeTestingFake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/publish":
			_, _ = fmt.Fprint(w, `{"result":{"offset":1,"epoch":"e"}}`)
		case "/api/broadcast": // one response per channel, in order
			_, _ = fmt.Fprint(w, `{"result":{"responses":[{"result":{"offset":1}},{"error":{"code":102,"message":"unknown channel"}}]}}`)
		case "/api/batch": // one reply per command, in order
			_, _ = fmt.Fprint(w, `{"replies":[{"publish":{"offset":1}},{"error":{"code":102,"message":"unknown channel"}}]}`)
		}
	}))
	defer srv.Close()
	client, err := gocent.New(gocent.Config{APIEndpoint: srv.URL + "/api"})
	if err != nil {
		t.Fatal(err)
	}
	d := jsontext.Value(`{"x":1}`)
	if res, perr := client.Publish(t.Context(), gocent.PublishRequest{Channel: "a", Data: d}); perr != nil || res.Offset != 1 {
		t.Errorf("publish %v %v", res, perr)
	}
	_, err = client.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", "b"}, Data: d})
	if _, ok := errors.AsType[*gocent.BroadcastFailedChannelsError](err); !ok {
		t.Errorf("broadcast %v", err)
	}
	b := client.NewBatch(gocent.BatchOptions{})
	b.Publish(gocent.PublishRequest{Channel: "a", Data: d})
	b.Publish(gocent.PublishRequest{Channel: "b", Data: d})
	if _, err := b.Send(t.Context()); err == nil {
		t.Error("batch: no partial failure")
	}
}
