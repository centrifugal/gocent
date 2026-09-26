package gocent_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/centrifugal/gocent/v4"
)

// fixedReply answers every request with body, so the benchmarks measure the
// client - encoding, the HTTP round trip over loopback, decoding - rather than
// a server.
func fixedReply(b *testing.B, body string) *gocent.Client {
	b.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, body)
	}))
	b.Cleanup(srv.Close)
	c, err := gocent.New(gocent.Config{APIEndpoint: srv.URL + "/api", APIKey: "k"})
	if err != nil {
		b.Fatal(err)
	}
	return c
}

func BenchmarkPublish(b *testing.B) {
	c := fixedReply(b, `{"result":{"offset":1,"epoch":"abcd"}}`)
	req := gocent.PublishRequest{Channel: "news", Data: data}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Publish(b.Context(), req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPublishParallel(b *testing.B) {
	c := fixedReply(b, `{"result":{"offset":1,"epoch":"abcd"}}`)
	req := gocent.PublishRequest{Channel: "news", Data: data}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := c.Publish(b.Context(), req); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkBroadcast1000(b *testing.B) {
	channels := make([]string, 1000)
	responses := make([]string, 1000)
	for i := range channels {
		channels[i] = "user:" + strconv.Itoa(i)
		responses[i] = `{"result":{"offset":` + strconv.Itoa(i) + `,"epoch":"abcd"}}`
	}
	c := fixedReply(b, `{"result":{"responses":[`+strings.Join(responses, ",")+`]}}`)
	req := gocent.BroadcastRequest{Channels: channels, Data: data}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Broadcast(b.Context(), req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBatch100(b *testing.B) {
	replies := make([]string, 100)
	for i := range replies {
		replies[i] = `{"publish":{"offset":1,"epoch":"abcd"}}`
	}
	c := fixedReply(b, `{"replies":[`+strings.Join(replies, ",")+`]}`)
	b.ReportAllocs()
	for b.Loop() {
		batch := c.NewBatch(gocent.BatchOptions{})
		for i := range 100 {
			batch.Publish(gocent.PublishRequest{Channel: "ch" + strconv.Itoa(i), Data: data})
		}
		if err := batch.Send(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}
