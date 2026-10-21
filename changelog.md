# v4.0.0

A new major version for Centrifugo v6: every API method, the current HTTP API
endpoints, typed errors and automatic batching. Requires Go 1.27. Import path
`github.com/centrifugal/gocent/v4`.

* **Every API method.** Types and methods are generated from Centrifugo's
  `api.proto`, so every method - including map, shared poll and the Centrifugo
  PRO methods - is a `Client` method, and every request field is a struct
  field.
* **Current endpoints.** Each method is sent to its own endpoint
  (`/api/publish`, ...), and batches to `/api/batch`, instead of the legacy
  `/api` endpoint.
* **Errors.** Centrifugo errors are `*gocent.Error`, matched by code with
  `errors.Is(err, gocent.ErrUnknownChannel)`. HTTP failures are
  `*gocent.HTTPError` (`errors.Is(err, gocent.ErrUnauthorized)` for a bad key),
  and replies that are not Centrifugo's are `*gocent.DecodeError`.
* **Broadcast and batch report each part.** They return an error unless every
  channel or command succeeded; when some failed, it is a
  `*gocent.BroadcastFailedChannelsError` or `*gocent.BatchFailedCommandsError`
  saying which. Every part always has an outcome: `res.Channels` holds one entry
  per requested channel, and every batch command its `Pending`. Both results
  answer the same two questions: `Replied` - did Centrifugo carry the request
  out and reply for every part, or did it fail as a whole - and `Failed()`, the
  parts which failed. `Batch.Send` returns a `BatchResult` with them.
* **Typed batches.** `client.NewBatch(opts)` gives a `Batch` with a method per
  command, each returning a `Pending` holding that command's typed result.
* **Automatic batching.** `Config.AutoBatch` sends concurrent calls together
  once `MaxInFlight` requests are in flight, with no delay otherwise.
* **Retries.** `gocent.Retryable(err)` tells whether retrying a call may
  succeed. It follows `Error.Temporary`, which Centrifugo sets on errors from a
  temporary condition, such as the new `ErrTooManyRequests`, and treats network
  failures, timeouts and HTTP 429 or 5xx as retryable too. A retryable error
  does not mean nothing was done: retry publications with the same
  `IdempotencyKey`. `Client.BroadcastWithRetry` retries a broadcast: only the
  channels which may still succeed, with one `IdempotencyKey` for all its
  attempts - made for the call when the request has none - the attempts' results
  merged. `BroadcastRetryOptions.MaxAttempts` bounds the attempts - two by
  default, so one retry. `BroadcastRetryOptions.OnAttemptErr` is called after
  each failed attempt which will be retried, with the channels to retry and
  their errors: it waits before the retry, logs or counts it, or returns an
  error to stop.
  By default it waits `gocent.ExponentialBackoff(100ms, 5s)(attempt)`.
  `ExponentialBackoff` gives the wait after a failed attempt, for any retry
  loop, and `gocent.Wait` waits until a context ends.
  `BroadcastResult.IdempotencyKey` names the key used, for a retry later.
  `Attempts`, on the result and on each channel, and `BroadcastResult.Failed()`
  tell what happened.
  `BroadcastFailedChannelsError.RetryableChannels()` gives the channels to retry
  for a loop of your own. `BroadcastRetryOptions.AttemptTimeout` bounds each
  attempt, while the context bounds the call. `HTTPError.Header` holds the
  response's headers: a proxy's request ID, or a `Retry-After`.
* **Shutdown.** `client.Close(ctx)` refuses new calls with `gocent.ErrClosed`
  and waits for those in progress, calls queued for an automatic batch
  included.
* **Observability without dependencies.** `Config.OnRequest` is told of
  every request - method, commands, failed commands, automatic batch or not,
  duration, status, error - and `Config.BatchContext` gives an automatic
  batch's request a context made from its callers', for a span linked to
  each caller's.
* **Bearer token authentication.** `Config.BearerTokenFunc` sends a token
  with every request as `Authorization: Bearer`, for Centrifugo PRO's JWKS
  authentication of the API.
* **Configuration is validated.** `New` returns an error for a missing or
  malformed `APIEndpoint`, and contradictory settings.
* **Safer defaults.** A default HTTP client with a connection pool - at most
  256 connections to the server, all kept while idle, so bursts of calls do
  not open and close connections - and a per-request timeout when the context
  has none. Redirects are not followed:
  Centrifugo's API never redirects, and following one would send the API key
  to wherever it points. A redirect is an `HTTPError` naming the likely
  cause, a wrong `APIEndpoint`.
