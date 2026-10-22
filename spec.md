# Centrifugo server SDK specification

What a Centrifugo server SDK - the library a backend uses to talk to
Centrifugo, as opposed to a client SDK in browsers and apps - provides, and
how it behaves. gocent is its reference implementation: where this document
and gocent disagree, it is a bug in one of them, to be fixed in both.

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119.

Conventions:

- **Names** are given in a neutral form (`api_endpoint`,
  `broadcast_with_retry`); each SDK spells them the way its language does
  (`APIEndpoint` in Go, `api_endpoint` in Python, `apiEndpoint` in
  TypeScript), and keeps the same words.
- **Idioms are the language's.** A "deadline", "cancellation" or "context" is
  the language's own way to bound and cancel a call: `context.Context` in Go;
  an `AbortSignal` and a timeout option in JavaScript; task cancellation and
  timeouts in Python's asyncio, a timeout argument in synchronous Python. An
  "error" is an error value or an exception, whichever the language uses, and
  "matches" is its way to test one (`errors.Is`, `isinstance`, `instanceof`).
  `batch_context` and context values exist only where the language has
  request-scoped context.
- **Numbers are gocent's.** Defaults and limits given here - timeouts, pool
  sizes, body limits, cache lifetimes, retry waits - are gocent's: an SDK
  SHOULD use them, and MAY differ where its platform suggests otherwise,
  saying so in its docs.

## 1. Scope

An SDK covers what a backend does with Centrifugo over HTTP:

| Part | What it is | gocent |
|---|---|---|
| API client | Calls every method of the server HTTP API | package `gocent` |
| Commands | API calls left for Centrifugo's async consumers | `gocent.NewCommand` |
| Tokens | Connection, subscription and server API tokens, JWKS | package `token` |
| Proxy | The backend side of Centrifugo's HTTP proxy | package `proxy` |
| Shared poll | Signatures for shared poll subscriptions | package `sharedpoll` |

Out of scope, on purpose:

- **GRPC** - the GRPC server API and GRPC proxy. Applications using them
  generate code from Centrifugo's protos.
- **Subscription stream proxies** (`SubscribeUnidirectional`,
  `SubscribeBidirectional`): GRPC only.
- **The API `rpc` method**: it calls server API extensions, which neither
  Centrifugo OSS nor PRO provides.
- **Transport code for consumers** - Kafka producers, SQL for outbox tables:
  the application writes a command with its own driver.

An SDK MAY leave out a part its ecosystem has no use for, and SHOULD say so in
its README.

## 2. Sources of truth and code generation

- The API is defined by Centrifugo's `internal/apiproto/api.proto`, the proxy
  by `internal/proxyproto/proxy.proto`. An SDK MUST generate its request and
  result types - and, where its language allows, its API methods - from them,
  and MUST NOT hand-edit generated code. Generated code MUST be checked to be
  current in CI.
- The protos MUST be copied from a released Centrifugo tag before an SDK
  release. A branch MAY be used during development.
- The proto comments become the SDK's documentation of every type, field and
  method.
- Field names on the wire are the protos' field names, exactly: snake_case,
  case-sensitive (Centrifugo v7 stops accepting other spellings).
- A field left at its zero value is not sent. A `BoolValue` field is
  tri-state - absent leaves the server's setting alone, which `false` cannot
  say - and MUST be represented as optional (a pointer, `None`, `undefined`),
  with a helper to make one (gocent: `gocent.Bool(true)`).
- Payload fields (`bytes` in the protos: data, info, meta) are JSON values in
  the API and the proxy: section 3.4 says how an SDK takes and returns them.
- A method whose comment says "Centrifugo PRO only" MUST be marked so in the
  SDK's docs, and known to the client for the 404 hint (section 4.1).
- `broadcast` is written by hand (section 7): its result is per channel.

## 3. The API client

### 3.1 Configuration

A client is created once from a configuration and shared; it MUST be safe for
concurrent use. Creating it MUST validate everything that can be validated
without a request and fail with an `InvalidConfig` error naming the field and
the fix.

