# gocent

[![Go Reference](https://pkg.go.dev/badge/github.com/centrifugal/gocent/v4.svg)](https://pkg.go.dev/github.com/centrifugal/gocent/v4)

Go client for the [HTTP server API](https://centrifugal.dev/docs/server/server_api)
of [Centrifugo](https://github.com/centrifugal/centrifugo): every API method,
including those of Centrifugo PRO. For a real-time client over WebSocket, see
[centrifuge-go](https://github.com/centrifugal/centrifuge-go).

Centrifugo PRO is commercial software, under the [Centrifugal Labs License
Agreement](https://centrifugal.dev/license) - not under gocent's MIT license.
gocent's methods for it are MIT licensed like the rest of gocent; calling them
needs a Centrifugo PRO server, whose use that agreement governs.

```
go get github.com/centrifugal/gocent/v4
```

No dependencies.

| | Version |
|---|---|
| Go | 1.27 or newer |
| Centrifugo | v6, any release |
| Map and shared poll methods | Centrifugo v6.8.0 or newer |
| `Error.Temporary`, `ErrTooManyRequests`, refused requests reported as such rather than as internal errors | Centrifugo v6.10.0 or newer |
| Methods marked "Centrifugo PRO only" | Centrifugo PRO v6 |

With an older Centrifugo, `Retryable` still works: it treats the internal
error, code 100, as temporary when the server does not mark errors.

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
and share it: it is safe for concurrent use.

## Timeouts

Each call is bounded by its context, so calls get their own timeouts:

```go
ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
defer cancel()
info, err := client.Info(ctx, gocent.InfoRequest{})
```

A call whose context has no deadline times out after `Config.RequestTimeout`,
ten seconds by default. `http.Client.Timeout`, in `Config.HTTPClient`, bounds
every request of the client on top of that. However a call times out,
`errors.Is(err, context.DeadlineExceeded)` is true.

`BroadcastWithRetry` makes several requests in one call: its context bounds
them all, waits between them included, and
`BroadcastRetryOptions.AttemptTimeout` bounds each one - so that an attempt
which hangs leaves time to retry. In a retry loop of your own, give each
attempt its own `context.WithTimeout`.

## Errors

| Error | Meaning | Check |
|---|---|---|
| `*gocent.Error` | Centrifugo refused the request or failed to carry it out | `errors.Is(err, gocent.ErrUnknownChannel)`, or `errors.AsType` for `Code` |
| `*gocent.BroadcastFailedChannelsError`, `*gocent.BatchFailedCommandsError` | some parts of a broadcast or batch failed - see below | `errors.AsType` |
| `*gocent.HTTPError` | the request did not reach the API method | `errors.Is(err, gocent.ErrUnauthorized)` for rejected credentials |
| `*gocent.DecodeError` | the reply was not Centrifugo's - usually `APIEndpoint` points elsewhere | `errors.AsType` |
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
req := gocent.PublishRequest{
	Channel: "orders:42",
	Data:    data,
	// The same key on every attempt: a publication which happened in an
	// earlier attempt, even one reported as failed, is not published again.
	IdempotencyKey: "order-42-paid",
}
backoff := gocent.ExponentialBackoff(200*time.Millisecond, 5*time.Second)
for attempt := 1; ; attempt++ {
	_, err := client.Publish(ctx, req)
	if err == nil || !gocent.Retryable(err) || attempt == 3 {
		return err
	}
	// Ends early, with the context's error, when ctx is cancelled or out of time.
	if err := gocent.Wait(ctx, backoff(attempt)); err != nil {
		return err
	}
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
- The memory, Redis and PostgreSQL brokers keep keys. The NATS broker ignores
  them, so with it a retry publishes again.

Retry a broadcast with `client.BroadcastWithRetry` rather than a retry library:
see below. With a retry library for other calls, give it `gocent.Retryable` as
the condition to retry on, and set an `IdempotencyKey`.

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
if _, err := b.Send(ctx); err != nil {
	return err
}
res, err := pub.Result()   // each command has its own result and error
st, err := stats.Result()
```

Parts succeed or fail on their own, and the result tells which case it is:

- `res.Replied` is true: Centrifugo carried the request out and replied for
  every part. `res.Failed()` lists the parts which failed, up to all, and the
  error is a `*gocent.BroadcastFailedChannelsError` or
  `*gocent.BatchFailedCommandsError` listing them too - or nil when none did.
- `res.Replied` is false: the request failed as a whole - the network, the
  credentials, an invalid request - and no part got a reply. After a timeout
  or a network error Centrifugo may still have carried it out.

`errors.Is` answers for the call as a whole: it does not look into the parts,
so one unknown channel among many does not make
`errors.Is(err, gocent.ErrUnknownChannel)` true. To go on despite some failed
parts, stop only when Centrifugo did not reply, and read the parts - every
part always has an outcome, whatever the error:

```go
res, err := client.Broadcast(ctx, req)
if !res.Replied {
	return err // no channel got a reply
}
for _, ch := range res.Failed() {
	log.Printf("not published into %s: %v", ch.Channel, ch.Err)
}
```

The same for a batch: `res, err := b.Send(ctx)`, then `res.Replied` and
`res.Failed()`; each command's typed result is in its `Pending`.

Retrying:

- a **broadcast**: `client.BroadcastWithRetry` does it. It sends again only the
  channels which may still succeed, all attempts with one `IdempotencyKey` -
  made for the call when the request has none - and returns every channel's
  last outcome, as `Broadcast` does:

  ```go
  res, err := client.BroadcastWithRetry(ctx, req, gocent.BroadcastRetryOptions{})
  ```

  The zero `BroadcastRetryOptions` retries once, after about 100 milliseconds;
  `MaxAttempts` sets how many attempts in all. `OnAttemptErr` is called after
  each failed attempt which will be retried, with the channels to retry and
  their errors, and decides what happens before the retry: it waits, logs,
  counts, or returns an error to stop. Any strategy fits in it:

  ```go
  backoff := gocent.ExponentialBackoff(200*time.Millisecond, 5*time.Second)
  res, err := client.BroadcastWithRetry(ctx, req, gocent.BroadcastRetryOptions{
  	MaxAttempts: 5,
  	OnAttemptErr: func(ctx context.Context, a gocent.BroadcastAttempt) error {
  		log.Printf("attempt %d: %d channels to retry", a.Attempt, len(a.Retry))
  		return gocent.Wait(ctx, backoff(a.Attempt)) // or gocent.Wait(ctx, time.Second)
  	},
  })
  ```

  `ExponentialBackoff` gives a longer wait after each failed attempt, with some
  randomness, so that clients which failed together do not retry together; the
  default waits it. A `*gocent.HTTPError` in `a.Err` carries the response's
  `Header`: wait at least its `Retry-After` to honour one. `AttemptTimeout`
  bounds each attempt - see [Timeouts](#timeouts).
  `res.IdempotencyKey` is the key the attempts used: to retry the broadcast
  later, after it gave up - from a job queue, say - send it with that key.
  `res.Attempts`, each channel's `Attempts` and `res.Failed()` tell what
  happened.

  A retry library which calls `Broadcast` again on any error would instead
  send it to every channel again, those which already got it included.
- a **batch**: build a new one, with the same keys, from the commands
  `res.Failed()` lists with a retryable error - every command when the batch
  failed as a whole. A batch is sent once, so keep your own list of its
  requests: `Failed()` gives each command's `Index` in it. Commands without a
  key are repeated.

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

## Tracing

Tracing and metrics come from the HTTP client's transport. With OpenTelemetry:

```go
hc := gocent.DefaultHTTPClient()
hc.Transport = otelhttp.NewTransport(hc.Transport)
client, err := gocent.New(gocent.Config{APIEndpoint: endpoint, APIKey: key, HTTPClient: hc})
```

Each call is a span in its caller's trace, and Centrifugo continues the trace
when its OpenTelemetry API tracing is on. An automatic batch belongs to no
single caller: `Config.BatchContext` makes its request's context from the
callers', with a link to each caller's span, say. `Config.OnRequest` is told
of every request - method, commands and how many failed, duration, status,
error - for metrics without a transport of your own. See the
[package docs](https://pkg.go.dev/github.com/centrifugal/gocent/v4#hdr-Tracing_and_metrics).

## Shutdown

`client.Close(ctx)` refuses new calls with `gocent.ErrClosed` and waits for the
ones in progress - calls queued for an automatic batch included - so that what
was accepted is sent before the process exits. Call it on SIGTERM:

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
if err := client.Close(ctx); err != nil {
	log.Printf("gocent: calls still in progress at exit: %v", err)
}
```

## Proxy

The `proxy` package is the other direction: the backend side of Centrifugo's
HTTP proxy, which calls your application when a client connects, subscribes,
publishes or makes an RPC call. `proxy.Handlers` has a method per proxy type,
taking a function:

```go
h := proxy.Handlers{OnError: logProxyError} // the zero value works too
mux.Handle("/centrifugo/subscribe", h.Subscribe(
	func(r *http.Request, req proxy.SubscribeRequest) (proxy.SubscribeResult, error) {
		if !canRead(r.Context(), req.User, req.Channel) {
			return proxy.SubscribeResult{}, &proxy.Error{Code: 403, Message: "permission denied"}
		}
		return proxy.SubscribeResult{}, nil
	}))
```

Return a result to accept the request, a `*proxy.Error` to reject it with an
error the client receives, or a `*proxy.Disconnect` to close the client's
connection; any other error fails the request, and goes to `OnError` to be
logged. Keep proxy endpoints reachable
by Centrifugo only: whoever can call them can claim to be any user. See the
[package docs](https://pkg.go.dev/github.com/centrifugal/gocent/v4/proxy).

## Tokens

The `token` package issues the JWTs Centrifugo authenticates clients with -
connection and subscription tokens, with Centrifugo's claims, signed with
HMAC, RSA, ECDSA or Ed25519 - and serves the endpoints the `getToken`
callbacks of client SDKs call:

```go
key := token.HMAC(secret) // client.token.hmac_secret_key
h := token.Handlers{OnError: logError}
mux.Handle("/centrifugo/connection_token", h.Connection(
	func(r *http.Request) (string, error) {
		user, ok := sessionUser(r)
		if !ok {
			return "", token.ErrUnauthorized // 403: the client stops asking
		}
		return token.Connection(key, token.ConnectionClaims{User: user, ExpiresAt: time.Now().Add(time.Hour)})
	}))
```

`h.Subscription` serves subscription tokens the same way, with the channel the
client asks for.

## Shared poll signatures

Clients tracking keys of a [shared poll](https://centrifugal.dev/docs/server/shared_poll)
channel need a signature from the backend. The `sharedpoll` package makes one,
and serves the endpoint a subscription's `getSignature` callback calls:

```go
h := sharedpoll.Handlers{OnError: logError}
mux.Handle("/centrifugo/shared_poll_signature", h.Signature(
	func(r *http.Request, channel string, keys []string) ([]string, string, error) {
		allowed := readable(r.Context(), user, channel, keys) // the rest are revoked
		return allowed, sharedpoll.Sign(secret, user, channel, allowed, time.Now().Add(time.Hour)), nil
	}))
```

Shared poll subscriptions are experimental in Centrifugo, and the package
follows their changes.

## More

- [Package docs](https://pkg.go.dev/github.com/centrifugal/gocent/v4): everything
  above in more detail, plus writing commands for Centrifugo's async consumers.
- A 404 from a Centrifugo PRO method means the server is Centrifugo OSS; the
  error says so.
- Upgrading from v3: [MIGRATING.md](MIGRATING.md) has complete programs before
  and after, and the [changelog](changelog.md#migrating-from-v3) maps every
  call.
- Development: see [AGENTS.md](AGENTS.md); `make check` runs everything CI does.

MIT license. Centrifugo PRO, which the methods marked "Centrifugo PRO only"
call, is commercial software under its own
[license](https://centrifugal.dev/license).
