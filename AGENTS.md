# Working on gocent

gocent is the Go client for Centrifugo's HTTP server API. Applications use it
on their hot paths - publishing on every request, broadcasting to thousands of
channels - so it has to be correct under concurrency, cheap per call, and hard
to misuse. Read this before changing code.

## Invariants

These are not preferences. A change that breaks one is wrong even if every test
passes; if you find yourself about to break one, stop and say so instead.

1. **No dependencies, no cgo, no `unsafe`.** `go.mod` has no `require` block;
   `make no-deps` fails if one appears. JSON is `encoding/json/v2`.
2. **`api_gen.go` is generated, never edited.** It is made from `api.proto` by
   `internal/gen`. `api.proto` is a copy of Centrifugo's
   `internal/apiproto/api.proto`, the source of truth for the API; its comments
   become the Go documentation. `TestGeneratedIsCurrent` and
   `make generate-check` fail when the two disagree.
3. **A call returns a result or an error - with one exception, stated
   everywhere it applies.** `Client.Broadcast` and `Batch.Send` return an error
   together with a complete result when one or more of their parts failed
   (`*BroadcastError`, `*BatchError`). Their `Unwrap() []error` exposes every
   part's error, so `errors.Is` reaches through. They are returned when all
   parts failed too - the error is typed by the shape of the reply, not by
   how many parts succeeded. Any other error means nothing was done.
4. **Errors follow Centrifugo's model.** A Centrifugo error is an `*Error`;
   `errors.Is` matches it against the exported values by `Code` only, never by
   message. Centrifugo adds codes over time: nothing may switch exhaustively
   over codes or treat an unknown code as anything but an `*Error`. The error
   is the same in either of Centrifugo's error modes: in a 200 reply, or - with
   `http_api.error_mode: transport` - as the body of an HTTP error status.
5. **Automatic batching never delays a call and never mixes up replies.**
   - Below `MaxInFlight` a call is sent at once, alone, from its own goroutine.
     There are no timers.
   - At the cap, calls queue first in, first out, and go out together when a
     request finishes. Requests in flight never exceed `MaxInFlight`.
   - Every call gets exactly its own reply. A broadcast inside a batch returns
     what a direct broadcast returns, `*BroadcastError` included.
   - A caller whose context ends while queued leaves without its command being
     sent. A batch runs on its own goroutine with its own timeout, never a
     caller's context.
6. **`New` catches configuration mistakes.** Anything wrong with a `Config` that
   can be seen without a request is an error from `New` wrapping
   `ErrInvalidConfig`, naming the field and the fix.
7. **One timeout story.** Every request carries a context with a deadline (the
   caller's, or `RequestTimeout`), and `DefaultHTTPClient` sets no timeouts of
   its own, so a request has one deadline. Whatever times out - the context,
   or a caller's `http.Client` - the error matches
   `errors.Is(err, context.DeadlineExceeded)`.
8. **HTTP hygiene.** Every response body is closed and drained up to a bound,
   so connections are reused. `http.DefaultClient` is never used.
9. **Invalid requests are never sent.** `jsontext.Value` payloads are checked,
   and publish and broadcast check the rules every Centrifugo shares (a
   channel, some data) before sending; the error wraps `ErrInvalidRequest`. A
   batch with an invalid command sends nothing. Rules which differ between
   Centrifugo versions or setups stay the server's to check.

## Running things

Everything goes through the Makefile; CI runs the same targets.

```sh
make check             # the gate: fmt, vet, no-deps, generated code, race, coverage, lint, govulncheck
make test-race         # just the suite, under the race detector
make test-integration  # against a real Centrifugo configured like testdata/centrifugo.json
make fuzz              # reply decoding; FUZZTIME=2m for something thorough
make generate          # regenerate api_gen.go from api.proto
make bench
```

`make check` is what to run before handing work back. For a change to the
transport, the error model or batching, also run `make test-integration` with
Centrifugo running:

```sh
centrifugo -c testdata/centrifugo.json   # listens on :8100
make test-integration
```

## Updating to a new Centrifugo API

1. Copy `internal/apiproto/api.proto` from Centrifugo over `api.proto`.
2. `make generate`. A new command appears as a `Client` method, a `Batch`
   method and a request type with `APIMethod`, with no hand-written code.
3. `make check`. `TestEveryMethodReachesItsEndpoint` checks every method,
   new ones included, reaches its endpoint and batch command.

The generator understands only what `api.proto` uses today: top-level messages
with scalar, message, repeated and string-keyed map fields, and one service.
Anything else - a `oneof`, an enum, a nested message - fails the generation on
purpose rather than being dropped. Teach `internal/gen/proto.go` the construct
before generating.

Broadcast is the one command written by hand (`broadcast.go`): its result is a
result per channel, which the generator skips. A new command with parts that
can fail on their own would need the same treatment.

## Using gocent: easy mistakes

For an agent writing code against gocent, and for anyone changing messages or
docs here - these are the places a reader goes wrong.

- **A 404 from a method Centrifugo PRO only has means the server is OSS, not
  that APIEndpoint is wrong.** The `*HTTPError` hint asks whether APIEndpoint
  is the API base URL, because that is the usual cause; if other methods work,
  it is not. In a
  batch the same call fails with `ErrNotFound` (code 104) instead. The doc
  comment of every PRO-only method says "Centrifugo PRO only."
- **`IsPartial` does not mean something succeeded.** A `*BroadcastError` or
  `*BatchError` is returned when all parts failed too. `len(Failed) < Total`
  tells whether any part succeeded.
- **Handle a batch's failures once.** Check `Send` for a whole failure
  (`err != nil && !IsPartial(err)`), then read each `Pending`. `BatchError.Failed`
  lists the same failures again, for a summary.
- **Credentials are not required by `New`.** No `APIKey` or `BearerTokenFunc`
  is valid - mutual TLS, a proxy, `http_api.insecure` - so a key which failed
  to load shows as `ErrUnauthorized` on the first call, not at `New`.
- **A larger `MaxInFlight` batches less, not more.** Calls are batched only
  once that many requests are in flight.

## Things that look wrong but are deliberate

- **Request structs are passed by value.** Callers write them as literals, and
  a value cannot change under a call in flight. `hugeParam` is disabled for it.
- **`APIMethod`, not `Method`.** It says what the name is - the API method -
  and matches the other SDKs (`api_method` in the Python one).
- **`rpc` is not offered**, though `api.proto` has it: it calls server API
  extensions, which Centrifugo does not provide. `omittedCommands` in
  `internal/gen/main.go` leaves it out with its request and result types.
- **`BoolValue` fields are pointers.** Absent means "leave the server's setting
  alone", which `false` cannot say. Every other message field is a value, sent
  only when non-zero.
- **An automatic batch always goes to `/api/batch`, even with one command.**
  The command's own endpoint would reply in a different shape; one path keeps
  the reply handling single.
- **`api_gen.go` is left out of `make cover`.** Its methods are one line each,
  all written by the same generator; `TestEveryMethodReachesItsEndpoint` checks
  them as a whole.