| Option | Meaning | Rules |
|---|---|---|
| `api_endpoint` | Base URL of the API, `http://localhost:8000/api` | Absolute `http`/`https` URL with a host; no query, fragment or credentials; a trailing `/` is dropped; a path ending in a method name (`/api/publish`) is refused with a hint |
| `api_endpoint_func` | Returns the base URL per request, for discovered addresses | Exactly one of the two is set; its result is validated per request |
| `api_key` | Sent as `X-API-Key` | No control characters |
| `bearer_token_func` | Returns a token sent as `Authorization: Bearer <token>`, per request | Not together with `api_key`: Centrifugo PRO judges a request with a bearer token by the token alone. An empty token or one with control characters fails the call before sending |
| `http_client` | The HTTP client | Default: section 3.2 |
| `request_timeout` | Bounds a call without a deadline of its own | Default 10 s; negative refused |
| `header` | Extra headers on every request | MUST refuse `X-API-Key`, `Authorization`, `Content-Type`, invalid names and values with control characters; `User-Agent` replaces the SDK's own; `Host` sets the request's host |
| `auto_batch` | Section 8 | |
| `on_request` | Section 10 | |
| `batch_context` | Section 10 | |

No credentials at all is valid - mutual TLS, a proxy, or
`http_api.insecure` - so a missing key shows as `Unauthorized` on the first
call.

### 3.2 HTTP

- Every method is a `POST` of JSON to `{api_endpoint}/{method}`, such as
  `/api/publish`; a batch to `/api/batch`. The legacy `/api` endpoint MUST NOT
  be used.
- Headers: `Content-Type: application/json`, `User-Agent: <sdk>/<major>`
  (gocent: `gocent/v4`), the credentials.
- Redirects MUST NOT be followed: Centrifugo's API never redirects, and
  following one would send the API key wherever it points and turn a `POST`
  into a `GET`. A redirect is an `HTTPError` with a hint to check the endpoint.
  A caller's HTTP client with its own redirect policy is left to it.
- The default HTTP client MUST keep connections alive with a pool sized for
  concurrent use - gocent: 256 connections to a host, all kept while idle, so
  a burst does not open connections only to close them; idle ones closed after
  50 s, before load balancers commonly close theirs. It MUST NOT be the
  language's process-wide default client when that keeps few connections.
- Every response body MUST be closed, and drained up to a bound (gocent:
  64 KiB), so connections are reused. A body of an unexpected status is read
  up to 4 KiB for a Centrifugo error, and 512 bytes of it kept for the
  `HTTPError`.
- A request body MUST NOT be reused or pooled while the transport may still
  read it.
- One timeout story: every request has exactly one deadline - the caller's,
  or `request_timeout` - and the default client sets no timeouts of its own.
  However a request times out, the SDK MUST report it as the language's one
  timeout error (gocent: `errors.Is(err, context.DeadlineExceeded)`).
- Replies are decoded as strictly as requests are encoded: case-sensitive
  names. A reply which is not a Centrifugo reply is a `DecodeError`.

### 3.3 Methods

Every API method in `api.proto` but `rpc` MUST be a client method taking its
request and returning its result, plus a batch method (section 7). The request
type MUST tell its API method name (gocent: `APIMethod()`; Python:
`api_method`).

### 3.4 Payloads

Payloads - `data`, `info`, `meta` and the like, in requests, replies, token
claims and the proxy - are JSON values: an object, array, string, number,
boolean or null. Every SDK takes and returns them the same way:

1. **A payload takes the language's native value, encoded by the SDK**:
   `data={"text": "hi"}` in Python, `data: {text: "hi"}` in JavaScript,
   `$data = ['text' => 'hi']` in PHP. This is the usual way to publish.
2. **Every SDK also takes raw JSON, marked by a type of its own,** and sends
   it verbatim - not decoded and encoded again: Python
   `RawJSON(b'{"text":"hi"}')`, JavaScript `JSON.rawJSON('{"text":"hi"}')`,
   Go `jsontext.Value` - the table below names each. It is for JSON the
   application already has - from a database, a queue, a cache - and for
   encoding with a library or rules of the application's own (a faster
   encoder, dates, decimals): encode, then pass the result as raw JSON. An
   SDK therefore has no option to plug in a JSON encoder.
3. **A string is a JSON string**, never raw JSON: `data="hello"` publishes
   `"hello"`. Only the raw JSON type marks raw JSON.
4. **Binary data goes in the `b64` fields only** (`b64data`, `b64info`),
   base64-encoded. Bytes given as a JSON payload are refused, not guessed at -
   unless bytes are the language's raw JSON type, as in Go. An SDK MAY take
   bytes for a `b64` field and encode them itself.
