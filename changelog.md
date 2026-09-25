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
* **Broadcast and batch report partial failures.** `Broadcast` returns a
  `*gocent.BroadcastError`, and `Batch.Send` a `*gocent.BatchError`, with the
  complete result whenever one or more parts failed.
* **Typed batches.** `client.NewBatch()` gives a `Batch` with a method per
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

| v3 | v4 |
|---|---|
| `gocent.New(gocent.Config{Addr, Key})` | `gocent.New(gocent.Config{APIEndpoint, APIKey})`, which now returns an error |
| `Config.GetAddr` | `Config.APIEndpointFunc`, which takes a context |
| `client.Publish(ctx, ch, data, gocent.WithSkipHistory(true))` | `client.Publish(ctx, gocent.PublishRequest{Channel: ch, Data: data, SkipHistory: true})` |
| `WithTags`, `WithIdempotencyKey`, `WithDelta`, `WithVersion`, `WithVersionEpoch`, `WithB64data` | `Tags`, `IdempotencyKey`, `Delta`, `Version`, `VersionEpoch`, `B64Data` fields |
| `client.Broadcast(ctx, channels, data)` | `client.Broadcast(ctx, gocent.BroadcastRequest{Channels: channels, Data: data})`; per-channel results in `res.Channels` |
| subscribe, unsubscribe, disconnect and history options | fields of `SubscribeRequest`, `UnsubscribeRequest`, `DisconnectRequest`, `HistoryRequest` |
| `client.Pipe()`, `pipe.AddPublish(...)`, `client.SendPipe(ctx, pipe)` | `b := client.NewBatch()`, `p := b.Publish(...)`, `b.Send(ctx, gocent.BatchOptions{})`, `p.Result()` |
| `ErrStatusCode` | `*gocent.HTTPError` |
| `DefaultHTTPClient` variable | `gocent.DefaultHTTPClient()`, returning a new client |
| `json.RawMessage` data | `jsontext.Value` data |

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
