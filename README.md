# gocent

[![Go Reference](https://pkg.go.dev/badge/github.com/centrifugal/gocent/v4.svg)](https://pkg.go.dev/github.com/centrifugal/gocent/v4)

Go client for the [HTTP server API](https://centrifugal.dev/docs/server/server_api)
of [Centrifugo](https://github.com/centrifugal/centrifugo): every API method,
including those of Centrifugo PRO. For a real-time client over WebSocket, see
[centrifuge-go](https://github.com/centrifugal/centrifuge-go).

```
go get github.com/centrifugal/gocent/v4
```

Requires Go 1.27, works with Centrifugo v6, and has no dependencies.

## Example

```go
client, err := gocent.New(gocent.Config{
	APIEndpoint: "http://localhost:8000/api",
	APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
})
if err != nil {
	log.Fatal(err) // the config is wrong, and the error says how
}

res, err := client.Publish(ctx, gocent.PublishRequest{
	Channel: "news",
	Data:    jsontext.Value(`{"text":"hello"}`),
})
if err != nil {
	return err
}
log.Printf("published at offset %d", res.Offset)
```

Every API method is a `Client` method taking a request struct and returning a
result struct, with fields mapping one to one onto the
[API](https://centrifugal.dev/docs/server/server_api). Create the `Client` once
and share it: it is safe for concurrent use. A call whose context has no
deadline times out after `Config.RequestTimeout`, ten seconds by default.

## Errors

| Error | Meaning | Check |
|---|---|---|
| `*gocent.Error` | Centrifugo refused the request or failed to carry it out | `errors.Is(err, gocent.ErrUnknownChannel)`, or `errors.As` for `Code` |
| `*gocent.BroadcastError`, `*gocent.BatchError` | some parts of a broadcast or batch failed - see below | `errors.As` |
| `*gocent.HTTPError` | the request did not reach the API method | `errors.Is(err, gocent.ErrUnauthorized)` for rejected credentials |
| `*gocent.DecodeError` | the reply was not Centrifugo's - usually `APIEndpoint` points elsewhere | `errors.As` |
| `gocent.ErrInvalidRequest` | the client could tell the request is wrong and did not send it | `errors.Is` |
| network, context | as usual | `errors.Is(err, context.DeadlineExceeded)` for any timeout |

Centrifugo adds error codes over time: an unknown code is still an
`*gocent.Error`, so match the codes you handle and treat the rest by class.

## Retries

`gocent.Retryable(err)` reports whether retrying may succeed: the error came from
a temporary condition - a broker being unavailable, a rate limit, the network -
not from the request.

A temporary error does not mean nothing was done: after a timeout, or even an
internal error, the publication may have happened. So retry a publication only
with an `IdempotencyKey`, and back off between a bounded number of attempts:

```go
req := gocent.PublishRequest{Channel: "orders:42", Data: data, IdempotencyKey: "order-42-paid"}
for attempt := 1; ; attempt++ {
	_, err := client.Publish(ctx, req)
	if err == nil || !gocent.Retryable(err) || attempt == 3 {
		return err
	}
	time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
}
```

Choosing keys:

- One key per publication or broadcast, reused only to retry that same one. A
  broadcast needs one key, not one per channel.
- Centrifugo remembers a key per channel, for five minutes by default. A retry
  within that time is answered with the first result and not published again -
  and so is a *different* publication into the same channel under the same key.
  A key built from an ID must name the event too: `order-42-paid`, not
  `order-42`.

## Broadcast and batch

`Broadcast` publishes the same data into many channels in one request; a `Batch`
sends many commands at once. Both return an error unless every part succeeded:

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
pub := b.Publish(gocent.PublishRequest{Channel: "news", Data: data})
stats := b.PresenceStats(gocent.PresenceStatsRequest{Channel: "news"})
if err := b.Send(ctx); err != nil {
	return err
}
res, err := pub.Result()   // each command has its own result and error
st, err := stats.Result()
```

Parts succeed or fail on their own. The error tells which case it is:

- a `*gocent.BroadcastError` or `*gocent.BatchError`: Centrifugo carried out the
  request, and the error lists the parts which failed, up to all;
- any other error: the request failed as a whole, and no part got a reply.
  After a timeout or a network error Centrifugo may still have carried it out.

`errors.Is` answers for the call as a whole: it does not look into the parts, so
one unknown channel among many does not make `errors.Is(err,
gocent.ErrUnknownChannel)` true. To go on despite some failed parts, check each
one - every part always has an outcome, whatever the error:

```go
res, err := client.Broadcast(ctx, req)
var be *gocent.BroadcastError
if err != nil && !errors.As(err, &be) {
	return err // no channel got a reply
}
for _, ch := range res.Channels {
	if ch.Err != nil {
		log.Printf("not published into %s: %v", ch.Channel, ch.Err)
	}
}
```

Retrying:

- a **broadcast**: send it again with the same `IdempotencyKey`. To skip channels
  which can never succeed, keep only those whose error is `Retryable` - see the
  example of `Retryable` in the [package docs](https://pkg.go.dev/github.com/centrifugal/gocent/v4#Retryable).
- a **batch**: build it again with the same keys, leaving out the commands which
  failed for good. A batch is sent once, so keep your own list of its requests.
  Commands without a key are repeated.

`BatchOptions{}` runs the commands one by one, in order; `Parallel` runs them
concurrently. Centrifugo PRO sends a batch's publications to its broker
together: each channel's publications still take effect in order, but
publications into different channels may not, unless the namespace sets
`publication_grouping_disabled`.

## Automatic batching

With `AutoBatch` set, calls made concurrently go out together once enough
requests are in flight - with no delay otherwise, since nothing waits for a
timer:

```go
client, err := gocent.New(gocent.Config{
	APIEndpoint: "http://localhost:8000/api",
	APIKey:      key,
	AutoBatch:   gocent.AutoBatch{MaxInFlight: 8},
})
```

Every call still gets its own result or error, and keeps its own deadline. Calls
sharing a batch share its request, though: if the request itself fails - the
network, a body too large - every call in it fails. A smaller `MaxInFlight`
batches more; 8 suits most applications. See
[`AutoBatch`](https://pkg.go.dev/github.com/centrifugal/gocent/v4#AutoBatch) for
the details.

## Authentication

`APIKey` is sent in the `X-API-Key` header. `BearerTokenFunc` instead sends a
token as `Authorization: Bearer`, for Centrifugo PRO's JWKS authentication - it
is called for every request, so return a cached token, as an
`oauth2.TokenSource` does. Leave both unset when mutual TLS, configured in
`HTTPClient`, or a proxy authenticates instead.

Rejected credentials make every call fail with an `*HTTPError` matching
`gocent.ErrUnauthorized`.

## More

- [Package docs](https://pkg.go.dev/github.com/centrifugal/gocent/v4): everything
  above in more detail, plus writing commands for Centrifugo's async consumers.
- A 404 from a Centrifugo PRO method means the server is Centrifugo OSS; the
  error says so.
- [Upgrading from v3](changelog.md#v400).
- Development: see [AGENTS.md](AGENTS.md); `make check` runs everything CI does.

MIT license.