5. **Invalid JSON is never sent**: raw JSON is checked to be valid, and a
   native value the encoder cannot represent fails with `InvalidRequest`
   before sending - NaN and infinities included, which some encoders write
   anyway, as `NaN` or as `null`, though JSON has no such numbers.
6. **Payloads come back as the language is used to**: as the type the table
   names for replies - the decoded native value in Python, JavaScript, PHP,
   Ruby, Dart and Elixir (`pub.data["text"]`); the language's JSON type in the
   others, for the application to decode into its own types. The same holds
   for what the SDK receives in the proxy.
7. **Where one field cannot take both a native value and raw JSON** - Go,
   Rust, Swift - the SDK takes raw JSON only, and encoding a value is one call
   of the JSON library the table names.

| Language | JSON library | Native value in requests | Raw JSON | Payloads in replies and proxy requests | To mind |
|---|---|---|---|---|---|
| Go | `encoding/json/v2` | - (rule 7: `json.Marshal(v)`) | `jsontext.Value` | `jsontext.Value` | |
| Python | `json` | `Any` JSON-compatible: `dict`, `list`, `str`, `int`, `float`, `bool`, `None` | `RawJSON` of the SDK, holding `bytes` or `str` | decoded `Any` | encode with `allow_nan=False`: by default `json.dumps` writes `NaN` |
| JavaScript, TypeScript | `JSON` | `unknown` JSON-compatible: objects, arrays, `string`, `number`, `boolean`, `null` | `JSON.rawJSON(text)` (Node.js 21+, Chrome 114+); elsewhere the SDK's `rawJSON(text)` | decoded `unknown` | `JSON.stringify` writes NaN and infinities as `null` and drops `undefined`: refuse non-finite numbers; integers past 2^53 lose precision when decoded - say so in the docs |
| PHP | `ext-json` | `mixed`: arrays, scalars, `null`, `stdClass`, `JsonSerializable` | `RawJson` of the SDK, holding a `string` | `json_decode($json, true)`: associative arrays | `JSON_THROW_ON_ERROR`; `JSON_PRESERVE_ZERO_FRACTION`, so `1.0` stays a float; `[]` encodes as an array - an empty object is `new \stdClass()` |
| Ruby | `json` | `Hash`, `Array`, `String`, `Integer`, `Float`, `true`, `false`, `nil` | `JSON::Fragment` (json 2.10+) | `JSON.parse`: `Hash` with `String` keys | |
| Java | Jackson | `Object`: POJO, `Map`, `List`, `JsonNode` | `com.fasterxml.jackson.databind.util.RawValue` | `JsonNode` | refuse non-finite numbers |
| Kotlin | kotlinx.serialization | `JsonElement` (`buildJsonObject`, `Json.encodeToJsonElement`) | `JsonUnquotedLiteral(text)` | `JsonElement` | |
| C# | System.Text.Json | `object?`: POCO, anonymous type, `JsonNode` | `RawJson` of the SDK, written with `Utf8JsonWriter.WriteRawValue` | `JsonElement` | |
| Rust | serde_json | - (rule 7: `serde_json::value::to_raw_value(&v)`) | `Box<serde_json::value::RawValue>` (feature `raw_value`) | `Box<RawValue>` | serde_json writes NaN and infinities as `null`: refuse non-finite numbers |
| Swift | Foundation `Codable` | - (rule 7: `JSONEncoder().encode(v)`) | `Data` | `Data` | |
| Dart | `dart:convert` | `Object?`: `Map<String, dynamic>`, `List`, `num`, `String`, `bool`, `null` | `RawJson` of the SDK, holding a `String` | decoded `Object?` | |
| Elixir | Jason | maps, lists, strings, numbers, booleans, `nil` | `Jason.Fragment.new(iodata)` | decoded terms, maps with string keys | |

A language not listed follows the closest row: its standard or dominant JSON
library, native values where a field can take them, its library's raw JSON
type, or the SDK's own when there is none.

### 3.5 Validation before sending

A request the client can tell is wrong MUST NOT be sent, and fails with an
`InvalidRequest` error naming the method and problem:

- a request which cannot be encoded: a payload which is not valid JSON, a
  string which is not valid UTF-8;
- `publish`: no `channel`; neither `data` nor `b64data`;
- `broadcast`: no `channels`; an empty channel name (naming its index);
  neither `data` nor `b64data`.

