package gocent_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
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
		// Every channel always has an outcome, whatever the error.
		if len(res.Channels) != 2 || res.Channels[0].Channel != "a" || res.Channels[1].Channel != "b" {
			t.Fatalf("broadcast result %+v for channels a, b", res.Channels)
		}
		var be *gocent.BroadcastFailedChannelsError
		switch {
		case err == nil:
			if res.Channels[0].Err != nil || res.Channels[1].Err != nil {
				t.Fatalf("no error, yet a channel failed: %+v", res.Channels)
			}
		case errors.As(err, &be):
			if be.Total != 2 || len(be.Failed) == 0 {
				t.Fatalf("broadcast error %+v for 2 channels", be)
			}
		default:
			if !errors.Is(res.Channels[0].Err, err) || !errors.Is(res.Channels[1].Err, err) {
				t.Fatalf("broadcast failed as a whole (%v), yet a channel has another outcome: %+v", err, res.Channels)
			}
		}

		b := c.NewBatch(gocent.BatchOptions{})
		pub := b.Publish(gocent.PublishRequest{Channel: "a", Data: data})
		bc := b.Broadcast(gocent.BroadcastRequest{Channels: []string{"a"}, Data: data})
		_, _ = b.Send(ctx)
		_, _ = pub.Result()
		_, _ = bc.Result()
	})
}

// scriptTransport answers broadcasts as the script says, a byte per
// decision, and records what each request sent. Once the script runs out,
// everything succeeds.
type scriptTransport struct {
	script   []byte
	requests []scriptRequest
}

type scriptRequest struct {
	key      string
	channels []string
	// outcome per channel: 'o' published, 't' temporary failure, 'p'
	// permanent failure; all the same for a request which failed as a whole.
	outcome []byte
	// whole is true when no channel got a reply.
	whole bool
}

func (s *scriptTransport) next() byte {
	if len(s.script) == 0 {
		return 2 // published
	}
	b := s.script[0]
	s.script = s.script[1:]
	return b
}

