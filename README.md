# gocent

[![Go Reference](https://pkg.go.dev/badge/github.com/centrifugal/gocent/v4.svg)](https://pkg.go.dev/github.com/centrifugal/gocent/v4)

Go client for the [HTTP server API](https://centrifugal.dev/docs/server/server_api)
of [Centrifugo](https://github.com/centrifugal/centrifugo): publish into
channels, broadcast, manage subscriptions and connections, read history and
presence - every API method, including those of Centrifugo PRO.

If you are looking for a real-time client connecting over WebSocket, you need
[centrifuge-go](https://github.com/centrifugal/centrifuge-go).

## Install

```
go get github.com/centrifugal/gocent/v4
```

Requires Go 1.27 and works with Centrifugo v6. No dependencies: `go.mod` has no
`require` block.

## Example

```go
package main

import (
	"context"
	"encoding/json/jsontext"
	"log"
	"os"

	"github.com/centrifugal/gocent/v4"
)

func main() {
	client, err := gocent.New(gocent.Config{
		APIEndpoint: "http://localhost:8000/api",
		APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
	})
	if err != nil {
		log.Fatal(err) // the config is wrong, and the error says how
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
```

Every API method is a method of `Client`, taking a request struct and returning
a result struct. Request fields map one to one onto the
[API](https://centrifugal.dev/docs/server/server_api); fields left at their zero
value are not sent. `Data` is JSON, checked before it is sent.

Create the `Client` once and share it: it is safe for concurrent use.

## Errors

| Error | Meaning | Check |
|---|---|---|
| `*gocent.Error` | Centrifugo refused the request or failed to carry it out | `errors.Is(err, gocent.ErrUnknownChannel)`, or `errors.As` for `Code` |
| `*gocent.HTTPError` | the request did not reach the API method | `errors.Is(err, gocent.ErrUnauthorized)` for rejected credentials |
| `*gocent.DecodeError` | the reply was not Centrifugo's - usually `APIEndpoint` points elsewhere | `errors.As` |
| `gocent.ErrInvalidRequest` | the client could tell the request is wrong and did not send it | `errors.Is` |
| network, context | as usual | `errors.Is(err, context.DeadlineExceeded)` for any timeout |

Centrifugo adds error codes over time. An unknown code is still an
`*gocent.Error`: match the codes you handle and treat the rest by their class.

A call whose context has no deadline times out after `Config.RequestTimeout`,
ten seconds by default.

## Broadcast and batch

`Broadcast` publishes the same data into many channels in one request, and a
`Batch` sends many commands at once. Like every call, they return an error
unless they did everything asked:

```go
_, err := client.Broadcast(ctx, gocent.BroadcastRequest{
	Channels:       []string{"user:1", "user:2"},
	Data:           jsontext.Value(`{"text":"hello"}`),
	IdempotencyKey: messageID,
})
if err != nil {
	return err // retrying with the same IdempotencyKey is safe
}
```

```go
b := client.NewBatch(gocent.BatchOptions{})
for _, ev := range events {
	b.Publish(gocent.PublishRequest{Channel: ev.Channel, Data: ev.Data})
}
if err := b.Send(ctx); err != nil {
	return err
}
```

Their parts succeed or fail on their own. When Centrifugo carried out the
request and some parts failed - up to all - the error says which: a
`*gocent.BroadcastError` or a `*gocent.BatchError`. `errors.Is` sees through
both: `errors.Is(err, gocent.ErrUnknownChannel)` is true when any part failed
that way. Any other error means the request failed as a whole: no part got a
reply. After a timeout or a network error Centrifugo may still have carried it
out, so retry with the same `IdempotencyKey`.

Every part always has an outcome, whatever the error: `res.Channels` holds one
entry per requested channel, and every command of a batch has its `Pending`.
When the request failed as a whole, each part's error is that error. So a
caller which accepts some failed channels checks `err` first, then reads each:

```go
res, err := client.Broadcast(ctx, req)
var be *gocent.BroadcastError
if err != nil && !errors.As(err, &be) {
	return err // no channel got a reply
}
for _, ch := range res.Channels {
	if ch.Err != nil {
		log.Printf("not published into %s: %v", ch.Channel, ch.Err)
		continue
	}
	log.Printf("%s: offset %d", ch.Channel, ch.Result.Offset)
}
```

A broadcast inside a batch returns what a direct broadcast returns, and one
which failed in any channel counts as a failed command.

`BatchOptions`, given to `NewBatch`, say how Centrifugo runs the batch: the
zero value runs the commands one by one, in order; `Parallel` runs them
concurrently. A batch keeps its commands' requests as given until it is sent -
their slices, such as `Channels` and `Data`, are shared with the caller, so do
not modify them before `Send` returns.

## Automatic batching

Publishing from many goroutines - a server handling requests - can let the
client batch for you:

```go
client, err := gocent.New(gocent.Config{
	APIEndpoint: "http://localhost:8000/api",
	APIKey:      key,
	AutoBatch:   gocent.AutoBatch{MaxInFlight: 8},
})
```

While fewer than `MaxInFlight` requests are in flight, every call is sent at
once, alone. Once that many are, further calls wait for one of them to finish -
not for a timer - and then go out together as one batch. The busier the client,
the larger the batches. Every call still gets its own command's result or
error.

Calls sharing a batch share its request, though: when the batch request itself
fails - the network, the credentials, a body too large for Centrifugo or a
proxy - every call in it fails with that error. Keep payloads well within the
body limit, or send large ones from a client without `AutoBatch`. A batch
request is bounded by `RequestTimeout`, not by the deadlines of the calls in
it.

A smaller `MaxInFlight` batches more: a value above the concurrency your load
needs leaves nearly every call sent alone. 8 suits most applications.

Centrifugo PRO sends a batch's publications to its broker together, so larger
batches save more of Centrifugo's and Redis's work.

## Authentication

Requests carry what the config sets, and nothing else:

- `APIKey`, sent in the `X-API-Key` header;
- `BearerTokenFunc`, for Centrifugo PRO's JWKS authentication of the API. It is
  called for every request, and its token is sent as
  `Authorization: Bearer <token>`. Return a cached token, refreshed before it
  expires - an `oauth2.TokenSource` does exactly that:

  ```go
  ts := clientCredentials.TokenSource(ctx) // golang.org/x/oauth2/clientcredentials

  client, err := gocent.New(gocent.Config{
  	APIEndpoint: "https://centrifugo.internal/api",
  	BearerTokenFunc: func(context.Context) (string, error) {
  		t, err := ts.Token()
  		if err != nil {
  			return "", err
  		}
  		return t.AccessToken, nil
  	},
  })
  ```

  If getting a token fails, the call fails before anything is sent, with an
  error wrapping the token source's own. It may not be combined with `APIKey`:
  Centrifugo PRO judges a request with a bearer token by the token alone.
- Neither, when requests are authenticated another way or not at all - such as
  mutual TLS, set up in the HTTP client:

  ```go
  hc := gocent.DefaultHTTPClient()
  hc.Transport.(*http.Transport).TLSClientConfig = &tls.Config{
  	Certificates: []tls.Certificate{clientCert},
  	RootCAs:      centrifugoCA,
  }
  client, err := gocent.New(gocent.Config{
  	APIEndpoint: "https://centrifugo.internal:9000/api",
  	HTTPClient:  hc,
  })
  ```

  Mutual TLS combines with `APIKey` or `BearerTokenFunc` too.

Credentials Centrifugo rejects make every call fail with an `*HTTPError`
matching `gocent.ErrUnauthorized`.

## Async consumers

Instead of calling Centrifugo, an application can leave a command for it in a
PostgreSQL outbox table, a Kafka topic or another source Centrifugo's
[async consumers](https://centrifugal.dev/docs/server/consumers) read. Such a
command is two things: the API method's name, and its request as JSON. A
request struct gives both - `APIMethod()` the name, and JSON encoding the
request:

```go
req := gocent.PublishRequest{Channel: "news", Data: data}
payload, err := json.Marshal(req) // {"channel":"news","data":{...}}
if err != nil {
	return err
}

// PostgreSQL outbox: written in the same transaction as the change it announces.
_, err = tx.ExecContext(ctx,
	"INSERT INTO centrifugo_outbox (method, payload, partition) VALUES ($1, $2, 0)",
	req.APIMethod(), payload) // "publish", {"channel":"news",...}
```

A Kafka message carries both in one JSON object:

```go
msg, err := json.Marshal(struct {
	Method  string                `json:"method"`
	Payload gocent.PublishRequest `json:"payload"`
}{req.APIMethod(), req})
// {"method":"publish","payload":{"channel":"news","data":{...}}}
```

`encoding/json` and `encoding/json/v2` encode requests the same way. The
consumers run commands which change state - publish, broadcast, unsubscribe,
disconnect and the like - and not `batch`. Nothing is checked on the way: a
mistake in the request is Centrifugo's to report, in its logs.

## Easy mistakes

- **A 404 from a Centrifugo PRO method means the server is OSS**, not that
  `APIEndpoint` is wrong - if other methods work, `APIEndpoint` is fine. In a
  batch the same call fails with `gocent.ErrNotFound`. PRO-only methods say so
  in their docs.
- **An error from `Broadcast` or `Batch.Send` does not mean nothing was
  done.** A `*gocent.BroadcastError` or `*gocent.BatchError` says which parts
  succeeded. Retry a broadcast with the same `IdempotencyKey`.
- **`New` does not require credentials**, since mutual TLS or a proxy may
  authenticate instead. An API key which failed to load shows as
  `gocent.ErrUnauthorized` on the first call.
- **A larger `AutoBatch.MaxInFlight` batches less, not more.** Calls are
  batched only once that many requests are in flight.

## Upgrading from v3

See the [changelog](changelog.md#v400).

## Development

See [AGENTS.md](AGENTS.md). `make check` runs everything CI does.

## License

MIT.