Every other rule stays Centrifugo's to check: the SDK MUST NOT copy them.

### 3.6 Shutdown

`close(deadline)` MUST refuse new calls with a `Closed` error and wait for the
calls in progress - requests on their way, calls queued for an automatic
batch, batches whose callers left, a broadcast retry (which stops at its next
attempt). It returns the deadline's error if that ends first, leaving the rest
to go on, and MAY be called again. With its own HTTP client the SDK then
closes idle connections.

## 4. Errors

### 4.1 Kinds

| Kind | Meaning | Contents |
|---|---|---|
| `Error` | Centrifugo refused the request or failed to carry it out | `code` (stable), `message` (for people, not compared), `temporary` |
| `HTTPError` | The request did not reach the API method | `status_code`, `body` (start of it), `header` (always present: request IDs, `Retry-After`) |
| `DecodeError` | The reply is not Centrifugo's - usually a wrong endpoint | the cause |
| `InvalidRequest` | Not sent: sections 3.4 and 3.5 | method, problem, cause |
| `InvalidConfig` | Client not created: section 3.1 | field, fix |
| `Closed` | Call after `close` | |
| `BatchSent` / `BatchNotSent` | Batch misuse: section 7.2 | |
| network / timeout | From the transport, wrapped | the cause |

- An `Error` MUST be matched against known codes by code alone, never by
  message: `errors.Is(err, ErrUnknownChannel)`, `err.code == 102`.
- Known codes: 100 internal (temporary), 102 unknown channel, 104 not found,
  107 bad request, 108 not available, 111 too many requests (temporary), 112
  unrecoverable position, 113 conflict. Centrifugo adds codes: nothing may
  switch exhaustively over them, and an unknown code is still an `Error`.
- An `HTTPError` with status 401 MUST match `Unauthorized`.
- Error messages SHOULD carry hints: a redirect - check http or https in the
  endpoint; a 404 for a PRO-only method - the server is likely Centrifugo OSS;
  any other 404 - is the endpoint the API base URL; a 401 - check the
  credentials. In a batch, a PRO-only command sent to Centrifugo OSS fails
  with code 104 (not found) instead of a 404.

### 4.2 Transport error mode

With `http_api.error_mode: transport`, Centrifugo answers a failed call with
an HTTP status and the error alone as the body. The SDK MUST return the same
`Error` as in a 200 reply when the status is one Centrifugo maps errors to -
400, 404, 409, 416, 429, 500 - and the body is `{"code": N, ...}` with N ≥ 100.
A body whose code is itself an HTTP error status (400-599, `{"code": 429}`
from a rate-limiting gateway) is a gateway's: it stays an `HTTPError`.

### 4.3 The core rule

A call returns an error unless it did everything asked:
`if err != nil { return err }` - or a raised exception - MUST always be correct
code. Broadcast and batch apply it too (section 7).

### 4.4 Retryable

`retryable(err)` reports whether retrying may succeed: the error came from a
temporary condition, not from the request.

| Retryable | Not retryable |
|---|---|
| `Error` with `temporary`, or code 100 (servers before `temporary`) | Any other `Error`: unknown channel, bad request, ... |
| Network failures: refused or reset connections | `InvalidRequest`, `InvalidConfig`, `Closed` |
| Timeouts, the caller's own deadline included | A cancelled call |
| `HTTPError` 429 and 5xx | Other `HTTPError`s, 401 included |
| A broadcast or batch partial error with any retryable part | `DecodeError`, TLS certificate errors |

Errors from `api_endpoint_func` and `bearer_token_func` are judged by the same
rules. Docs MUST say that a retryable error does not mean nothing was done: a
publication MUST be retried only with the same `idempotency_key`.

## 5. Idempotency

The SDK's docs MUST explain, wherever retries are:

- each publication or broadcast gets its own key, reused only to retry that
  same one; a broadcast needs one key, not one per channel;
- Centrifugo remembers a key per channel, five minutes by default: a second
  publication under the same key is not published, so a key derived from an
  entity must also name the event (`order-42-paid`, not `order-42`);
- the memory, Redis and PostgreSQL brokers keep keys; the NATS broker ignores
  them, and a retry publishes again.

## 6. Retry helpers

