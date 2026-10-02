package gocent_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/centrifugal/gocent/v4"
)

func Example() {
	client, err := gocent.New(gocent.Config{
		APIEndpoint: "http://localhost:8000/api",
		APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}

	res, err := client.Publish(context.Background(), gocent.PublishRequest{
		Channel: "news",
		Data:    jsontext.Value(`{"text":"hello"}`),
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("published at offset %d", res.Offset)
}

// Data is JSON. Encode a value of your own with encoding/json.
func ExampleClient_Publish() {
	var client *gocent.Client // created once with gocent.New

	type message struct {
		Text string `json:"text"`
	}
	data, err := json.Marshal(message{Text: "hello"})
	if err != nil {
		log.Fatal(err)
	}
	_, err = client.Publish(context.Background(), gocent.PublishRequest{
		Channel:        "chat:index",
		Data:           data,
		IdempotencyKey: "message-42", // retried publications are not duplicated
	})
	switch {
	case errors.Is(err, gocent.ErrUnknownChannel):
		log.Print("no such channel namespace")
	case err != nil:
		log.Fatal(err)
	}
}

// A broadcast returns an error unless the data was published into every
// channel. Most callers need nothing more: retried with the same
// IdempotencyKey, the broadcast is not published twice into any channel.
func ExampleClient_Broadcast() {
	var client *gocent.Client // created once with gocent.New

	_, err := client.Broadcast(context.Background(), gocent.BroadcastRequest{
		Channels:       []string{"user:1", "user:2", "user:3"},
		Data:           jsontext.Value(`{"text":"hello"}`),
		IdempotencyKey: "message-42",
	})
	if err != nil {
		log.Fatal(err) // retry the same request later
	}
}

// The result holds every channel's outcome whatever the error, so a caller
// which accepts some failed channels checks err first, then reads each one.
func ExampleBroadcastError() {
	var client *gocent.Client // created once with gocent.New

	res, err := client.Broadcast(context.Background(), gocent.BroadcastRequest{
		Channels: []string{"user:1", "user:2", "user:3"},
		Data:     jsontext.Value(`{"text":"hello"}`),
	})
	var be *gocent.BroadcastError
	if err != nil && !errors.As(err, &be) {
		log.Fatal(err) // no channel got a reply
	}
	for _, ch := range res.Channels {
		if ch.Err != nil {
			log.Printf("not published into %s: %v", ch.Channel, ch.Err)
			continue
		}
		log.Printf("%s: offset %d", ch.Channel, ch.Result.Offset)
	}
}

// A batch sends many commands in one request, and Send returns an error
// unless every command succeeded. Each command's own outcome is read from the
// Pending its method returned.
func ExampleBatch() {
	var client *gocent.Client // created once with gocent.New

	b := client.NewBatch(gocent.BatchOptions{})
	pub := b.Publish(gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{}`)})
	stats := b.PresenceStats(gocent.PresenceStatsRequest{Channel: "news"})

	if err := b.Send(context.Background()); err != nil {
		log.Print(err) // gocent: batch: 1 of 2 commands failed: ...
	}
	if res, err := pub.Result(); err == nil {
		log.Printf("published at offset %d", res.Offset)
	}
	if res, err := stats.Result(); err == nil {
		log.Printf("%d clients online", res.NumClients)
	}
}

// With AutoBatch, calls made concurrently share requests once MaxInFlight of
// them are in flight. The calls themselves do not change.
func ExampleAutoBatch() {
	client, err := gocent.New(gocent.Config{
		APIEndpoint: "http://localhost:8000/api",
		APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
		AutoBatch: gocent.AutoBatch{
			MaxInFlight: 8,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	// Called from many goroutines, as a server handling requests does.
	_, err = client.Publish(context.Background(), gocent.PublishRequest{
		Channel: "news",
		Data:    jsontext.Value(`{"text":"hello"}`),
	})
	if err != nil {
		log.Print(err)
	}
}

// A command for Centrifugo's async consumers - an outbox row, a Kafka message -
// is an API method's name and its request as JSON. A request gives both.
func ExamplePublishRequest_APIMethod() {
	req := gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{"text":"hello"}`)}
	payload, err := json.Marshal(req)
	if err != nil {
		log.Fatal(err)
	}
	// An outbox row, or the "method" and "payload" of a Kafka message.
	fmt.Println(req.APIMethod(), string(payload))
	// Output: publish {"channel":"news","data":{"text":"hello"}}
}

// A broadcast retried until every channel got the publication, dropping the
// channels which can never succeed. The IdempotencyKey keeps a channel from
// getting the publication twice when a retry repeats it.
func ExampleRetryable() {
	client, err := gocent.New(gocent.Config{
		APIEndpoint: "http://localhost:8000/api",
		APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}
	req := gocent.BroadcastRequest{
		Channels:       []string{"news", "sport", "weather"},
		Data:           jsontext.Value(`{"text":"hello"}`),
		IdempotencyKey: "message-42",
	}
	for attempt := 1; ; attempt++ {
		_, err := client.Broadcast(context.Background(), req)
		if err == nil {
			return
		}
		if !gocent.Retryable(err) || attempt == 3 {
			log.Fatal(err)
		}
		var be *gocent.BroadcastError
		if errors.As(err, &be) {
			// Retry only the channels which failed for a temporary reason.
			req.Channels = nil
			for _, f := range be.Failed {
				if gocent.Retryable(f.Err) {
					req.Channels = append(req.Channels, f.Channel)
				} else {
					log.Printf("not published into %s: %v", f.Channel, f.Err)
				}
			}
		}
		time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
	}
}

// A batch is retried by building it again: a Batch is sent once. Keep the
// requests, give every publication an IdempotencyKey, and leave out the
// commands which failed for good. The publications which happened are not
// repeated.
func ExampleRetryable_batch() {
	client, err := gocent.New(gocent.Config{
		APIEndpoint: "http://localhost:8000/api",
		APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}
	reqs := []gocent.PublishRequest{
		{Channel: "orders:42", Data: jsontext.Value(`{"status":"paid"}`), IdempotencyKey: "order-42-paid"},
		{Channel: "user:7", Data: jsontext.Value(`{"text":"payment received"}`), IdempotencyKey: "order-42-paid"},
	}
	for attempt := 1; ; attempt++ {
		b := client.NewBatch(gocent.BatchOptions{})
		for _, req := range reqs {
			b.Publish(req)
		}
		err := b.Send(context.Background())
		if err == nil {
			return
		}
		if !gocent.Retryable(err) || attempt == 3 {
			log.Fatal(err)
		}
		var batchErr *gocent.BatchError
		if errors.As(err, &batchErr) {
			// Resending every command is safe with keys, but a command which
			// failed for good would fail on every attempt: leave it out.
			keep := reqs[:0:0]
			failed := map[int]error{}
			for _, f := range batchErr.Failed {
				failed[f.Index] = f.Err
			}
			for i, req := range reqs {
				if err, ok := failed[i]; ok && !gocent.Retryable(err) {
					log.Printf("not published into %s: %v", req.Channel, err)
					continue
				}
				keep = append(keep, req)
			}
			reqs = keep
		}
		time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
	}
}
