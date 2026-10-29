# Centrifugo server SDK specification

What a Centrifugo server SDK - the library a backend uses to talk to
Centrifugo, as opposed to a client SDK in browsers and apps - provides, and
how it behaves. gocent is its reference implementation: where this document
and gocent disagree, it is a bug in one of them, to be fixed in both.

**Version 0.2 (draft)**, 2026-10-28. Changes: section 18.

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119. Each
section states its rules with the reasons for them, so that a case the spec
does not list can be decided the same way. Section 15.2 lists every rule to
check an SDK against; section 17 lists gocent's values.

The spec has a version of its own, which an SDK's README names ("implements
spec 1.2"). From 1.0, released with gocent v4:

- a major version makes an existing rule stricter, so that an SDK following
  the previous one may no longer conform;
- a minor version adds rules for something new (a new proxy type, a new
  method), or new SHOULDs: an SDK following the previous one still conforms
  to everything it implements;
- wording which changes no rule is no new version.

Before 1.0, every version which changes a rule is a minor one.

Conventions:

- **Names** are given in a neutral form (`api_endpoint`,
  `broadcast_with_retry`); each SDK spells them the way its language does
  (`APIEndpoint` in Go, `api_endpoint` in Python, `apiEndpoint` in
  TypeScript), and keeps the same words.
- **Idioms are the language's.** A "deadline", "cancellation" or "context" is
  the language's own way to bound and cancel a call: `context.Context` in Go;
  an `AbortSignal` and a timeout option in JavaScript; in Python, which has no
  deadline object, a per-call timeout argument - in async code too, since it
  cannot read the deadline of an enclosing `asyncio.timeout`, which still ends
  a call sooner, as cancelling its task does. An
  "error" is an error value or an exception, whichever the language uses, and
  "matches" is its way to test one (`errors.Is`, `isinstance`, `instanceof`).
  `batch_context` and context values exist only where the language has
  request-scoped context: Go's `context.Context`, Python's `contextvars`.
- **Unified meaning, idiomatic spelling.** What a call means is the same in
  every SDK: the methods and the words of their fields, results, error kinds
  and codes, the behaviour this document sets, tokens, handlers and vectors.
  How a call is written is the language's: Go passes a request struct, Python
  keyword arguments, PHP named arguments, JavaScript a plain object, each the
  way that language's own SDKs take parameters. Almost every user and agent
  sees one SDK only, and writes it the way its language writes calls; what
  carries between languages is the meaning, which the docs explain once.
- **Blocking and async alike.** Where a language has both blocking and async
  I/O in common use - Python, Rust - an SDK SHOULD provide both clients, with
  the same methods, options, errors and behaviour: an application uses the
  one its framework runs on. The parts which do no I/O - tokens, commands,
  shared poll signatures, the core of the HTTP handlers - are shared.
- **Numbers are gocent's.** Defaults and limits - timeouts, pool sizes, body
  limits, cache lifetimes, retry waits - are gocent's, listed in section 17:
  an SDK SHOULD use them, and MAY differ where its platform suggests
  otherwise, saying so in its docs.
- **HTTP handlers are framework-free.** The JWKS endpoint (section 11.3), the
  endpoints for client SDKs (section 12) and the proxy handlers (section 14)
  are given in HTTP terms: statuses, headers, body limits. An SDK SHOULD
  implement each as one function, `(method, headers, body) -> (status,
  headers, body)`, holding all of that behaviour and tested once, with thin
  adapters for its language's frameworks: gocent's are `http.Handler`s;
  Python's would be WSGI and ASGI, JavaScript's a Fetch `Request -> Response`
  and Express middleware. An SDK MUST NOT require one framework.

## 1. Scope

An SDK covers what a backend does with Centrifugo over HTTP:

| Part | What it is | gocent |
|---|---|---|
| API client | Calls every method of the server HTTP API | package `gocent` |
| Commands | API calls written for Centrifugo's async consumers | `gocent.NewCommand` |
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
  result types from them, and its API methods where its language allows. It
  MUST NOT hand-edit generated code. Generated code MUST be checked to be
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
| `api_key` | Sent as `X-API-Key` | No control characters but tab |
| `bearer_token_func` | Returns a token sent as `Authorization: Bearer <token>`, per request | Not together with `api_key`: Centrifugo PRO judges a request with a bearer token by the token alone. An empty token or one with control characters fails the call before sending |
| `http_client` | The HTTP client | Default: section 3.2. The SDK MUST NOT change a client the caller passed - its headers, pool or redirect policy: credentials and headers go on each request |
| `request_timeout` | Bounds a call without a deadline of its own | A default (section 17); negative refused |
| `header` | Extra headers on every request | MUST refuse `X-API-Key`, `Authorization`, `Content-Type`, invalid names and values with control characters but tab; `User-Agent` replaces the SDK's own; `Host`, with exactly one non-empty value, sets the request's host |
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
  concurrent use. It keeps every connection while idle, so that a burst does
  not open connections only to close them, and closes idle ones before load
  balancers commonly close theirs (values: section 17). It MUST NOT be the
  language's process-wide default client when that keeps few connections.
- Every response body MUST be closed, and drained up to a bound, so
  connections are reused. A body of an unexpected status is read up to a
  bound for a Centrifugo error, and the start of it kept for the `HTTPError`
  (values: section 17).
- A request body MUST NOT be reused or pooled while the transport may still
  read it.
- One timeout story: every request has exactly one deadline - the caller's, or
  `request_timeout` - and the default client sets no timeouts of its own.
  How it is enforced is the language's: a deadline or cancellation where the
  platform has one (Go's `context`, JavaScript's `AbortSignal`, Python's
  `asyncio.timeout`); where the HTTP library's timeouts bound each operation
  rather than the request - a connect, each read, as Python's `httpx` and
  Ruby's `Net::HTTP` do - each request gets the time left as its timeout, and
  the SDK checks the deadline between the chunks of the reply's body. An SDK
  SHOULD use its HTTP library's public API only: what it cannot bound so - a
  reply's headers trickling in - its docs MUST say may outlast the timeout.
  Whichever way a request times out, the SDK MUST report it as the language's
  one timeout error (gocent: `errors.Is(err, context.DeadlineExceeded)`).