- `exponential_backoff(base, limit)` returns `wait(attempt)`: `base` after the
  first failure, doubling, capped at `limit`, each wait a uniformly random time
  in `[d/2, d]`. A negative base counts as 0, a limit below base as base, an
  attempt below 1 as 1; doubling MUST NOT overflow.
- `wait(deadline, d)` waits for `d` or until the call's deadline or
  cancellation, and returns that error - never a sleep the deadline cannot
  end.

## 7. Broadcast and batch

### 7.1 Broadcast

`broadcast(req)` returns a result with one entry per requested channel, in
order - `channel`, `result` (the publication) or `err`, `attempts` - whatever
the error:

- nil error: every channel got the publication;
- `BroadcastFailedChannelsError`: Centrifugo carried it out and one or more
  channels failed, up to all - with `failed` and `total`;
- any other error: it failed as a whole, no channel got a reply, and every
  entry carries that error.

`result.replied` is true exactly when the error is nil or a
`BroadcastFailedChannelsError`; `result.failed()` lists the failed channels.
They MUST never disagree with the error. A partial error MUST NOT match its
parts' errors: one unknown channel among many does not make the broadcast
match `UnknownChannel`. `retryable_channels()` returns the channels a retry
should go to.

### 7.2 Batch

- `new_batch(options)` - options fixed at creation, since they change what the
  commands mean together. `parallel: false` (the default) runs commands in
  order; `parallel: true` concurrently. Docs MUST note that Centrifugo PRO
  sends a sequential batch's publications to its broker together: each
  channel's take effect in order, publications into different channels may
  not, unless the namespace sets `publication_grouping_disabled`.
- One method per API method adds a command and returns a pending handle
  (gocent: `Pending[T]`) holding that command's typed outcome once sent;
  reading it before returns `BatchNotSent`.
- `send()`: nil when every command succeeded; `BatchFailedCommandsError`
  (`failed` with index, method, error; `total`) when some failed, each pending
  holding its own outcome; any other error when the batch failed as a whole,
  every pending returning that error. `replied` and `failed()` as for
  broadcast. A broadcast in a batch returns exactly what a direct broadcast
  returns.
- A command the client can tell is invalid fails the whole batch before
  sending, naming its index.
- A batch is sent once, whatever happened: sending again returns `BatchSent`.
  An empty batch does nothing and succeeds.
- A reply count different from the command count is a `DecodeError`.

### 7.3 Broadcast with retry

`broadcast_with_retry(req, options)`:

- sends only the channels which may still succeed: a channel which failed for
  good is not retried, and when only those are left it stops;
- every attempt carries the same `idempotency_key`; without one, the SDK
  makes a random one for the call (gocent: `gocent-` + 26 random base32
  characters) and returns it in the result;
- the result holds every channel's last outcome and how many attempts each
  took;
- `max_attempts` - default 2, one retry; `attempt_timeout` bounds each
  attempt while the call's deadline bounds all, waits included;
- `on_attempt_err(attempt)` is called after each failed attempt a retry may
  fix, except the last, with the attempt number, its error, the channels to
  retry, the result so far and the key. It waits, logs or stops by returning
  an error; the call then returns both errors joined - or the broadcast's
  alone when it already holds the other, a deadline say. Default: wait
  `exponential_backoff(100 ms, 5 s)(attempt)`;
- a failure no retry can fix - a 401, an invalid request - stops it without
  calling `on_attempt_err`.

## 8. Automatic batching

`auto_batch` sends concurrent calls together, cutting the requests Centrifugo
handles under load. An SDK whose language has no concurrent calls MAY leave it
out.

- `max_in_flight` (0 disables): below it, every call is sent at once, alone.
  There are no timers: nothing ever waits for a batch to fill.
- At the cap, calls queue first in, first out, and when a request finishes
  all those queued go out as one batch, at most `max_batch_size` (default
  1000). Requests in flight never exceed `max_in_flight`. A larger
  `max_in_flight` batches less; docs SHOULD suggest about 8.
- Refused at creation: negative values; `max_batch_size` without
  `max_in_flight`; `max_batch_size` of 1.
- An automatic batch always goes to `/api/batch`, even with one command, so
  that replies have one shape.
- Every call gets exactly its own reply: its command's result or error, or the
  error of the batch request which carried it. A batch request failing as a
  whole fails every call in it; no call is retried alone.
- A call's command is encoded before it queues; once a call returns nothing
  reads its request again.
