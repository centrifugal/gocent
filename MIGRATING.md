# Migrating from v3

Three complete programs, as written with v3 and with v4: publishing,
broadcasting, and sending several commands at once. Each pair does the same
thing. The [changelog](changelog.md#migrating-from-v3) maps every v3 call and
option to v4.

Change the import path to `github.com/centrifugal/gocent/v4` first. v4 needs
Go 1.27.

## Publish

v3:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"time"

	"github.com/centrifugal/gocent/v3"
)

type Event struct {
	Text string `json:"text"`
}

func main() {
	c := gocent.New(gocent.Config{
		Addr: "http://localhost:8000/api",
		Key:  os.Getenv("CENTRIFUGO_API_KEY"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	data, err := json.Marshal(Event{Text: "hello"})
	if err != nil {
		log.Fatal(err)
	}
	res, err := c.Publish(ctx, "news", data,
		gocent.WithTags(map[string]string{"kind": "greeting"}),
		gocent.WithIdempotencyKey("event-1"),
	)
	if err != nil {
		var apiErr *gocent.Error
		if errors.As(err, &apiErr) && apiErr.Code == 102 {
			log.Fatal("no such namespace")
		}
		log.Fatal(err)
	}
	log.Printf("published at offset %d", res.Offset)
}
```

v4:

```go
package main

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"log"
	"os"
	"time"

	"github.com/centrifugal/gocent/v4"
)

type Event struct {
	Text string `json:"text"`
}

func main() {
	c, err := gocent.New(gocent.Config{
		APIEndpoint: "http://localhost:8000/api",
		APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
	})
	if err != nil {
		log.Fatal(err) // the config is wrong, and the error says how
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	data, err := json.Marshal(Event{Text: "hello"})
	if err != nil {
		log.Fatal(err)
	}
	res, err := c.Publish(ctx, gocent.PublishRequest{
		Channel:        "news",
		Data:           jsontext.Value(data),
		Tags:           map[string]string{"kind": "greeting"},
		IdempotencyKey: "event-1",
	})
	if errors.Is(err, gocent.ErrUnknownChannel) {
		log.Fatal("no such namespace")
	}
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("published at offset %d", res.Offset)
}
```

- `New` checks the config and returns an error: an endpoint ending with a
  method name, a header that net/http would refuse, and the like.
- A request struct replaces the positional arguments and the `With...`
  options. `Data` is a `jsontext.Value`: convert bytes you already have with
  `jsontext.Value(data)`. It must be valid JSON, or the call fails before
  anything is sent.
- `errors.Is` matches Centrifugo errors by code, so there is no need to
  compare `Code` by hand.

## Broadcast

v3:

```go
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/centrifugal/gocent/v3"
)

func main() {
	c := gocent.New(gocent.Config{
		Addr: "http://localhost:8000/api",
		Key:  os.Getenv("CENTRIFUGO_API_KEY"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	channels := []string{"user:1", "user:2", "user:3"}
	res, err := c.Broadcast(ctx, channels, []byte(`{"text":"hello"}`),
		gocent.WithIdempotencyKey("notice-1"),
	)
	if err != nil {
		log.Fatal(err) // the request failed as a whole
	}
	// A channel which failed shows only here: err above was nil.
	for i, r := range res.Responses {
		if r.Error != nil {
			log.Printf("not published into %s: %v", channels[i], r.Error)
			continue
		}
		log.Printf("%s: offset %d", channels[i], r.Result.Offset)
	}
}
```

v4:

```go
package main

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"log"
	"os"
	"time"

	"github.com/centrifugal/gocent/v4"
)

func main() {
	c, err := gocent.New(gocent.Config{
		APIEndpoint: "http://localhost:8000/api",
		APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.Broadcast(ctx, gocent.BroadcastRequest{
		Channels:       []string{"user:1", "user:2", "user:3"},
		Data:           jsontext.Value(`{"text":"hello"}`),
		IdempotencyKey: "notice-1",
	})
	// err is nil only when every channel got the publication. A
	// *BroadcastError means Centrifugo ran the broadcast and some channels
	// failed; any other error means no channel got a reply.
	var be *gocent.BroadcastError
	if err != nil && !errors.As(err, &be) {
		log.Fatal(err)
	}
	for _, ch := range res.Channels {
		if ch.Err != nil {
			log.Printf("not published into %s: %v", ch.Channel, ch.Err)
			continue
		}
		log.Printf("%s: offset %d", ch.Channel, ch.Result.Offset)
	}
}
```

- In v3 a broadcast which failed in some channels returned a nil error: the
  failures were only in the reply. In v4 the error is a `*BroadcastError`
  then, so a plain `if err != nil` no longer misses them.
- Each entry of `res.Channels` names its channel, and has one whatever the
  error.
- Retried with the same `IdempotencyKey`, the broadcast is not published again
  into channels which already have it.

## Pipe, now a batch

v3:

```go
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/centrifugal/gocent/v3"
)

func main() {
	c := gocent.New(gocent.Config{
		Addr: "http://localhost:8000/api",
		Key:  os.Getenv("CENTRIFUGO_API_KEY"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p := c.Pipe()
	if err := p.AddPublish("news", []byte(`{"text":"hello"}`)); err != nil {
		log.Fatal(err)
	}
	if err := p.AddPresenceStats("news"); err != nil {
		log.Fatal(err)
	}
	if err := p.AddHistory("news", gocent.WithLimit(10)); err != nil {
		log.Fatal(err)
	}
	replies, err := c.SendPipe(ctx, p)
	if err != nil {
		log.Fatal(err)
	}
	// Replies come by position, as raw JSON to decode.
	for i, r := range replies {
		if r.Error != nil {
			log.Printf("command #%d failed: %v", i, r.Error)
		}
	}
	if replies[0].Error == nil {
		var pub gocent.PublishResult
		if err := json.Unmarshal(replies[0].Result, &pub); err != nil {
			log.Fatal(err)
		}
		log.Printf("published at offset %d", pub.Offset)
	}
	if replies[1].Error == nil {
		var stats gocent.PresenceStatsResult
		if err := json.Unmarshal(replies[1].Result, &stats); err != nil {
			log.Fatal(err)
		}
		log.Printf("%d clients", stats.NumClients)
	}
	if replies[2].Error == nil {
		var history gocent.HistoryResult
		if err := json.Unmarshal(replies[2].Result, &history); err != nil {
			log.Fatal(err)
		}
		log.Printf("%d publications in history", len(history.Publications))
	}
}
```

v4:

```go
package main

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"log"
	"os"
	"time"

	"github.com/centrifugal/gocent/v4"
)

func main() {
	c, err := gocent.New(gocent.Config{
		APIEndpoint: "http://localhost:8000/api",
		APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	b := c.NewBatch(gocent.BatchOptions{})
	pub := b.Publish(gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{"text":"hello"}`)})
	stats := b.PresenceStats(gocent.PresenceStatsRequest{Channel: "news"})
	history := b.History(gocent.HistoryRequest{Channel: "news", Limit: 10})

	// err is nil only when every command succeeded. A *BatchError means
	// Centrifugo ran the batch and some commands failed; any other error
	// means no command got a reply.
	err = b.Send(ctx)
	var batchErr *gocent.BatchError
	if err != nil && !errors.As(err, &batchErr) {
		log.Fatal(err)
	}
	// Each command has its own typed result, or its own error.
	if res, err := pub.Result(); err == nil {
		log.Printf("published at offset %d", res.Offset)
	} else {
		log.Printf("publish failed: %v", err)
	}
	if res, err := stats.Result(); err == nil {
		log.Printf("%d clients", res.NumClients)
	}
	if res, err := history.Result(); err == nil {
		log.Printf("%d publications in history", len(res.Publications))
	}
}
```

- Each command returns a `Pending` with its own typed result: no decoding by
  position.
- `Send` returns an error when any command failed, a `*BatchError` listing
  them. Every `Pending` still has its own outcome.
- A batch is sent once; to send the same commands again, build a new one.
  An empty batch sends nothing and returns nil, where v3 returned
  `ErrPipeEmpty`.
- `BatchOptions{}` runs the commands in order, as a v3 pipe did.