- Replies are decoded as strictly as requests are encoded: case-sensitive
  names. A reply which is not JSON of the reply's shape - an HTML page, say -
  is a `DecodeError`.

### 3.3 Methods

Every API method in `api.proto` but `rpc` MUST be a client method taking its
request and returning its result, and the same method on a batch (section 7)
and for commands (section 9): one method per API method in all three, so that
the method a call goes to is its name, and each is typed by its own request
and result.

A method takes its request in the language's idiom (conventions): a struct
in Go (`client.Publish(ctx, gocent.PublishRequest{...})`), keyword arguments
in Python (`client.publish(channel="news", data=d)`), named arguments in PHP
(`$client->publish(channel: 'news', data: $d)`), a plain object in
JavaScript (`client.publish({ channel: 'news', data })`). Keyword and named
arguments are keyword-only where the language can say so, so that a field
the protos add cannot shift a call. A nested message is the language's type
for it - a dataclass, a class, an object. Where the request is a plain object,
a field the SDK does not know MUST be refused with `InvalidRequest` naming
it, rather than dropped: a typo or a field spelled as on the wire
(`skip_history`) would otherwise change the request without a word. Where a
request is one value - a Go struct - its type tells its API method (gocent:
`APIMethod()`).

### 3.4 Payloads

Payloads - `data`, `info`, `meta` and the like, in requests, replies, token
claims and the proxy - are JSON values: an object, array, string, number,
boolean or null.

The rule: an SDK carries a payload exactly. Whatever comes out of it can go
back in unchanged, as the same JSON value, and taking a payload in or giving
it out costs the application as little as its language allows. Where the
choices pull apart, they are settled in this order:

- **Exact** - a payload is never changed on the way: not `{}` into `[]`,
  not an integer past 2^53, not `1.0` into `1`, not NaN into `null`. (Where
  the language's numbers are doubles, integers past 2^53 are the exception:
  the note below the table.)
- **Stable** - the SDK's API holds no type tying applications to one major
  version of a dependency.
- **Idiomatic** - the types are those the language's developers already
  use; a type of the SDK's own only where the language has none.
- **Convenient** - the least code at the call site.

The rules below, and the table, follow from it; a language the table does
not list is decided the same way:

1. **A payload takes the language's native value, encoded by the SDK** -
   except in the languages of rule 7: `data={"text": "hi"}` in Python,
   `data: {text: "hi"}` in JavaScript, `$data = ['text' => 'hi']` in PHP.
   This is the usual way to publish.
2. **Every SDK also takes raw JSON, marked by a type of its own,** and sends
   it as the same JSON value, number text kept, never decoding it into the
   language's values to encode it again. The table below names each type:
   Python `RawJSON(b'{"text":"hi"}')`, JavaScript `rawJSON('{"text":"hi"}')`,
   Go `jsontext.Value`. An encoder MAY drop insignificant whitespace and
   normalize string escapes in it, as Go's does. Raw JSON is for JSON the
   application already has (from a database, a queue, a cache), and for
   encoding with a library or rules of the application's own (a faster
   encoder, dates, decimals): encode, then pass the result as raw JSON. An
   SDK therefore has no option to plug in a JSON encoder.
3. **A string is a JSON string**, never raw JSON: `data="hello"` publishes
   `"hello"`. Only the raw JSON type marks raw JSON.
4. **Binary data goes in the `b64` fields only** (`b64data`, `b64info`),
   base64-encoded. Bytes given as a JSON payload are refused, not guessed at,
   unless bytes are the language's raw JSON type, as in Go. An SDK MAY take
   bytes for a `b64` field and encode them itself.
5. **Invalid JSON is never sent**: raw JSON is checked to be valid, and a
   native value the encoder cannot represent fails with `InvalidRequest`
   before sending - NaN and infinities included, which some encoders write
   anyway, as `NaN`, `"NaN"` or `null`, though JSON has no such numbers.
6. **A payload which came back can go out again as it is.** Replies - and
   what the SDK receives in the proxy - hold payloads in the type the table
   names, which requests take as they are and send as the same JSON value:
   republishing a publication from history, or answering a publish proxy with
   the client's data, changes nothing. Dynamic languages give the decoded
   native value (`pub.data["text"]`), parsing the reply in one go as they
   must; the others give a JSON type, which costs nothing to send on and one
   call to decode into the application's own types.
7. **Go, Rust, Swift and Kotlin take one JSON type only,** by choice: one
   type in and out, and encoding a value is one call of the language's JSON
   library. In Go, Rust and Swift the type is raw JSON; in Kotlin it is the
   library's `JsonElement`.