- A call whose deadline ends while queued leaves without its command being
  sent. A batch runs on its own, never on a caller's context, until the latest
  deadline of its calls (`request_timeout` for a call without one), so each
  call keeps its own deadline. A reply arriving as a caller's deadline ends is
  returned, not a timeout.
- Calls made one after another keep their order: each waits for its reply.
- Callers' context values reach a batch only through `batch_context`.

## 9. Commands for async consumers

`new_command(req)` returns `{method, payload}`: the API method and the request
as JSON. It MUST validate the request as a call would (section 3.5), so that a
command Centrifugo could not run fails when written rather than in a consumer
which drops it. Encoded as JSON it is the `{"method": ..., "payload": ...}`
message consumers take whole (Kafka, NATS JetStream). Docs MUST say that a
consumer runs commands in order only within a partition, and that a channel's
commands keep their order only in one partition, derived from the channel the
same way by every writer.

## 10. Observability

- `on_request(context, info)` is called after every HTTP request with:
  `method` ("batch" for batches), `commands`, `failed` (commands answered with
  an error; a broadcast failing in some channels is not counted), `auto_batch`,
  `duration` (from before getting the endpoint and credentials to the reply
  read; MAY be zero on a coarse clock), `status_code` (0 without a reply),
  `err` (the request's own error, nil when Centrifugo's reply was read). A
  broadcast retry reports each attempt, an automatic batch once.
- `batch_context(batch_context, callers)` makes an automatic batch's request
  context from its callers' - for a tracing span linking every caller's span.
- Tracing and metrics otherwise come from the HTTP client's transport; the SDK
  MUST NOT depend on a tracing library. Docs SHOULD show the language's
  OpenTelemetry HTTP instrumentation.

## 11. Tokens

### 11.1 Keys

