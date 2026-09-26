package gocent_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log"
	"os"

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