* **Async consumers.** `APIMethod()` on every request gives the method name for
  building events from a request.
* **Tokens.** The new `token` package signs connection and subscription
  tokens with Centrifugo's claims - `token.Connection`, `token.Subscription` -
  with HMAC, RSA, ECDSA or Ed25519 keys - Centrifugo PRO's claims included:
  capabilities, labels, server-side tag filters - and `token.Handlers` serves the
  endpoints the `getToken` callbacks of client SDKs call: `{"token": ...}`,
  never cached, 403 for `token.ErrUnauthorized`.
* **Shared poll signatures.** The new `sharedpoll` package signs the keys a
  client may track - `sharedpoll.Sign` - and `sharedpoll.Handlers` serves the
  endpoint centrifuge-js's `getSignature` calls: keys left out are revoked,
  `sharedpoll.ErrUnauthorized` ends the subscription.
* **Proxy handlers.** The new `proxy` package serves Centrifugo's HTTP proxy:
  the request and result types of every proxy type, generated from
  Centrifugo's `proxy.proto`, and `proxy.Handlers` making an `http.Handler`
  for each - connect,
  refresh, subscribe, publish, RPC, sub refresh, map publish and remove,
  shared poll refresh, and Centrifugo PRO's cache empty and channel state
  notifications. A function returns a result, or a `*proxy.Error` or
  `*proxy.Disconnect` for the client; the handler decodes the request and
  encodes the answer Centrifugo expects.

### Migrating from v3

[MIGRATING.md](MIGRATING.md) has complete programs for publishing,
broadcasting and batching, as written with v3 and with v4.

Change the import path to `github.com/centrifugal/gocent/v4`, then:

| v3 | v4 |
|---|---|
| `c := gocent.New(gocent.Config{Addr: addr, Key: key})` | `c, err := gocent.New(gocent.Config{APIEndpoint: addr, APIKey: key})` - it returns an error for a wrong config |
| `Config.GetAddr func() (string, error)` | `Config.APIEndpointFunc func(context.Context) (string, error)` |
| `c.SetHTTPClient(hc)` | `Config.HTTPClient: hc` - the field itself is unchanged |
| `DefaultHTTPClient` variable, with a 1 second timeout | `gocent.DefaultHTTPClient()`, returning a new client with no timeout of its own; `Config.RequestTimeout` (10 seconds by default) bounds each call whose context has no deadline |
| `c.Publish(ctx, ch, data, gocent.WithSkipHistory(true))` | `c.Publish(ctx, gocent.PublishRequest{Channel: ch, Data: data, SkipHistory: true})` |
| `WithTags`, `WithIdempotencyKey`, `WithDelta`, `WithVersion`, `WithVersionEpoch`, `WithB64data` | `Tags`, `IdempotencyKey`, `Delta`, `Version`, `VersionEpoch`, `B64Data` fields |
| `c.Broadcast(ctx, channels, data)`; `res.Responses[i].Error`, `res.Responses[i].Result` | `c.Broadcast(ctx, gocent.BroadcastRequest{Channels: channels, Data: data})`; `res.Channels[i].Err`, `res.Channels[i].Result`, and `res.Channels[i].Channel` |
| `Subscribe`, `Unsubscribe`, `Disconnect`, `Presence`, `PresenceStats`, `History`, `HistoryRemove`, `Channels` with options such as `WithLimit`, `WithPattern` | request structs such as `gocent.HistoryRequest{Channel: ch, Limit: 10}`; `Subscribe`, `Unsubscribe`, `Disconnect` and `HistoryRemove` now return a result too. `Info(ctx)` is unchanged |
| `WithSubscribeInfo`, `WithSubscribeData` | `Info`, `Data` fields |
| `WithSubscribeClient`, `WithUnsubscribeClient`, `WithDisconnectClient` | `Client` field |
| `WithDisconnect`, `WithDisconnectClientWhitelist` | `Disconnect`, `Whitelist` fields |
| `WithRecoverSince(&pos)`, `WithSince(&pos)` | `RecoverSince: pos`, `Since: pos` - a value, not a pointer |
| `WithPresence(true)`, `WithJoinLeave(true)`, `WithPosition(true)`, `WithRecover(true)` | `Override: gocent.SubscribeOptionOverride{Presence: gocent.Bool(true)}`, with the fields `Presence`, `JoinLeave`, `ForcePositioning`, `ForceRecovery` - Centrifugo takes these only as overrides of the channel's options |
| `data []byte`, `json.RawMessage` in subscribe options | `jsontext.Value`, which takes a `[]byte` or a `json.RawMessage` as is - since Go 1.27 `json.RawMessage` is the same type; it must be valid JSON, with no key repeated in an object, or the call fails with `gocent.ErrInvalidRequest` before anything is sent |
| `p := c.Pipe()`, `err := p.AddPublish(...)`, `replies, err := c.SendPipe(ctx, p)` | `b := c.NewBatch(gocent.BatchOptions{})`, `pub := b.Publish(...)`, `_, err := b.Send(ctx)`, `res, err := pub.Result()` |
| `p.Reset()` to reuse a pipe | a new `NewBatch`: a batch is sent once |
| `ErrPipeEmpty` | none: sending an empty batch does nothing and returns nil |
| `*gocent.Error`, `err.(*gocent.Error)` | unchanged, and `errors.Is(err, gocent.ErrUnknownChannel)` now matches by code |
| `ErrStatusCode{Code, Body}` | `*gocent.HTTPError{StatusCode, Body}`; `errors.Is(err, gocent.ErrUnauthorized)` for a 401 |
| `ErrMalformedResponse` | `*gocent.DecodeError` |