| Language | JSON library | Requests take | Raw JSON | Replies give | Decode into own type | Pitfalls |
|---|---|---|---|---|---|---|
| Go | `encoding/json/v2` | raw JSON only (rule 7): `json.Marshal(v)` | `jsontext.Value` | `jsontext.Value` | `json.Unmarshal(v, &x)` | |
| Python | `json` | `dict`, `list`, `str`, `int`, `float`, `bool`, `None`, or raw JSON | `RawJSON` of the SDK, holding `bytes` or `str` | decoded values | `Model.model_validate(data)` (pydantic) | encode with `allow_nan=False`: by default `json.dumps` writes `NaN`, and an HTTP library's own encoder may too (aiohttp's does). Request and result types are generated dataclasses: never pydantic models in the API, which would tie applications to one major version of it |
| JavaScript, TypeScript | `JSON` | objects, arrays, `string`, `number`, `bigint`, `boolean`, `null`, or raw JSON | `rawJSON(text)` of the SDK - `JSON.rawJSON` takes primitives only | decoded values; integers past 2^53 as `bigint` with the `bigIntegers` option | a cast, or a schema library | `JSON.stringify` writes NaN and infinities as `null` and drops `undefined`: refuse non-finite numbers. Integers past 2^53: the note below the table |
| PHP | `ext-json` | arrays, `stdClass`, scalars, `null`, `JsonSerializable`, or raw JSON | `RawJson` of the SDK, holding a `string` | objects: `json_decode($json, false)` | a DTO's constructor | `JSON_THROW_ON_ERROR`, `JSON_PRESERVE_ZERO_FRACTION`. Objects, not associative arrays: arrays turn `{}` and `{"0":"a"}` into JSON arrays when sent on. An empty object in a request is `new \stdClass()` - `[]` is an array |
| Ruby | `json` 2.10+ (bundled from Ruby 4.0; a gem dependency before) | `Hash`, `Array`, `String`, `Integer`, `Float`, `true`, `false`, `nil`, or raw JSON | `JSON::Fragment` | `JSON.parse`: `Hash` with `String` keys | `Model.new(**h.transform_keys(&:to_sym))` | |
| Java | Jackson, internal to the SDK | POJOs, `Map`, `List`, scalars, or raw JSON | `RawJson` of the SDK, holding the text | `RawJson`: `toString()` is the JSON | `raw.decode(Foo.class)` | Never Jackson types in the API: Jackson 3 moved them from `com.fasterxml.jackson` to `tools.jackson`, and an application uses one or the other. Disable `WRITE_NAN_AS_STRINGS`, which writes NaN as `"NaN"` by default |
| Kotlin | kotlinx.serialization 1.10+ | `JsonElement` only (rule 7): `buildJsonObject`, `Json.encodeToJsonElement(v)` | `JsonUnquotedLiteral(text)` | `JsonElement` - numbers keep their text | `Json.decodeFromJsonElement<Foo>(e)` | |
| C# | System.Text.Json | POCOs, anonymous types, `JsonNode`, `JsonElement`, or raw JSON | `RawJson` of the SDK, written with `Utf8JsonWriter.WriteRawValue` | `JsonElement` which outlives the reply (`Deserialize<JsonElement>`, or `Clone()` from a `JsonDocument`) | `element.Deserialize<Foo>()` | |
| Rust | serde_json, feature `raw_value` | raw JSON only (rule 7): `serde_json::value::to_raw_value(&v)` | `Box<RawValue>` | `Box<RawValue>` | `serde_json::from_str::<Foo>(raw.get())` | serde_json writes NaN and infinities as `null`, which the SDK cannot tell from a null: the docs say so |
| Swift | Foundation `Codable` | raw JSON only (rule 7): `JSONEncoder().encode(v)` | `Data` | `Data` | `JSONDecoder().decode(Foo.self, from: data)` | |
| Dart | `dart:convert` | `Map<String, dynamic>`, `List`, `num`, `String`, `bool`, `null`, or raw JSON | `RawJson` of the SDK, holding a `String` | decoded `Object?` | `Foo.fromJson(map)` | on the web, integers past 2^53 lose precision: the note below the table |
| Elixir | `JSON` (Elixir 1.18+) | maps, lists, strings, numbers, booleans, `nil`, or raw JSON | `RawJSON` struct of the SDK, encoded through `JSON.Encoder` as its text | decoded terms, maps with string keys | a struct, or an Ecto embedded schema | |

A language not listed follows the closest row: its standard or dominant JSON
library, kept out of the SDK's API when it has rival major versions; native
values in and out where the language is dynamic, its JSON type where it is
not; and a raw JSON type of the SDK's own where the library has none.

**Integers past 2^53.** JavaScript, and Dart on the web, decode JSON numbers
as doubles, which hold integers exactly only up to 2^53 - in browsers running
a client SDK too, whatever language published the payload. So every SDK's
docs MUST say: a payload SHOULD carry integers which may pass it - 64-bit
IDs, say - as JSON strings, `{"id": "9007199254740993"}`, as proto3's JSON
mapping does for `int64` (gocent: the `json:",string"` option). A string is
one type in every message, read exactly in every language and tool, and kept
through every round trip.

**JavaScript: the `bigIntegers` option**, as in centrifuge-js, the client
SDK, for applications which cannot change how their payloads are written.
Off by default: payloads decode with plain `JSON.parse`, integers past 2^53
rounded. With `bigIntegers: true`, integers a `number` cannot hold exactly
decode as `bigint`, and every other number stays a `number`. A `bigint` in a
request is always encoded as a JSON number, exactly:

```js
const integerLiteral = /^-?\d+$/;
function decode(text, bigIntegers) {
  // An integer past 2^53 has 16 digits or more: most replies skip the reviver.
  if (!bigIntegers || !hasSourceText || !/\d{16}/.test(text)) return JSON.parse(text);
  return JSON.parse(text, (key, value, ctx) =>
    typeof value === "number" && !Number.isSafeInteger(value) && integerLiteral.test(ctx.source)
      ? BigInt(ctx.source) // the original digits, not the rounded number
      : value);
}

function encode(value) {
  return JSON.stringify(value, (key, v) => {
    if (typeof v === "bigint") return JSON.rawJSON(v.toString());
    if (typeof v === "number" && !Number.isFinite(v)) throw new TypeError(`${key}: ${v} is not a JSON number`);
    return v;
  });
}
```

- The reviver's third argument holds each primitive's source text, and
  `JSON.rawJSON` writes a primitive's text verbatim: Chrome and Edge 114+,
  Firefox 135+, Safari 18.4+, Node.js 21+, Deno 1.33+, Bun 1.1.43+, and
  edge runtimes on V8.
