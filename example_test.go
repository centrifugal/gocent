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
// which accepts some failed channels stops only when Centrifugo did not
// reply, then reads each one.
func ExampleBroadcastFailedChannelsError() {
	var client *gocent.Client // created once with gocent.New

	res, err := client.Broadcast(context.Background(), gocent.BroadcastRequest{
		Channels: []string{"user:1", "user:2", "user:3"},
		Data:     jsontext.Value(`{"text":"hello"}`),
	})
	if !res.Replied {
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

	if _, err := b.Send(context.Background()); err != nil {
		log.Print(err) // gocent: batch: 1 of 2 commands failed, 1 succeeded: ...
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

// Retrying a call: Retryable says whether it may succeed, and the same
// IdempotencyKey keeps a publication which happened in an earlier attempt
// from happening again. For a broadcast, use [Client.BroadcastWithRetry].
func ExampleRetryable() {
	client, err := gocent.New(gocent.Config{
		APIEndpoint: "http://localhost:8000/api",
		APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}
	req := gocent.PublishRequest{
		Channel: "orders:42",
		Data:    jsontext.Value(`{"status":"paid"}`),
		// The same key on every attempt: a publication which happened in an
		// earlier one - even one reported as failed - is not published again.
		IdempotencyKey: "order-42-paid",
	}
	backoff := gocent.ExponentialBackoff(100*time.Millisecond, 2*time.Second)
	publish := func(ctx context.Context) error {
		for attempt := 1; ; attempt++ {
			_, publishErr := client.Publish(ctx, req)
			if publishErr == nil || !gocent.Retryable(publishErr) || attempt == 3 {
				return publishErr
			}
			// Back off, unless the context ends first.
			if waitErr := gocent.Wait(ctx, backoff(attempt)); waitErr != nil {
				return waitErr
			}
		}
	}
	// All the attempts together, backing off included, take at most this.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = publish(ctx)
	cancel()
	if err != nil {
		log.Fatal(err)
	}
}

// A broadcast retried by the client: only the channels which failed for a
// temporary reason are sent again, all with one IdempotencyKey, and the result
// holds every channel's last outcome.
func ExampleClient_BroadcastWithRetry() {
	client, err := gocent.New(gocent.Config{
		APIEndpoint: "http://localhost:8000/api",
		APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	backoff := gocent.ExponentialBackoff(100*time.Millisecond, 2*time.Second)
	res, err := client.BroadcastWithRetry(ctx, gocent.BroadcastRequest{
		Channels: []string{"news", "sport", "weather"},
		Data:     jsontext.Value(`{"text":"hello"}`),
		// Optional: without one, a key is made for this call.
		IdempotencyKey: "message-42",
	}, gocent.BroadcastRetryOptions{
		MaxAttempts: 3,
		// Logs why each retry happens, then waits. Log a count and a sample,
		// not every channel: a broadcast to many channels may fail in all of
		// them. a.Retry is never empty here.
		OnAttemptErr: func(ctx context.Context, a gocent.BroadcastAttempt) error {
			log.Printf("attempt %d: %d channels to retry, first: %v",
				a.Attempt, len(a.Retry), a.Retry[0].Err)
			return gocent.Wait(ctx, backoff(a.Attempt))
		},
	})
	log.Printf("broadcast: %d channels, %d failed, %d attempts, key %s",
		len(res.Channels), len(res.Failed()), res.Attempts, res.IdempotencyKey)
	if !res.Replied {
		log.Print(err) // no attempt got a reply
	}
}

// A batch is retried by building it again: a Batch is sent once. Keep the
// requests, give every publication an IdempotencyKey, and send again only the
// commands which failed for a temporary reason - all of them when the batch
// failed as a whole, since res.Failed() then lists every command.
func ExampleRetryable_batch() {
	client, err := gocent.New(gocent.Config{
		APIEndpoint: "http://localhost:8000/api",
		APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}
	// One key for the event: keys are kept per channel, so both
	// publications can share it, and every attempt sends the same one.
	reqs := []gocent.PublishRequest{
		{Channel: "orders:42", Data: jsontext.Value(`{"status":"paid"}`), IdempotencyKey: "order-42-paid"},
		{Channel: "user:7", Data: jsontext.Value(`{"text":"payment received"}`), IdempotencyKey: "order-42-paid"},
	}
	backoff := gocent.ExponentialBackoff(100*time.Millisecond, 2*time.Second)
	send := func(ctx context.Context) error {
		for attempt := 1; ; attempt++ {
			b := client.NewBatch(gocent.BatchOptions{})
			for _, req := range reqs {
				b.Publish(req)
			}
			res, sendErr := b.Send(ctx)
			if sendErr == nil || !gocent.Retryable(sendErr) || attempt == 3 {
				return sendErr
			}
			// Next, the commands which may still succeed. Index is the
			// command's position in this attempt's batch, that is in reqs.
			var next []gocent.PublishRequest
			for _, f := range res.Failed() {
				if gocent.Retryable(f.Err) {
					next = append(next, reqs[f.Index])
				} else {
					log.Printf("not published into %s: %v", reqs[f.Index].Channel, f.Err)
				}
			}
			reqs = next
			if waitErr := gocent.Wait(ctx, backoff(attempt)); waitErr != nil {
				return waitErr
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = send(ctx)
	cancel()
	if err != nil {
		log.Fatal(err)
	}
}
