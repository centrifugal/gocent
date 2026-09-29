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
  `*gocent.BroadcastError` or `*gocent.BatchError` saying which. Every part
  always has an outcome: `res.Channels` holds one entry per requested
  channel, and every batch command its `Pending`.
* **Typed batches.** `client.NewBatch(opts)` gives a `Batch` with a method per
  command, each returning a `Pending` holding that command's typed result.
* **Automatic batching.** `Config.AutoBatch` sends concurrent calls together
  once `MaxInFlight` requests are in flight, with no delay otherwise.
* **Bearer token authentication.** `Config.BearerTokenFunc` sends a token
  with every request as `Authorization: Bearer`, for Centrifugo PRO's JWKS
  authentication of the API.
* **Configuration is validated.** `New` returns an error for a missing or
  malformed `APIEndpoint`, and contradictory settings.
* **Safer defaults.** A default HTTP client with a connection pool, and a
  per-request timeout when the context has none.
* **Async consumers.** `APIMethod()` on every request gives the method name for
  building events from a request.

### Migrating from v3

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
| `WithPresence(true)`, `WithJoinLeave(true)`, `WithPosition(true)`, `WithRecover(true)` | `Override: gocent.SubscribeOptionOverride{Presence: &gocent.BoolValue{Value: true}}`, with the fields `Presence`, `JoinLeave`, `ForcePositioning`, `ForceRecovery` - Centrifugo takes these only as overrides of the channel's options |
| `data []byte`, `json.RawMessage` in subscribe options | `jsontext.Value` - convert with `jsontext.Value(b)`; it must be valid JSON |
| `p := c.Pipe()`, `err := p.AddPublish(...)`, `replies, err := c.SendPipe(ctx, p)` | `b := c.NewBatch(gocent.BatchOptions{})`, `pub := b.Publish(...)`, `err := b.Send(ctx)`, `res, err := pub.Result()` |
| `p.Reset()` to reuse a pipe | a new `NewBatch`: a batch is sent once |
| `ErrPipeEmpty` | none: sending an empty batch does nothing and returns nil |
| `*gocent.Error`, `err.(*gocent.Error)` | unchanged, and `errors.Is(err, gocent.ErrUnknownChannel)` now matches by code |
| `ErrStatusCode{Code, Body}` | `*gocent.HTTPError{StatusCode, Body}`; `errors.Is(err, gocent.ErrUnauthorized)` for a 401 |
| `ErrMalformedResponse` | `*gocent.DecodeError` |

Behaviour to check when migrating:

* **Failed channels and commands are errors now.** In v3, `Broadcast` and
  `SendPipe` returned `err == nil` when single channels or commands failed,
  and the caller had to inspect every reply. In v4 they return an error
  unless everything succeeded - a `*gocent.BroadcastError` or
  `*gocent.BatchError` when Centrifugo carried the request out and some parts
  failed. Code which checked each reply keeps working: `res.Channels` still
  holds every channel's outcome and every `Pending` its command's. Code which
  only checked `err` now sees failures it used to miss. See the examples
  below.
* **Batches are ordered unless asked otherwise.** `BatchOptions{}` runs the
  commands one by one, in the order they were added; `Parallel: true` runs
  them concurrently. Since v6.9.7, Centrifugo PRO sends the publications of
  a batch to its broker together: each channel's publications still take
  effect in order, but publications into different channels may not. A
  namespace with `publication_grouping_disabled` keeps the strict order.
* **Invalid requests are not sent.** A publication or broadcast without a
  channel or data, or with data which is not valid JSON, fails before sending
  with an error wrapping `gocent.ErrInvalidRequest`; in a batch it fails the
  whole batch.
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
var be *gocent.BroadcastError
switch {
case err == nil:
	// Published into every channel.
case errors.As(err, &be):
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

* `errors.Is(err, gocent.ErrUnknownChannel)` is true when any channel failed
  that way, through the `*BroadcastError`.
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

err := b.Send(ctx)
var bErr *gocent.BatchError
if err != nil && !errors.As(err, &bErr) {
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
  `gocent.ErrInvalidRequest` and naming the command (`batch command #1: ...`).
  Nothing is sent, and every `Pending` returns that error.
* **A broadcast in a batch** behaves as `Client.Broadcast`: its `Pending`
  holds every channel's outcome with a `*BroadcastError`, and a broadcast
  which failed in any channel counts as a failed command in the
  `*BatchError`.
* **A batch is sent once.** `Send` again returns `gocent.ErrBatchSent`, and
  adding a command to a sent batch panics: to retry, build a new batch.
  `Result` before `Send` returns `gocent.ErrBatchNotSent`.
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
