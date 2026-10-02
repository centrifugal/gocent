// Package gocent is a client for the HTTP server API of Centrifugo, the
// real-time messaging server: publish into channels, manage subscriptions and
// connections, read history and presence.
//
// Create one [Client] and share it; it is safe for concurrent use. Every API
// method is a method of the Client, taking a request struct and returning a
// result struct:
//
//	client, err := gocent.New(gocent.Config{
//		APIEndpoint: "http://localhost:8000/api",
//		APIKey:      os.Getenv("CENTRIFUGO_API_KEY"),
//	})
//	if err != nil {
//		return err // the config is wrong: New says how
//	}
//	res, err := client.Publish(ctx, gocent.PublishRequest{
//		Channel: "news",
//		Data:    jsontext.Value(`{"text":"hello"}`),
//	})
//
// Request fields map one to one onto Centrifugo's API; fields left at their
// zero value are not sent. Data is JSON: encode yours with encoding/json or
// encoding/json/v2, or pass bytes you already have. It is checked before
// sending, so a malformed payload never reaches Centrifugo.
//
// The methods most applications need:
//
//   - [Client.Publish] and [Client.Broadcast] send data into one channel or many.
//   - [Client.History] and [Client.Presence] read a channel's recent
//     publications and who is subscribed; [Client.PresenceStats] counts them.
//   - [Client.Subscribe], [Client.Unsubscribe], [Client.Disconnect] and
//     [Client.Refresh] manage users' subscriptions and connections.
//   - [Client.NewBatch] sends many commands in one request.
//
// The other methods and most of the types serve Centrifugo PRO features - push
// notifications, user status, token revocation - and map subscriptions.
//
// # Errors
//
// An error from a method is one of these, told apart with [errors.Is] and
// [errors.As]:
//
//   - [*Error]: Centrifugo refused the request or failed to carry it out. Its
//     Code identifies why; the known codes have values such as
//     [ErrUnknownChannel], which errors.Is matches by code.
//   - [*HTTPError]: the request did not reach the API method. A 401 matches
//     [ErrUnauthorized]: the API key or bearer token was rejected.
//   - [*DecodeError]: the reply was not a Centrifugo reply - most often
//     APIEndpoint points somewhere else.
//   - [ErrInvalidRequest]: the client could tell the request is wrong - a
//     publication without a channel, data which is not JSON - and did not
//     send it.
//   - Anything else comes from the network or the context, wrapped.
//
// A request without a deadline in its context times out after
// Config.RequestTimeout, ten seconds by default, so a call never hangs.
// However a request times out, errors.Is(err, context.DeadlineExceeded) is
// true.
//
// # Retrying
//
// [Retryable] reports whether retrying a call may succeed: the error came
// from a temporary condition - a broker being unavailable, a rate limit, the
// network - not from the request. Newer Centrifugo versions mark such errors
// themselves ([Error.Temporary]). Back off between attempts and bound their
// number; see the example of [Retryable] for a broadcast which drops the
// channels that can never succeed.
//
// A temporary error does not mean nothing was done: after a timeout, and
// even with an internal error, the publication may have happened. So a
// publication is safe to retry only with an IdempotencyKey:
//
//   - Give each publication or broadcast its own key, and reuse it only to
//     retry that same one. A broadcast needs one key, not one per channel.
//   - Centrifugo remembers a key per channel, for five minutes by default. A
//     retry within that time is answered with the first attempt's result and
//     not published again. So is a different publication into the same
//     channel under the same key: a key derived from, say, an order ID must
//     also name the event - "order-42-paid", not "order-42".
//
// To retry a failed batch, build it again with the same keys, leaving out the
// commands which failed for good, and send it: the publications which
// happened are not repeated. See the batch example of [Retryable]. A command without a key
// is repeated, and so is a read, which is harmless.
//
// # Broadcast and batch
//
// A call returns an error unless it did everything asked, and
// [Client.Broadcast] and [Batch.Send] are no exception - though they do many
// things in one request, and each of those can fail on its own. When
// Centrifugo carried out the request and one or more parts failed, up to
// all, the error is a [*BroadcastError] or a [*BatchError] listing them. Any
// other error means the request failed as a whole: no part got a reply. After
// a timeout or a network error Centrifugo may still have carried it out, so
// retry with the same IdempotencyKey.
//
// Every part always has an outcome, whatever the error: a broadcast's result
// holds one entry per channel, and every command of a batch its [Pending].
// When the request failed as a whole, each part's error is that error.
//
// Most callers need no more than err != nil: a broadcast retried with the
// same IdempotencyKey is not published twice into any channel. To go on
// despite some failed parts, check err first, then read each part:
//
//	res, err := client.Broadcast(ctx, req)
//	var be *gocent.BroadcastError
//	if err != nil && !errors.As(err, &be) {
//		return err // no channel got a reply
//	}
//	for _, ch := range res.Channels { ... } // ch.Err is set for a failed channel
//
// # Automatic batching
//
// With [AutoBatch] set, calls made concurrently are sent together once enough
// of them are in flight, which cuts the requests Centrifugo handles - and its
// CPU - under load, with no change to the code making the calls and no delay
// when the client is not busy.
//
// # Tracing and metrics
//
// gocent has no hooks of its own: every request goes through the
// [http.Client] in Config.HTTPClient, so tracing and metrics come from its
// transport. With OpenTelemetry, wrap the transport of the default client:
//
//	hc := gocent.DefaultHTTPClient()
//	hc.Transport = otelhttp.NewTransport(hc.Transport) // go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp
//	client, err := gocent.New(gocent.Config{
//		APIEndpoint: "http://localhost:8000/api",
//		APIKey:      key,
//		HTTPClient:  hc,
//	})
//
// Each call is then a client span, a child of the span in its context, with
// the HTTP client metrics otelhttp records. The transport also sends the
// trace context to Centrifugo: with opentelemetry.enabled and
// opentelemetry.api in Centrifugo's configuration, its spans for the request
// join the same trace.
//
// A call sent in an automatic batch is the exception. The batch request
// carries the commands of several callers, so it does not run on any
// caller's context: its span starts a trace of its own, not linked to the
// spans of the calls it carries. Leave AutoBatch off where every call must
// show in its caller's trace.
//
// A transport of your own works the same way. It must not modify the
// request it is given - net/http's RoundTripper contract - but clone it
// first, as otelhttp does.
//
// # Async consumers
//
// Instead of calling Centrifugo, an application can leave a command for its
// async consumers to run - in a PostgreSQL outbox table, a Kafka topic. A
// command is an API method's name and its request as JSON, and a request
// struct gives both: APIMethod, such as [PublishRequest.APIMethod], returns
// the name, and the request encodes to the JSON:
//
//	payload, err := json.Marshal(req) // {"channel":"news","data":{...}}
//	_, err = tx.ExecContext(ctx,
//		"INSERT INTO centrifugo_outbox (method, payload, partition) VALUES ($1, $2, 0)",
//		req.APIMethod(), payload)
package gocent