func (s *scriptTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var req struct {
		Channels []string `json:"channels"`
		Key      string   `json:"idempotency_key"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	sent := scriptRequest{key: req.Key, channels: req.Channels, outcome: make([]byte, len(req.Channels))}
	reply := func(status int, b string) (*http.Response, error) {
		s.requests = append(s.requests, sent)
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(b)),
			Header:     http.Header{"Content-Type": {"application/json"}},
		}, nil
	}
	whole := func(o byte) {
		sent.whole = true
		for i := range sent.outcome {
			sent.outcome[i] = o
		}
	}
	switch s.next() % 8 {
	case 0:
		whole('t')
		return reply(http.StatusServiceUnavailable, "")
	case 1:
		whole('p')
		return reply(http.StatusOK, `{"error":{"code":107,"message":"bad request"}}`)
	}
	responses := make([]string, len(req.Channels))
	for i := range req.Channels {
		switch s.next() % 3 {
		case 0:
			sent.outcome[i] = 't'
			responses[i] = `{"error":{"code":100,"message":"internal","temporary":true}}`
		case 1:
			sent.outcome[i] = 'p'
			responses[i] = `{"error":{"code":102,"message":"unknown channel"}}`
		default:
			sent.outcome[i] = 'o'
			responses[i] = `{"result":{"offset":7,"epoch":"e"}}`
		}
	}
	return reply(http.StatusOK, `{"result":{"responses":[`+strings.Join(responses, ",")+`]}}`)
}

// FuzzBroadcastWithRetry checks BroadcastWithRetry against a model of what it
// must do, for any sequence of outcomes the server gives: which channels each
// attempt sends, when it stops, and what it returns.
func FuzzBroadcastWithRetry(f *testing.F) {
	f.Add(uint8(3), uint8(3), []byte{2, 0, 1, 2, 0})
	f.Add(uint8(2), uint8(4), []byte{0, 0, 0})
	f.Add(uint8(4), uint8(2), []byte{1})
	f.Add(uint8(1), uint8(1), []byte{2, 0})
	f.Add(uint8(5), uint8(3), []byte{2, 0, 0, 1, 2, 0, 0, 2, 0})
	f.Fuzz(func(t *testing.T, numChannels, maxAttempts uint8, script []byte) {
		n, attempts := 1+int(numChannels%6), 1+int(maxAttempts%4)
		tr := &scriptTransport{script: script}
		c, err := gocent.New(gocent.Config{
			APIEndpoint: "http://centrifugo.invalid/api",
			APIKey:      "k",
			HTTPClient:  &http.Client{Transport: tr},
		})
		if err != nil {
			t.Fatal(err)
		}
		channels := make([]string, n)
		for i := range channels {
			channels[i] = fmt.Sprintf("c%d", i)
		}
		res, err := c.BroadcastWithRetry(t.Context(), gocent.BroadcastRequest{Channels: channels, Data: data},
			gocent.BroadcastRetryOptions{
				MaxAttempts:  attempts,
				OnAttemptErr: func(context.Context, gocent.BroadcastAttempt) error { return nil },
			})

		// The model: replay the requests, keeping each channel's last
		// outcome and how often it was sent.
		last := map[string]byte{}
		sentTimes := map[string]int{}
		replied := false
		for i, r := range tr.requests {
			if r.key == "" || r.key != res.IdempotencyKey {
				t.Fatalf("request %d has key %q, result %q", i, r.key, res.IdempotencyKey)
			}
			replied = replied || !r.whole
			for j, ch := range r.channels {
				// Only a channel which may still succeed is sent again.
				if o, ok := last[ch]; ok && o != 't' {
					t.Fatalf("request %d sent %s after outcome %c", i, ch, o)
				}
				last[ch] = r.outcome[j]
				sentTimes[ch]++
			}
			// A retry sends exactly the channels of the attempt before which
			// failed temporarily, in their order.
			if i > 0 {
				var want []string
				prev := tr.requests[i-1]
				for j, ch := range prev.channels {
					if prev.outcome[j] == 't' {
						want = append(want, ch)
					}
				}
				if !slices.Equal(r.channels, want) {
					t.Fatalf("request %d sent %v, want %v", i, r.channels, want)
				}
			}
		}
		if len(tr.requests) == 0 || len(tr.requests) > attempts || res.Attempts != len(tr.requests) {
			t.Fatalf("%d requests, %d attempts reported, at most %d allowed", len(tr.requests), res.Attempts, attempts)
		}
		// It stops early only when nothing may still succeed - or a request
		// failed as a whole for good.
		lastReq := tr.requests[len(tr.requests)-1]
		mayRetry := slices.Contains(lastReq.outcome, 't')
		if len(tr.requests) < attempts && mayRetry {
			t.Fatalf("stopped after %d of %d attempts with %v", len(tr.requests), attempts, lastReq)
		}
		if res.Replied != replied {
			t.Fatalf("Replied %v, model %v", res.Replied, replied)
		}
		if len(res.Channels) != n {
			t.Fatalf("%d channel results for %d channels", len(res.Channels), n)
		}
		failed := 0
		for i, ch := range res.Channels {
			if ch.Channel != channels[i] || ch.Attempts != sentTimes[ch.Channel] {
				t.Fatalf("channel %d: %+v, sent %d times", i, ch, sentTimes[channels[i]])
			}
			if (last[ch.Channel] == 'o') != (ch.Err == nil) {
				t.Fatalf("channel %s: last outcome %c, error %v", ch.Channel, last[ch.Channel], ch.Err)
			}
			if ch.Err != nil {
				failed++
			}
		}
		if len(res.Failed()) != failed {
			t.Fatalf("Failed() has %d, %d channels failed", len(res.Failed()), failed)
		}
		be, isFailedChannels := errors.AsType[*gocent.BroadcastFailedChannelsError](err)
		switch {
		case failed == 0:
			if err != nil {
				t.Fatalf("every channel published, error %v", err)
			}
		case !replied:
			if err == nil || isFailedChannels {
				t.Fatalf("no reply, error %v", err)
			}
		default:
			if !isFailedChannels || len(be.Failed) != failed {
				t.Fatalf("%d channels failed after a reply, error %v", failed, err)
			}
		}
	})
}
