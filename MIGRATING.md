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
	// err is nil only when every channel got the publication. This program
	// reports each failed channel below, as the v3 one did, so it stops only
	// when no channel got a reply.
	if err = gocent.WholeFailure(err); err != nil {
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

- In v3 a broadcast which failed in some channels returned a nil error. In v4
  the error is a `*BroadcastError` then - see
  [Failed channels are errors now](#failed-channels-are-errors-now).
- Each entry of `res.Channels` names its channel, and has one whatever the
  error.
- Retried with the same `IdempotencyKey`, the broadcast is not published again
  into channels which already have it.

### Failed channels are errors now

**The mistake v3 made easy.** In v3, `Broadcast` returned a nil error as long
as the request reached Centrifugo, even when some channels failed: their
errors were only in `res.Responses`. So the usual Go code

```go
_, err := c.Broadcast(ctx, channels, data)
if err != nil {
	return err
}
```

treated a broadcast with failed channels as a success. v3 did report the
failed channels - each entry of `res.Responses` has its `Error` - but with a
nil error nothing pointed the caller there, and code which checked only `err`,
as Go code usually does, never looked. Those channels did not get the
publication - a channel in a namespace removed from the configuration, say, or
channels on a Redis shard which was briefly down - and the caller neither
logged nor retried it. Code which checked every `Error` in `res.Responses` was
right; the trap was that leaving the check out was the natural way to write
the call.

**What v4 does.** The error is nil only when every channel got the
publication. When Centrifugo ran the broadcast and some channels failed, the
error is a `*gocent.BroadcastError` listing them, so the same `if err != nil`
now catches them - and, for a temporary failure, can retry them. Check
code which only looked at `err`: it reports failures it used to miss, which
is the point.

**When to keep v3's behaviour.** Code which already handled every channel on
its own - it checked each reply, logged or counted the failed channels, and
went on on purpose - may want a broadcast with some failed channels to stay a
success, as in v3. For that, pass the error through `gocent.WholeFailure`,
which drops a `*BroadcastError` and keeps any other error, and read the
channels as before:

```go
res, err := c.Broadcast(ctx, req)
if err = gocent.WholeFailure(err); err != nil {
	return err // the request failed as a whole: no channel got a reply
}
for _, ch := range res.Channels {
	if ch.Err != nil {
		log.Printf("not published into %s: %v", ch.Channel, ch.Err)
	}
}
```

Do this only where failed channels are expected and handled. Elsewhere, let
the error through.

### Broadcast checklist

What else changes for code written against v3's `Broadcast`:

- **A channel's result is a value, not a pointer.** v3 set `Result` to nil for
  a failed channel; v4's is a zero value then, and `Err` tells:

  ```go
  // v3
  if r.Error == nil {
  	log.Println(r.Result.Offset)
  }
  // v4
  if ch.Err == nil {
  	log.Println(ch.Result.Offset)
  }
  ```

- **A failed request still has every channel.** v3 returned no responses when
  the request failed as a whole; v4 returns an entry per channel, each with
  that error. Tell the two failures apart by the error - a `*BroadcastError`
  or not - not by the number of entries.
- **Some broadcasts are refused before sending,** with an error wrapping
  `gocent.ErrInvalidRequest`: no channels, an empty channel name among them,
  no data, or data which is not valid JSON or repeats a key in an object. v3
  sent them, and Centrifugo answered with an error.
- **Match errors by code, not by message.** `Error.Code` is a `uint32` now,
  and messages changed: v3's `unknown channel: 102` is
  `gocent: unknown channel (code 102)`. Use `errors.Is`:

  ```go
  // v3
  if r.Error != nil && r.Error.Code == 102 { ... }
  // v4
  if errors.Is(ch.Err, gocent.ErrUnknownChannel) { ... }
  ```

- **Retries have helpers.** Instead of a retry loop of your own,
  `gocent.Retryable(err)` says whether a retry may succeed, and
  `be.RetryableChannels()` which channels to retry, leaving out those which
  failed for good. Retry with the same `IdempotencyKey`. See the example of
  `Retryable` in the
  [package docs](https://pkg.go.dev/github.com/centrifugal/gocent/v4#Retryable).

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

	// err is nil only when every command succeeded. This program reads each
	// command's outcome below, so it stops only when no command got a reply.
	if err = gocent.WholeFailure(b.Send(ctx)); err != nil {
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
