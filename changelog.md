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
| `c.SetHTTPClient(hc)` | `Config.HTTPClient: hc` |
| `DefaultHTTPClient` variable, with a 1 second timeout | `gocent.DefaultHTTPClient()`, returning a new client with no timeout of its own; `Config.RequestTimeout` (10 seconds by default) bounds each call whose context has no deadline |
| `c.Publish(ctx, ch, data, gocent.WithSkipHistory(true))` | `c.Publish(ctx, gocent.PublishRequest{Channel: ch, Data: data, SkipHistory: true})` |
| `WithTags`, `WithIdempotencyKey`, `WithDelta`, `WithVersion`, `WithVersionEpoch`, `WithB64data` | `Tags`, `IdempotencyKey`, `Delta`, `Version`, `VersionEpoch`, `B64Data` fields |
| `c.Broadcast(ctx, channels, data)`; `res.Responses[i].Error`, `res.Responses[i].Result` | `c.Broadcast(ctx, gocent.BroadcastRequest{Channels: channels, Data: data})`; `res.Channels[i].Err`, `res.Channels[i].Result`, and `res.Channels[i].Channel` |
| `Subscribe`, `Unsubscribe`, `Disconnect`, `History`, `Channels` with options such as `WithLimit`, `WithPattern` | request structs such as `gocent.HistoryRequest{Channel: ch, Limit: 10}`; `Subscribe`, `Unsubscribe` and `Disconnect` now return a result too |
| `data []byte`, `json.RawMessage` in subscribe options | `jsontext.Value` - convert with `jsontext.Value(b)`; it must be valid JSON |
| `p := c.Pipe()`, `err := p.AddPublish(...)`, `replies, err := c.SendPipe(ctx, p)` | `b := c.NewBatch(gocent.BatchOptions{})`, `pub := b.Publish(...)`, `err := b.Send(ctx)`, `res, err := pub.Result()` |
| `p.Reset()` to reuse a pipe | a new `NewBatch`: a batch is sent once |
| `ErrPipeEmpty` | none: sending an empty batch does nothing and returns nil |
| `gocent.Error` value, `err.(gocent.Error)` | `*gocent.Error`; `errors.Is(err, gocent.ErrUnknownChannel)`, or `errors.As` for `Code` |
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
  only checked `err` now sees failures it used to miss. To go on despite some
  failed channels:

  ```go
  res, err := c.Broadcast(ctx, req)
  var be *gocent.BroadcastError
  if err != nil && !errors.As(err, &be) {
  	return err // no channel got a reply
  }
  for _, ch := range res.Channels { ... } // ch.Err is set for a failed channel
  ```
* **Batches are ordered unless asked otherwise.** `BatchOptions{}` runs the
  commands one by one, in the order they were added; `Parallel: true` runs
  them concurrently.
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