Behaviour to check when migrating:

* **Failed channels and commands are errors now.** In v3, `Broadcast` and
  `SendPipe` returned `err == nil` when single channels or commands failed, and
  the caller had to inspect every reply. In v4 they return an error unless
  everything succeeded - a `*gocent.BroadcastFailedChannelsError` or
  `*gocent.BatchFailedCommandsError` when Centrifugo carried the request out and
  some parts failed. Code which checked each reply keeps working: `res.Channels`
  still holds every channel's outcome and every `Pending` its command's. Code
  which only checked `err` now sees failures it used to miss. See the examples
  below, and [MIGRATING.md](MIGRATING.md#failed-channels-are-errors-now) for
  why, and for keeping v3's behaviour where failed channels are expected.
* **Batches are ordered unless asked otherwise.** `BatchOptions{}` runs the
  commands one by one, in the order they were added; `Parallel: true` runs
  them concurrently. Since v6.9.7, Centrifugo PRO sends the publications of
  a batch to its broker together: each channel's publications still take
  effect in order, but publications into different channels may not. A
  namespace with `publication_grouping_disabled` keeps the strict order.
* **Invalid requests are not sent.** A publication or broadcast without a
  channel or data, a broadcast with an empty channel name, or data which is
  not valid JSON or repeats a key in an object, fails before sending with an
  error wrapping `gocent.ErrInvalidRequest`; in a batch it fails the whole
  batch.
* **A failed broadcast still has every channel.** v3 returned no responses
  when the request failed as a whole; v4 returns an entry per channel, each
  with that error. A channel's `Result` is a value, not a pointer: check
  `Err`, not `Result != nil`. See [MIGRATING.md](MIGRATING.md#broadcast-checklist).
* **Timeouts.** v3's default HTTP client gave up after 1 second. v4 bounds a
  call by its context's deadline, or by `Config.RequestTimeout`, 10 seconds by
  default.
* **The API key** is sent in the `X-API-Key` header instead of
  `Authorization: apikey <key>`. Centrifugo accepts both; a proxy in front of
  it which looks at the header needs updating.

### Broadcast and batch, before and after

A broadcast in v3 reported a failed channel only in its reply:

```go
res, err := c.Broadcast(ctx, []string{"a", "b"}, data)
if err != nil {
	return err // the request failed as a whole
}
for i, r := range res.Responses {
	if r.Error != nil {
		log.Printf("channel #%d: %v", i, r.Error) // err above was nil
	}
}
```

In v4 the error tells which case it is:

```go
res, err := c.Broadcast(ctx, gocent.BroadcastRequest{Channels: []string{"a", "b"}, Data: data})
be, partial := errors.AsType[*gocent.BroadcastFailedChannelsError](err)
switch {
case err == nil:
	// Published into every channel.
case partial:
	// Centrifugo ran the broadcast, and some channels failed - up to all.
	for _, f := range be.Failed {
		log.Printf("not published into %s: %v", f.Channel, f.Err)
	}
default:
	// No channel got a reply: the network, the credentials, an invalid
	// request. res.Channels still has an entry per channel, each with err.
	return err
}
```

* `errors.Is(err, ...)` answers for the broadcast as a whole: it does not look
  into a `*BroadcastFailedChannelsError`, so one unknown channel of many does
  not make `errors.Is(err, gocent.ErrUnknownChannel)` true. Each channel's error
  is in `be.Failed`: `errors.Is(f.Err, gocent.ErrUnknownChannel)`. The same
  holds for a `*BatchFailedCommandsError` and its commands.
* After a timeout or a network error Centrifugo may still have published. Set
  `IdempotencyKey` to retry the whole broadcast safely: channels which already
  have the publication do not get it again.

A batch in v3 gave raw replies to decode:

```go
p := c.Pipe()
_ = p.AddPublish("a", data)
_ = p.AddPresenceStats("a")
replies, err := c.SendPipe(ctx, p)
if err != nil {
	return err
}
for _, r := range replies {
	if r.Error != nil { /* this command failed */ }
	// else json.Unmarshal(r.Result, &...) by position
}
```

In v4 each command returns a typed `Pending`:

```go
b := c.NewBatch(gocent.BatchOptions{})
pub := b.Publish(gocent.PublishRequest{Channel: "a", Data: data})
stats := b.PresenceStats(gocent.PresenceStatsRequest{Channel: "a"})

res, err := b.Send(ctx)
if !res.Replied {
	return err // no command got a reply
}
// Each command's own outcome - also when others failed:
if res, err := pub.Result(); err == nil {
	log.Printf("published at offset %d", res.Offset)
}
if st, err := stats.Result(); err == nil {
	log.Printf("%d clients", st.NumClients)
}
```

Corner cases:

* **An invalid command fails the whole batch before sending.** A publication
  with data which is not valid JSON makes `Send` return an error wrapping
  `gocent.ErrInvalidRequest` and naming the command (`gocent: batch command #1: ...`).
  Nothing is sent, and every `Pending` returns that error.
* **A broadcast in a batch** behaves as `Client.Broadcast`: its `Pending`
  holds every channel's outcome with a `*BroadcastFailedChannelsError`, and a
  broadcast which failed in any channel counts as a failed command in the
  `*BatchFailedCommandsError`.
* **A batch is sent once.** `Send` again returns `gocent.ErrBatchSent`, and
  adding a command to a sent batch panics: to retry, build a new batch.
  `Result` before `Send` returns `gocent.ErrBatchNotSent`.
* **Retry a failed batch** by building a new one, with the same
  `IdempotencyKey`s, from the commands `BatchResult.Failed()` lists with a
  retryable error - every command when the batch failed as a whole.
  Publications which happened are not repeated, even one reported as failed.
  Without a key a publication is repeated.
* **An empty batch** sends nothing, and `Send` returns nil - v3 returned
  `ErrPipeEmpty`.

# v3.4.0

* Add publish options `WithTags`, `WithB64data`, `WithIdempotencyKey`, `WithDelta`, `WithVersion` and `WithVersionEpoch`. See [#23](https://github.com/centrifugal/gocent/pull/23).

# v3.3.0

* `ErrStatusCode` includes the response body. See [#20](https://github.com/centrifugal/gocent/pull/20).

# v3.2.0

* Fix broadcast request bug: JSON payloads were additionally encoded to base64 due to the lack of `json.RawMessage` usage. See [#16](https://github.com/centrifugal/gocent/pull/16).

# v3.1.0

* Add `Client.Subscribe` method to dynamically subscribe user to a channel (using server-side subscriptions).

```
gorelease -base v3.0.0 -version v3.1.0
github.com/centrifugal/gocent/v3
--------------------------------
Compatible changes:
- (*Client).Subscribe: added
- (*Pipe).AddSubscribe: added

v3.1.0 is a valid semantic version for this release.
```

# v3.0.0

HTTP API client for Centrifugo >= v3.0.0

* API address now should be passed explicitly, like `http://localhost:8000/api`. Previously `gocent` could automatically add `/api` for address like `http://localhost:8000` - this behaviour now removed.
* API changed to reflect Centrifugo v3 improvements - see [migration guide](https://centrifugal.dev/docs/getting-started/migration_v3) and [API description](https://centrifugal.dev/docs/server/server_api)

# v2.2.0

* Add `Config.GetAddr` function to dynamically provide API endpoint at the moment of API request
