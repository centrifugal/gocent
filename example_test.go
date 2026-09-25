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

// A broadcast fails or succeeds per channel. Checking err is enough to notice a
// failure in any channel; the result holds every channel's outcome.
func ExampleClient_Broadcast() {
	var client *gocent.Client // created once with gocent.New

	res, err := client.Broadcast(context.Background(), gocent.BroadcastRequest{
		Channels: []string{"user:1", "user:2", "user:3"},
		Data:     jsontext.Value(`{"text":"hello"}`),
	})
	var partial *gocent.BroadcastError
	switch {
	case errors.As(err, &partial):
		for _, failed := range partial.Failed {
			log.Printf("not published into %s: %v", failed.Channel, failed.Err)
		}
	case err != nil:
		log.Fatal(err) // nothing was published
	}
	for _, ch := range res.Channels {
		if ch.Err == nil {
			log.Printf("%s: offset %d", ch.Channel, ch.Result.Offset)
		}
	}
}

// A batch sends many commands in one request. Each command's outcome is read
// from the Pending its method returned.
func ExampleBatch() {
	var client *gocent.Client // created once with gocent.New

	b := client.NewBatch()
	pub := b.Publish(gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{}`)})
	stats := b.PresenceStats(gocent.PresenceStatsRequest{Channel: "news"})

	err := b.Send(context.Background(), gocent.BatchOptions{})
	var partial *gocent.BatchError
	if err != nil && !errors.As(err, &partial) {
		log.Fatal(err) // no command got a reply
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
