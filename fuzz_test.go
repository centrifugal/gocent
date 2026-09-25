package gocent_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/centrifugal/gocent/v4"
)

// replyTransport answers every request with the same body.
type replyTransport []byte

func (b replyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(b)),
		Header:     http.Header{"Content-Type": {"application/json"}},
	}, nil
}

// FuzzReplies feeds arbitrary replies to the methods whose replies have the
// most structure. Whatever the server sends, a call must not panic, and must
// report either a result or an error it can be told apart by.
func FuzzReplies(f *testing.F) {
	for _, seed := range []string{
		`{"result":{"offset":1,"epoch":"e"}}`,
		`{"error":{"code":102,"message":"unknown channel"}}`,
		`{"result":{"responses":[{"result":{"offset":1}},{"error":{"code":102}}]}}`,
		`{"result":{"responses":[]}}`,
		`{"replies":[{"publish":{"offset":1}},{"error":{"code":108}},{"broadcast":{"responses":[{"result":{}}]}}]}`,
		`{"replies":null}`,
		`{"result":null}`,
		`[]`, `null`, ``, `{`, `<html>`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		c, err := gocent.New(gocent.Config{
			APIEndpoint: "http://centrifugo.invalid/api",
			APIKey:      "k",
			HTTPClient:  &http.Client{Transport: replyTransport(body)},
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx := t.Context()

		_, _ = c.Publish(ctx, gocent.PublishRequest{Channel: "a", Data: data})

		res, err := c.Broadcast(ctx, gocent.BroadcastRequest{Channels: []string{"a", "b"}, Data: data})
		var partial *gocent.BroadcastError
		switch {
		case err == nil, errors.As(err, &partial):
			// A result is only ever returned whole: one entry per channel.
			if len(res.Channels) != 2 {
				t.Fatalf("broadcast result with %d channels for 2", len(res.Channels))
			}
		case len(res.Channels) != 0:
			t.Fatalf("broadcast failed as a whole (%v) yet returned results", err)
		}

		b := c.NewBatch()
		pub := b.Publish(gocent.PublishRequest{Channel: "a", Data: data})
		bc := b.Broadcast(gocent.BroadcastRequest{Channels: []string{"a"}, Data: data})
		_ = b.Send(ctx, gocent.BatchOptions{})
		_, _ = pub.Result()
		_, _ = bc.Result()
	})
}