- `hasSourceText` is found once, at startup:
  `JSON.parse("1", (k, v, ctx) => ...)` sees whether `ctx.source` is `"1"`.
  Without it, `bigIntegers: true` fails creating the client with
  `InvalidConfig`, naming the runtimes which have it: a server's runtime is
  its owner's choice, and one asking for exact integers must not get rounded
  ones. (centrifuge-js ignores the option there instead: a browser is the
  visitor's choice, and refusing would break the page.)
- Only integer literals outside the safe range become `bigint`: `1e300` and
  `1.5` stay numbers, and so does every integer a `number` holds exactly.
- Without `JSON.rawJSON`, a `bigint` in a request fails with
  `InvalidRequest` before sending (rule 5), naming the runtimes which have it.
- Off by default, because a field's type would then follow its value: a
  `bigint` does not mix with a `number` - `id + 1` throws, `42n === 42` is
  false, a `Map` keyed by one misses the other - and `JSON.stringify` refuses
  it. The docs say so, and show the replacer for an application which sends
  such a payload on with a `JSON.stringify` of its own:
  `(k, v) => typeof v === "bigint" ? JSON.rawJSON(String(v)) : v`.
- Without the option, a payload from a reply sent back unchanged loses the
  digits of an integer past 2^53; the round trip of rule 6 is exact for every
  other value. The docs say so, next to the advice to use strings. This is
  the one place where the spec puts agreeing with centrifuge-js ahead of
  Exact: the same payload decodes the same way on both sides.
- `JSON.rawJSON` takes primitives only, so the SDK's own `rawJSON` for an
  object or array is spliced in by the SDK: written as a unique placeholder
  string by `JSON.stringify`, then replaced by its text, checked to be valid
  JSON.

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
batch, batches whose callers left, and a broadcast retry's attempt in flight:
the retry stops at its next attempt, which fails with `Closed`. It returns
the deadline's error if that ends first, leaving the rest to go on, and MAY be
called again. With its default HTTP client the SDK then closes idle connections.

## 4. Errors

### 4.1 Kinds

| Kind | Meaning | Contents |
|---|---|---|
| `Error` | Centrifugo refused the request or failed to carry it out | `code` (stable), `message` (for people, not compared), `temporary` |
| `HTTPError` | The request did not reach the API method | `status_code`, `body` (start of it), `header` (always present: request IDs, `Retry-After`) |
| `DecodeError` | The reply is not Centrifugo's - usually a wrong endpoint | the cause |
| `InvalidRequest` | Not sent: sections 3.4 and 3.5 | its message names the method and problem; the cause, when there is one, is wrapped |
| `InvalidConfig` | Client not created: section 3.1 | its message names the field and the fix |
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
- Error messages SHOULD carry hints:

  | Error | Hint |
  |---|---|
  | A redirect | Check http or https in the endpoint |
  | A 404 for a PRO-only method | The server is likely Centrifugo OSS |
  | Any other 404 | Is the endpoint the API base URL? |
  | A 401 | Check the credentials |
  | Code 104 for a PRO-only command in a batch | The server may be Centrifugo OSS |

- In a batch, a PRO-only command sent to Centrifugo OSS fails with code 104
  (not found) instead of a 404. That is Centrifugo's answer, and the SDK
  keeps it. So it is an `Error` matching `NotFound`, while the call sent
  alone is an `HTTPError` 404, which does not: a missing route is not a
  missing entity. A PRO method on OSS is a configuration mistake, seen on the
  first call either way. Its hint is softer because 104 has other causes
  too.

### 4.2 Transport error mode

With `http_api.error_mode: transport`, Centrifugo answers a failed call with
an HTTP status and the error alone as the body. The SDK MUST return the same
`Error` as in a 200 reply when the status is one Centrifugo maps errors to -
400, 404, 409, 416, 429, 500 - and the body is `{"code": N, ...}` with
N ≥ 100 and N not in 400-599. A body whose code is an HTTP error status
(`{"code": 429}` from a rate-limiting gateway) is a gateway's: it stays an
`HTTPError`.

### 4.3 The core rule

A call returns an error unless it did everything asked:
`if err != nil { return err }` - or a raised exception - MUST always be correct
code. Broadcast and batch apply it too (section 7).

### 4.4 Retryable

`retryable(err)` reports whether a new attempt, with time left, may succeed:
the error came from a temporary condition, not from the request. A call's
own deadline counts - an outer loop, a queue worker say, retries with a new
one - while a retry under the deadline which ended fails at once.

Retryable:

- an `Error` with `temporary`, or code 100 (servers before `temporary`);
- network failures: refused or reset connections;
- timeouts, the caller's own deadline included;
- an `HTTPError` 429 or 5xx;
- a broadcast or batch partial error with any retryable part.

Not retryable:

- any other `Error`: unknown channel, bad request, ...;
- `InvalidRequest`, `InvalidConfig`, `Closed`;
- a cancelled call;
- other `HTTPError`s, 401 included;
- `DecodeError`, TLS certificate errors.

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

- `exponential_backoff(base, limit)` returns `delay(attempt)`: `base` after
  the first failure, doubling, capped at `limit`, each delay a uniformly
  random time in `[d/2, d]`. A negative base counts as 0, a limit below base
  as base, an attempt below 1 as 1; doubling MUST NOT overflow.
- Retrying a single call, such as `publish`, belongs to the application: a
  loop of the call, `retryable(err)` and `wait(deadline, delay(attempt))`,
  with the same `idempotency_key` on every attempt. Docs SHOULD show it
  (gocent: `ExampleRetryable`).
  A broadcast is not retried this way: `retryable(err)` is true when any
  channel may still succeed, and repeating the call resends to the channels
  which got it - twice on the NATS broker, which ignores keys. Retry only
  `retryable_channels()`, or use `broadcast_with_retry` (section 7.3), which
  does that.
- `wait(deadline, d)` waits for `d` or until the call's deadline or
  cancellation, and returns that error - never a sleep the deadline cannot
  end. A language without a deadline object - Python - has no such helper: its
  own sleep, which cancellation ends, is the wait, and each attempt takes its
  own timeout.

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
match `UnknownChannel`. This holds in every case, so that every language
behaves the same: a broadcast into one channel, or one where every channel
failed, does not match `UnknownChannel` either - `failed()` holds each
channel's error. For one channel, `publish` is the call.
`retryable_channels()` returns the channels a retry should go to.

A partial error's message SHOULD say how many parts failed and that the others
succeeded. A broadcast's SHOULD also say - when some failure is retryable - to
retry only those channels, with the same `idempotency_key`; a batch's commands
differ from each other, so a batch has no such advice (gocent: "broadcast: 2
of 50 channels failed, 48 got the publication: ... (retry only
RetryableChannels, with the same IdempotencyKey)"). Where an error usually
means nothing happened, as an exception does, a caller retrying the whole
broadcast would publish twice into the channels which got it.

In a language with exceptions, the exception raised for a partial failure
carries its result (`err.result`): the same object a Go call returns beside
its error, every channel's or command's outcome included. A failure as a whole
raises its own error, the network's say: its result would only say that every
part failed with it. There is no
mode which returns a partial result instead of raising: the core rule
(section 4.3) holds in every language. Docs SHOULD show how to log the
failures and go on:

```python
try:
    client.broadcast(channels=channels, data=data)
except BroadcastFailedChannelsError as e:
    log.warning("broadcast failed in %s", e.result.failed())
```

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
- A batch is sent once, whatever happened: sending again returns `BatchSent`,
  and adding a command to a sent batch is a programming error the SDK refuses
  loudly (gocent panics). An empty batch does nothing and succeeds - but
  returns `Closed` from a closed client. A batch is not safe for concurrent
  use.
- A reply count different from the command count is a `DecodeError`.

### 7.3 Broadcast with retry

`broadcast_with_retry(req, options)`:

- sends only the channels which may still succeed: a channel which failed for
  good is not retried, and when only those are left it stops;
- every attempt carries the same `idempotency_key`; without one, the SDK
  makes a random one for the call (section 17) and returns it in the result;
- the result holds every channel's last outcome and how many attempts each
  took;
- `max_attempts` - a default (section 17); `attempt_timeout` bounds each
  attempt while the call's deadline bounds all, waits included;
- `on_attempt_err(attempt)` is called after each failed attempt a retry may
  fix, except the last, with the attempt number, its error, the channels to
  retry, the result so far and the key. It waits, logs or stops by returning
  an error; the call then returns both errors joined - or only the
  broadcast's error when that already wraps the callback's (a deadline, say).
  In a language with exceptions it MAY instead return how long to wait - the
  default when it returns nothing - and raise to stop, its exception chained
  to the broadcast's error (pycent): a callback which only logs then cannot
  stop the retries by accident.
  Default: `wait(deadline, delay(attempt))` with the `delay` of an
  `exponential_backoff` (section 17);
- a failure no retry can fix - a 401, an invalid request - stops it without
  calling `on_attempt_err`, and no attempt starts once the call's deadline has
  ended;
- a negative `max_attempts` or `attempt_timeout` fails it with
  `InvalidRequest` before any attempt: every channel failed, 0 attempts.

## 8. Automatic batching

`auto_batch` sends concurrent calls together, cutting the requests Centrifugo
handles under load. An SDK whose language has no concurrent calls MAY leave it
out.

- `max_in_flight` (0 disables): below it, every call is sent at once, alone.
  There are no timers: nothing ever waits for a batch to fill.
- At the cap, calls queue first in, first out, and when a request finishes
  all those queued go out as one batch, at most `max_batch_size`. Requests in
  flight never exceed `max_in_flight`. A larger `max_in_flight` batches less:
  above the calls a process has in flight at once it never batches, and just
  below it batches calls one at a time, only limiting them. Docs SHOULD say
  who it is for - processes making many calls at once - and how to choose the
  value (section 17).
- Batches are sent from tasks or threads of the SDK's, not a caller's; where
  starting one costs - a thread - they are kept between batches, and end
  when idle.
- Refused at creation: negative values; `max_batch_size` without
  `max_in_flight`; `max_batch_size` of 1.
- An automatic batch always goes to `/api/batch`, even with one command, so
  that replies have one shape, and with `parallel: true`: its calls were made
  concurrently, so they have no order to keep.
- Every call gets exactly its own reply: its command's result or error, or the
  error of the batch request which carried it. A batch request failing as a
  whole fails every call in it; no call is retried alone.
- A call's command is encoded before it queues; once a call returns nothing
  reads its request again.
- A call whose deadline ends while queued leaves without its command being
  sent. A batch runs on its own, never on a caller's context, until the latest
  deadline of its calls (`request_timeout` for a call without one), so that no
  caller's earlier deadline cuts it short for the others - and never less than
  `request_timeout` from when it is sent, so that a call which waited in the
  queue still gets a full timeout. A reply arriving as a caller's deadline
  ends is returned, not a timeout.
- Calls made one after another keep their order: each waits for its reply.
- In the transport error mode (section 4.2), a call sent alone gets its error
  with an HTTP status, and one in an automatic batch inside a 200 reply. The
  SDK returns the same `Error` either way; only `on_request` sees the
  difference, as it reports requests, not calls.
- Docs SHOULD say that a proxy in front of Centrifugo may limit a request's
  size (nginx: 1 MB by default) where Centrifugo does not, failing a whole
  batch with a 413 its calls would not get alone; `max_batch_size` or the
  proxy's limit is the fix.
- Callers' context values reach a batch only through `batch_context`.

## 9. Commands for async consumers

A command per API method (gocent: `NewCommand(req)`; Python:
`command.publish(channel=..., data=...)`; JavaScript:
`command.publish({ channel, data })`) returns `{method, payload}`: the API
method and the request as JSON. It MUST validate the request as a call would (section 3.5), so that a
command Centrifugo could not run fails when written rather than in a consumer
which drops it. Encoded as JSON it is the `{"method": ..., "payload": ...}`
message consumers take whole (Kafka, NATS JetStream). Docs MUST say that a
consumer keeps commands in order only within a partition: a channel's
commands keep their order only when every writer derives their partition from
the channel the same way.

## 10. Observability

- `on_request(context, info)` is called after every request, including one
  which failed before reaching the network (the endpoint function, the token
  function or encoding failed). It is called synchronously: the call waits for
  it. `info` holds `method` ("batch" for batches), `commands`, `failed`
  (commands answered with an error; a broadcast failing in some channels is
  not counted), `auto_batch`, `duration` (from before getting the endpoint and
  credentials to the reply read; MAY be zero on a coarse clock), `status_code`
  (0 without a reply), `err` (the request's own error, nil when Centrifugo's
  reply was read; in the transport error mode, the error of the request's one
  command, which `failed` then does not count). A broadcast retry reports each
  attempt, an automatic batch once.
- `batch_context(base, callers)` makes an automatic batch's request context,
  from a base context and its callers' - for a tracing span linking every
  caller's span. In Python a batch runs in an empty `contextvars.Context`, and
  `batch_context(callers)` takes the callers' contexts and returns a context
  manager entered around the batch's request - `tracer.start_as_current_span`
  with a link to each caller's span, say.
- Tracing and metrics otherwise come from the HTTP client's transport; the SDK
  MUST NOT depend on a tracing library. Docs SHOULD show the language's
  OpenTelemetry HTTP instrumentation.

## 11. Tokens

### 11.1 Keys

| Key | Algorithm | Centrifugo option |
|---|---|---|
| HMAC secret, not empty | HS256 | `client.token.hmac_secret_key` |
| RSA private key | RS256 | `client.token.rsa_public_key` |
| ECDSA private key | ES256 / ES384 / ES512 for P-256 / P-384 / P-521; other curves refused | `client.token.ecdsa_public_key` |
| Ed25519 private key | EdDSA | `client.token.ed25519_public_key` (Centrifugo v6.10.2+), or JWKS |

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
| expiry | `exp` | Required: a time, or never - `exp` left out, the token accepted forever |
| connection / subscription expiry | `expire_at` | Unset: at `exp`; a time: the connection or subscription expires then; never: `expire_at: 0`, `exp` still bounds the token |
| `issued_at` | `iat` | Default now |
| `id` | `jti` | For PRO token revocation |
| `audience`, `issuer` | `aud`, `iss` | |
| `info` / `b64info` | `info` / `b64info` | |
| `channels` / `subs` (connection) | `channels` / `subs` | Not both: Centrifugo ignores `channels` when `subs` is set |
| `meta` (connection) | `meta` | Not visible to other clients, but visible to the user owning the token: docs warn against putting in it what that user must not see |
| `caps`, `labels` (connection, PRO) | `caps`, `labels` | |
| `channel` (subscription) | `channel` | Required |
| `data` / `b64data`, `override`, `allow`, `server_tags_filter` (subscription) | same names | `allow` and `server_tags_filter`: PRO |

Each expiry is one value, and "never" is always chosen explicitly. An SDK
SHOULD use its language's sum type for it (an enum, a union), so that a
missing or contradicting expiry cannot be written. Where the language has
none, as Go, it is fields - gocent: `ExpiresAt` and `NoExpiry`,
`ConnectionExpiresAt` and `ConnectionNoExpiry`, the subscription's alike - and
signing MUST refuse a missing token expiry and both fields of a pair set.
Claims are written with the exact names above.

### 11.3 JWKS

`jwks(keys)` returns the public keys as a JSON Web Key Set, for
`client.token.jwks_public_endpoint` and PRO's `http_api.jwks.endpoint`:
`{"keys": [...]}`, each key with `kty` (`RSA`, `EC`, `OKP`), `kid`, `use:
"sig"` (Centrifugo skips keys without it), `alg`, and `n`/`e`, `crv`/`x`/`y`
or `crv: "Ed25519"`/`x`, base64url unpadded, EC coordinates at the curve's
size. It MUST refuse: no keys, an HMAC key, a key without an ID, an ID used
twice. A JWKS endpoint built from keys it refuses fails when it is built, not
when it is called.

A JWKS endpoint answers `GET` and `HEAD` with `Content-Type:
application/json` and a `Cache-Control` letting caches keep the keys for a
while (section 17), for caches between the two. Centrifugo reads no header
of the reply: it keeps the keys for its own time, five minutes since v6.10.2.

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
TTL (its default: section 17), and is safe for concurrent use. It
refuses an expiry or issue time set in its claims, and a negative TTL.

Docs MUST say: PRO accepts a token signed with any key of its JWKS endpoint,
and checks audience, issuer and scope only when configured to - so API tokens
MUST be signed with keys of their own, published at a JWKS endpoint other
than the client tokens' one, and PRO configured to require an audience or
scope only API tokens carry. Otherwise any user can call the server API with
their connection token.

## 12. Endpoints for client SDKs

Endpoints the `getToken` and `getSignature` callbacks of client SDKs call,
around an application function deciding who gets what. The function is
required when the endpoint is built.

- Success: a JSON object, `Content-Type: application/json`, `Cache-Control:
  no-store`.
- The application's `Unauthorized` error (also wrapped): 403 - client SDKs
  stop asking, and disconnect or unsubscribe. It is the endpoints' own,
  named after client SDKs' `UnauthorizedError`, and distinct from the API
  client's `Unauthorized` (section 4.1), which means Centrifugo refused the
  SDK's credentials (gocent: `token.ErrUnauthorized`, `gocent.ErrUnauthorized`).
- Any other error: 500, reported to `on_error` - client SDKs retry.
- A malformed request: 400, reported to `on_error`; a wrong method: 405 with
  `Allow`. Bodies are read up to a limit (section 17).

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
| shared poll refresh; channel state (PRO) | result, `ProxyError` (a failure) |
| refresh, sub refresh; cache empty (PRO) | result only (refresh and sub refresh end through the result's `expired`) |

- A handler takes `POST` only (405 with `Allow` otherwise), reads bodies up
  to a configurable limit (section 17), and answers a malformed request with
  400. Its function is required when the handler is built.
- It answers with exactly one of result, error and disconnect: Centrifugo
  takes a response with none as accepting the request as sent.
- `ProxyError{code, message, temporary}`, with a code in [400, 1999],
  rejects the request. `Disconnect{code, reason}`, with a code in
  [4000, 4999], closes the connection; codes in [4500, 4999] tell the client
  not to reconnect. Wrapped ones count.
- An answer the proxy type cannot carry, any other error, or a result which
  cannot be encoded fails the request with 500, reported to `on_error`:
  Centrifugo answers the client with a temporary internal error, and for
  refresh and sub refresh asks again a minute later.
- Payloads follow section 3.4: a request's payloads arrive in the type the
  table of section 3.4 gives for replies, and a result takes them back as they
  are.
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
  raw JSON is sent as the same JSON value, number text kept; a string is a
  JSON string; bytes, NaN and invalid raw JSON are refused before sending; a
  payload from a reply sent back unchanged is the same JSON value - `{}`,
  `{"0":"a"}`, an integer past 2^53 (in JavaScript only with `bigIntegers`;
  not in Dart on the web) and a float with a zero fraction included.
- Automatic batching under random concurrency, with a race detector where the
  language has one.
- Integration tests of every OSS method against the `centrifugo/centrifugo`
  image, and of every PRO method against `centrifugo/centrifugo-pro` in
  sandbox mode (no license key), with the setup in gocent's `testdata/`:
  tokens connecting a real client (HMAC, and Ed25519 verified by
  `client.token.ed25519_public_key`), proxy handlers called by Centrifugo,
  commands run by a consumer, API tokens accepted - and refused without the
  audience or scope, as connection tokens, and from unpublished keys.
- The vectors below.

Section 15.2 lists what the tests check.

### 15.1 Vectors

The vectors fix `iat`: to check them, an SDK needs a way to sign at a given
time - a clock it can set, or an internal parameter (gocent: the unexported
`signAt`). Tokens take it as `issued_at`.

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

### 15.2 Checklist

Every rule an SDK MUST follow, one line each; the section holds the rule in
full and why. A rule added to a section is added here too.

Generation and the client:

- [ ] Request and result types generated from the protos, never edited by
  hand, checked current in CI; protos from a released tag (2).
- [ ] Field names on the wire exactly the protos', case-sensitive; zero
  values not sent (2).
- [ ] `BoolValue` fields optional, with a helper; PRO-only methods marked
  (2).
- [ ] Safe for concurrent use; creation validates all it can, with
  `InvalidConfig` naming the field and the fix (3.1).
- [ ] Exactly one of `api_endpoint` and `api_endpoint_func`; an endpoint
  an absolute `http`/`https` URL with a host, without query, fragment or
  credentials, not ending in a method name; `api_key` and
  `bearer_token_func` not together (3.1).
- [ ] An endpoint from `api_endpoint_func` validated per request; an empty
  bearer token, or one with control characters, fails the call before
  sending (3.1).
- [ ] `header` refuses credentials, `Content-Type` and invalid names and
  values (3.1).
- [ ] `POST {api_endpoint}/{method}`, never the legacy `/api` (3.2).
- [ ] Redirects not followed (3.2).
- [ ] The default HTTP client keeps connections alive in a pool sized for
  concurrent calls (3.2).
- [ ] Response bodies closed and drained to a bound; request bodies not
  reused while the transport may read them (3.2).
- [ ] One deadline per request: the platform's deadline, or the time left
  given to each operation and checked between body chunks; docs say what can
  outlast it; every timeout is the language's one timeout error (3.2).
- [ ] A client the caller passed is not changed: credentials and headers go
  on each request (3.1).
- [ ] A reply which is not of the reply's shape is a `DecodeError` (3.2).
- [ ] Every API method but `rpc`, on the client, on a batch and for
  commands; a request in the language's idiom; a plain-object request's
  unknown fields refused (3.3).

Payloads and validation:

- [ ] Native values in, but in the languages of rule 7; a raw JSON type of
  the SDK's, sent as the same JSON value (3.4).
- [ ] A string is a JSON string; bytes refused outside the `b64` fields;
  invalid JSON, NaN and infinities never sent (3.4).
- [ ] A payload from a reply goes back unchanged as the same JSON value
  (3.4).
- [ ] Docs advise strings for integers which may pass 2^53 (3.4).
- [ ] JavaScript: `bigIntegers`, off by default, refused with
  `InvalidConfig` where it cannot work (3.4).
- [ ] A request the client can tell is wrong is not sent: `InvalidRequest`;
  no other Centrifugo rule copied (3.5).
- [ ] `close` refuses new calls with `Closed` and waits for those in progress
  (3.6).

Errors and retries:

- [ ] The error kinds of 4.1; an `Error` matched by code alone; an unknown
  code still an `Error`; a 401 matches `Unauthorized` (4.1).
- [ ] The transport error mode returns the same `Error` as a 200 reply (4.2).
- [ ] An error unless the call did everything asked (4.3).
- [ ] `retryable` as in 4.4; docs: retry a publication only with the same
  `idempotency_key` (4.4).
- [ ] Docs explain idempotency keys (5).
- [ ] `exponential_backoff` clamps its arguments as in 6 and cannot
  overflow; `wait` ends with the deadline (6).

Broadcast, batch, automatic batching, commands, observability:

- [ ] A broadcast's result holds every channel, whatever the error;
  `replied` and `failed()` never disagree with it; a partial error does not
  match its parts (7.1).
- [ ] Where errors are exceptions, the exception carries the result (7.1).
- [ ] A batch: options fixed at creation, pending handles, sent once,
  `BatchSent` and `BatchNotSent`, a wrong reply count a `DecodeError`; docs
  on sequential batches in PRO (7.2).
- [ ] An invalid command fails the whole batch before sending, naming its
  index; an empty batch succeeds, but returns `Closed` from a closed client;
  a command added to a sent batch refused loudly (7.2).
- [ ] `broadcast_with_retry`: one key on every attempt, made when missing;
  only the channels which may still succeed; no attempt after the deadline;
  a negative `max_attempts` or `attempt_timeout` refused before any attempt
  (7.3).
- [ ] Automatic batching, below `max_in_flight`: every call sent at once,
  alone; no timers, nothing waits for a batch to fill (8).
- [ ] At the cap: calls queue first in, first out, and go out together when
  a request finishes, at most `max_batch_size`; requests in flight never
  exceed `max_in_flight` (8).
- [ ] Refused at creation: negative values, `max_batch_size` without
  `max_in_flight`, `max_batch_size` of 1 (8).
- [ ] An automatic batch goes to `/api/batch` with `parallel: true`, even
  with one command; every call gets exactly its own reply; a batch failing as
  a whole fails every call in it, none retried alone (8).
- [ ] A command encoded before it queues; nothing reads a request after its
  call returned (8).
- [ ] A call whose deadline ends while queued leaves without its command
  being sent (8).
- [ ] A batch never runs on a caller's context: until the latest deadline of
  its calls, and at least `request_timeout` from when it is sent; a reply
  arriving as a deadline ends is returned, not a timeout (8).
- [ ] Callers' context values reach a batch only through `batch_context`
  (8).
- [ ] Commands validate as a call does; docs on partitions (9).
- [ ] `on_request` after every request, synchronously; no tracing library
  (10).

Tokens, endpoints, shared poll, proxy:

- [ ] Keys, algorithms and JWTs as in 11.1; ECDSA curves other than P-256,
  P-384 and P-521 refused; a payload claim which is not valid JSON fails
  signing (11.1).
- [ ] An expiry required, "never" chosen explicitly, contradictions refused;
  `channels` and `subs` not both; docs on `meta` (11.2).
- [ ] `jwks` refuses no keys, HMAC keys, keys without an ID, an ID used
  twice; its endpoint's headers; docs on rotation (11.3).
- [ ] `api_token` refuses HMAC keys, keys without an ID, neither audience
  nor scope, no expiry, an empty scope or one with a space; docs: keys and
  an endpoint of their own (11.4).
- [ ] `api_tokens` reuses a token for half its TTL, is safe for concurrent
  use, and refuses an expiry or issue time in its claims and a negative TTL
  (11.4).
- [ ] HTTP handlers require no particular framework (conventions).
- [ ] Endpoints for client SDKs: statuses, headers and limits of 12; their
  `Unauthorized` distinct from the API client's (12).
- [ ] Shared poll signature endpoint: `keys` always an array, empty
  included; a key the client did not ask for fails the request with 500
  (12).
- [ ] Shared poll signatures as in 13.
- [ ] Proxy handlers: `POST` only, body limit, exactly one of result, error
  and disconnect, 500 for anything else; docs: keep them internal (14).
- [ ] Proxy payloads as in 3.4; binary mode: payloads in the `b64` fields
  (14).
- [ ] The tests of 15, and the vectors of 15.1 (15).

## 16. Versions

- An SDK's versions are its own: a new Centrifugo major brings a new SDK
  major, and an SDK MAY release a major of its own between them. The README
  holds a table of the Centrifugo versions each SDK major supports (gocent v4:
  Centrifugo v6).
- A field removed from the protos is a breaking change in typed languages:
  keep it as a deprecated field Centrifugo ignores until the SDK's next major.
- An SDK following section 2 is ready for Centrifugo v7's exact JSON names.

## 17. gocent's values

The defaults and limits an SDK SHOULD use (conventions). They are given only
here; the sections they belong to say what they are for.

| Value | gocent | Section |
|---|---|---|
| `request_timeout` | 10 s | 3.1 |
| Default HTTP client | 256 connections to a host, all kept while idle; idle ones closed after 50 s | 3.2 |
| Response bodies | drained up to 64 KiB; an error body read up to 4 KiB, 512 bytes kept | 3.2 |
| `broadcast_with_retry` | `max_attempts` 2 (one retry); waits `exponential_backoff(100 ms, 5 s)`; key `gocent-` + 26 base32 characters | 7.3 |
| Automatic batching | `max_batch_size` 1000; `max_in_flight` suggested at about half the calls a process makes at once | 8 |
| JWKS endpoint | `Cache-Control: public, max-age=300` | 11.3 |
| `api_tokens` | TTL 5 minutes | 11.4 |
| Endpoints for client SDKs | bodies up to 1 MiB | 12 |
| Proxy handlers | bodies up to 16 MiB, configurable | 14 |

## 18. Changes

### 0.2 - 2026-10-28

- Unified meaning, idiomatic spelling: a method takes its request in the
  language's idiom - a struct in Go, keyword arguments in Python, named
  arguments in PHP, a plain object in JavaScript - and a batch and commands
  have a method per API method, as the client (conventions, 3.3, 9).
- A plain-object request refuses fields the SDK does not know (3.3).
- One deadline per request is enforced by the language's public means: where
  the HTTP library times each operation, the time left per operation and a
  check between body chunks, the docs saying what can outlast it (3.2).

### 0.1 - 2026-10-25

The first version. Rules settled while it was reviewed:

- Python: a per-call timeout argument in async code too; no `wait` helper;
  only partial errors carry `result`; `on_attempt_err` may return the wait
  and raise to stop; `batch_context` over `contextvars` (conventions, 6, 7.1,
  7.3, 10).
- A partial error's message says the others succeeded; a broadcast's, how to
  retry only the failed channels (7.1).
- JavaScript decodes integers past 2^53 as numbers by default, with the
  `bigIntegers` option of centrifuge-js, refused where it cannot work; every
  SDK's docs advise strings for them (3.4).
- Go, Rust, Swift and Kotlin take one JSON type only (3.4, rule 7).
- In a batch, a PRO method's code 104 SHOULD carry a hint that the server may
  be Centrifugo OSS (4.1).
- Exceptions carry a broadcast's or batch's result (7.1).
- Retrying a single call belongs to the application; a broadcast is retried
  only in its failed channels (6).
- A partial error never matches its parts, also with one channel (7.1).
- An expiry is one value, in the language's sum type where it has one
  (11.2).
- HTTP handlers are framework-free (conventions).
- The endpoints' `Unauthorized` is distinct from the API client's (12).
- SDK versions are independent of Centrifugo's (16).
- Blocking and async clients alike where a language has both (conventions);
  a caller's HTTP client is never changed (3.1); one deadline also over
  per-operation timeouts (3.2); no pydantic types in a Python SDK's API
  (3.4).