| Key | Algorithm | Centrifugo option |
|---|---|---|
| HMAC secret | HS256 | `client.token.hmac_secret_key` |
| RSA private key | RS256 | `client.token.rsa_public_key` |
| ECDSA private key | ES256 / ES384 / ES512 for P-256 / P-384 / P-521; other curves refused | `client.token.ecdsa_public_key` |
| Ed25519 private key | EdDSA | `client.token.ed25519_public_key` (Centrifugo releases after #1288), or JWKS |

A key MAY carry a key ID, written as the token header's `kid`. A JWT is
`base64url(header).base64url(claims).base64url(signature)`, unpadded, header
`{"alg": ..., "typ": "JWT", "kid": ...}` (`kid` only when set). ECDSA
signatures are `r || s`, each padded to the curve's size. Payloads in the
claims - `info`, `data`, `meta` - follow section 3.4; one which is not valid
JSON fails at signing.

### 11.2 Connection and subscription claims

| SDK field | Claim | Notes |
|---|---|---|
| `user` | `sub` | Empty: anonymous |
| `expires_at` | `exp` | Required, unless `no_expiry` |
| `no_expiry` | `exp` left out | Explicit: the token is accepted forever |
| `connection_expires_at` / `subscription_expires_at` | `expire_at` | When the connection or subscription expires, instead of at `exp` |
| `connection_no_expiry` / `subscription_no_expiry` | `expire_at: 0` | Never expires; `exp` still bounds the token |
| `issued_at` | `iat` | Default now |
| `id` | `jti` | For PRO token revocation |
| `audience`, `issuer` | `aud`, `iss` | |
| `info` / `b64info` | `info` / `b64info` | |
| `channels` / `subs` (connection) | `channels` / `subs` | Not both: Centrifugo ignores `channels` when `subs` is set |
| `meta` (connection) | `meta` | Never sent to the client |
| `caps`, `labels` (connection, PRO) | `caps`, `labels` | |
| `channel` (subscription) | `channel` | Required |
| `data` / `b64data`, `override`, `allow`, `server_tags_filter` (subscription) | same names | `allow` and `server_tags_filter`: PRO |

Signing MUST refuse claims without an expiry - neither `expires_at` nor
`no_expiry` - and contradictions: both of them, or both of a pair of
`*_expires_at` and `*_no_expiry`. Claims are written with the exact names
above.

### 11.3 JWKS

`jwks(keys)` returns the public keys as a JSON Web Key Set, for
`client.token.jwks_public_endpoint` and PRO's `http_api.jwks.endpoint`:
`{"keys": [...]}`, each key with `kty` (`RSA`, `EC`, `OKP`), `kid`, `use:
"sig"` (Centrifugo skips keys without it), `alg`, and `n`/`e`, `crv`/`x`/`y`
or `crv: "Ed25519"`/`x`, base64url unpadded, EC coordinates at the curve's
size. It MUST refuse: no keys, an HMAC key, a key without an ID, an ID used
twice.

A JWKS endpoint answers `GET` and `HEAD` with `Content-Type:
application/json` and `Cache-Control: public, max-age=300`.

Docs MUST explain rotation: publish a new key next to the old one; sign with
it only once every instance of the application publishes it - from an
activation time set ahead, or in a second deploy; drop the old key once its
tokens expired; never give a key ID to another key, since Centrifugo caches
keys by ID. An example SHOULD show rotation by activation time (gocent:
`_examples/jwks-rotation`).

### 11.4 Server API tokens (Centrifugo PRO)

`api_token(key, claims)` signs a token for PRO's `http_api.jwks`: `aud`,
`scope` (scopes joined by spaces; none empty or with a space), `iss`, `sub`
(who calls, for logs), `exp`, `iat`, `jti`. It MUST refuse: an HMAC key or
one without an ID (PRO verifies through JWKS by `kid`), claims with neither
audience nor scope, a missing expiry.

`api_tokens` is a token source for `bearer_token_func`: it signs with a key
(or the key a function returns, for rotation), reuses each token for half its
TTL (default 5 minutes), and is safe for concurrent use. It refuses an expiry
or issue time set in its claims, and a negative TTL.

Docs MUST say: PRO accepts a token signed with any key of its JWKS endpoint,
and checks audience, issuer and scope only when configured to - so API tokens
MUST be signed with keys of their own, published at a JWKS endpoint other
than the client tokens' one, and PRO configured to require an audience or
scope only API tokens carry. Otherwise any user can call the server API with
their connection token.

## 12. Endpoints for client SDKs

Endpoints the `getToken` and `getSignature` callbacks of client SDKs call,
around an application function deciding who gets what:

- Success: a JSON object, `Content-Type: application/json`, `Cache-Control:
  no-store`.
- The application's `Unauthorized` error (also wrapped): 403 - client SDKs
  stop asking, and disconnect or unsubscribe.
- Any other error: 500, reported to `on_error` - client SDKs retry.
- A malformed request: 400, reported to `on_error`; a wrong method: 405 with
  `Allow`. Bodies are read up to 1 MiB.

| Endpoint | Request | Answer |
|---|---|---|
| Connection token | `GET` or `POST`, body ignored | `{"token": "..."}` |
| Subscription token | `POST {"channel": ...}`, or `GET ?channel=...`; a channel is required | `{"token": "..."}` |
| Shared poll signature | `POST {"channel": ..., "keys": [...]}`; a channel is required | `{"signature": ..., "keys": [...]}` |

For the shared poll signature, keys the function leaves out are revoked: the
client stops tracking them. A key the client did not ask for fails the request
(500). `keys` is always an array, empty included.

## 13. Shared poll signatures

`shared_poll_sign(secret, user, channel, keys, expires_at)` returns
`"{iat}:{exp}:{mac}"`:

- `iat` is now and `exp` is `expires_at`, in Unix seconds; `exp` is `0` for a
  signature which does not expire;
- `keys_hash = hex(sha256(keys joined by "\x00"))`;
- `mac = hex(hmac_sha256(secret, iat "\x00" exp "\x00" user "\x00" channel
  "\x00" keys_hash))`, NUL-separated so that a colon in a user or channel
  cannot move between fields.

The signature covers exactly `keys`, in order. `secret` is Centrifugo's
`shared_poll.hmac_secret_key`; `user` is empty for an anonymous connection.

## 14. Proxy handlers

One handler per proxy type, built from an application function taking the
decoded request (and the HTTP request: headers Centrifugo passed on, a
deadline ending with the proxy's timeout) and returning a result or an error.

| Proxy | Function may answer |
|---|---|
| connect, subscribe, publish, rpc, map publish, map remove | result, `ProxyError`, `Disconnect` |
| shared poll refresh, channel state (PRO) | result, `ProxyError` (a failure) |
| refresh, sub refresh, cache empty (PRO) | result only (refresh and sub refresh end through the result's `expired`) |

- A handler takes `POST` only (405 with `Allow` otherwise), reads up to 16 MiB,
  and answers a malformed request with 400.
- It answers with exactly one of result, error and disconnect: Centrifugo
  takes a response with none as accepting the request as sent.
- `ProxyError{code, message, temporary}` - codes in [400, 1999] - rejects the
  request; `Disconnect{code, reason}` - codes in [4000, 4999], [4500, 4999]
  for no reconnect - closes the connection. Wrapped ones count.
- An answer the proxy type cannot carry, any other error, or a result which
  cannot be encoded fails the request with 500, reported to `on_error`:
  Centrifugo answers the client with a temporary internal error, and for
  refresh and sub refresh asks again a minute later.
- Payloads follow section 3.4: a request's arrive as the language is used
  to, and a result takes a native value or raw JSON.
- Binary mode (`binary_encoding`): payloads arrive in the `b64` fields and
  `encoding` is `"binary"`; a result MAY answer with either form.
- Docs MUST say to keep proxy endpoints reachable by Centrifugo only: anyone
  else could claim any user.

## 15. Testing an SDK

An SDK MUST have:

- Unit tests against a fake server for the transport and error model: every
  error kind, the transport error mode, redirects, hints, the timeout story,
  `replied` agreeing with the error for every way a broadcast or batch is sent,
  and every API method reaching its endpoint and batch command.
- Payloads (section 3.4): a native value and raw JSON send the same value;
  raw JSON is sent verbatim; a string is a JSON string; bytes, NaN and invalid
  raw JSON are refused before sending; replies return payloads as the
  language is used to.
- Automatic batching under random concurrency, with a race detector where the
  language has one.
- Integration tests of every OSS method against the `centrifugo/centrifugo`
  image, and of every PRO method against `centrifugo/centrifugo-pro` in
  sandbox mode (no license key), with the setup in gocent's `testdata/`:
  tokens connecting a real client, proxy handlers called by Centrifugo,
  commands run by a consumer, API tokens accepted - and refused without the
  audience or scope, as connection tokens, and from unpublished keys.
- The vectors below.

### 15.1 Vectors

Shared poll signatures, `secret = "shared_poll_secret"`, `iat = 1700000000`:

| user | channel | keys | exp | signature |
|---|---|---|---|---|
| `42` | `poll:prices` | `AAPL`, `GOOG` | 1700003600 | `1700000000:1700003600:c61dbf52085a6973537e71644fa30a3bbfc29a52d7ce9f1b7618531596ce587d` |
| (empty) | `poll:prices` | (none) | none | `1700000000:0:92dd64af5612afde06cad03bb63435d951e037cb55a087af05c2a1399abade0e` |

HS256 tokens, `secret = "client_secret"`, `iat = 1700000000`, `exp =
1700003600`, `sub = "42"`. A token MUST decode to these claims and verify. It
matches byte for byte when its JSON is written compactly, the header as
`alg, typ, kid` and the claims, those present, in gocent's order:

- connection: `sub, exp, iat, jti, aud, iss, expire_at, info, b64info,
  channels, subs, meta, caps, labels`;
- subscription: `sub, exp, iat, jti, aud, iss, channel, expire_at, info,
  b64info, data, b64data, override, allow, server_tags_filter`.

Byte equality is a convenience for tests, not a requirement: Centrifugo reads
claims in any order.

- connection: `eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiI0MiIsImV4cCI6MTcwMDAwMzYwMCwiaWF0IjoxNzAwMDAwMDAwfQ.HKZwmcYmHVbRESuYd4swVgAH7gg41giR57F_K5FEmEY`
- subscription to `news`: `eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiI0MiIsImV4cCI6MTcwMDAwMzYwMCwiaWF0IjoxNzAwMDAwMDAwLCJjaGFubmVsIjoibmV3cyJ9.v_Quw-azNV1qFjm9ZQnkp6QBtK6LmtFxY74YcUHlJcA`

## 16. Versions

- An SDK's major version follows Centrifugo's major API: gocent v4 is for
  Centrifugo v6. The supported Centrifugo versions are stated in the README.
- A field removed from the protos is a breaking change in typed languages:
  keep it as a deprecated field Centrifugo ignores until the SDK's next major.
- Centrifugo v7 plans to accept only exact, case-sensitive JSON names in the
  API, proxy and tokens: an SDK following section 2 is ready for it.
