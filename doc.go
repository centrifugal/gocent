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
// Centrifugo PRO is commercial software, under the Centrifugal Labs License
// Agreement (https://centrifugal.dev/license), not under gocent's MIT license:
// these methods are MIT licensed like the rest of gocent, but the server they
// call is governed by that agreement.
//
// # Errors
//
// An error from a method is one of these, told apart with [errors.Is] and
// [errors.AsType]:
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
// Each call is bounded by its context: give a call a timeout of its own with
// context.WithTimeout. A request without a deadline in its context times out
// after Config.RequestTimeout, ten seconds by default, so a call never hangs;
// http.Client.Timeout, in Config.HTTPClient, bounds every request of the
// client on top of that. [Client.BroadcastWithRetry] makes several requests
// in one call: its context bounds them all, and
// [BroadcastRetryOptions].AttemptTimeout each one. However a request times
// out, errors.Is(err, context.DeadlineExceeded) is true.
//
// # Retrying
//
// [Retryable] reports whether retrying a call may succeed: the error came
// from a temporary condition - a broker being unavailable, a rate limit, the
// network - not from the request. Newer Centrifugo versions mark such errors
// themselves ([Error.Temporary]). Back off between attempts and bound their
// number - see the example of [Retryable]. A broadcast is retried with
// [Client.BroadcastWithRetry], which sends again only the channels which may
// still succeed, with one IdempotencyKey for all its attempts.
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
//   - The memory, Redis and PostgreSQL brokers keep keys. The NATS broker
//     ignores them, so with it a retry publishes again.
//
// To retry a failed batch, build a new one with the commands which may still
// succeed - those [BatchResult.Failed] lists with a retryable error, which is
// every command when the batch failed as a whole - with the same keys: the
// publications which happened are not repeated. See the batch example of
// [Retryable]. A command without a key is repeated, and so is a read, which is
// harmless.
//
// # Broadcast and batch
//
// A call returns an error unless it did everything asked, and
// [Client.Broadcast] and [Batch.Send] are no exception - though they do many
// things in one request, and each of those can fail on its own. When Centrifugo
// carried out the request and one or more parts failed, up to all, the error is
// a [*BroadcastFailedChannelsError] or a [*BatchFailedCommandsError] listing
// them. Any other error means the request failed as a whole: no part got a
// reply. After a timeout or a network error Centrifugo may still have carried
// it out, so retry with the same IdempotencyKey.
//
// Every part always has an outcome, whatever the error: a broadcast's result
// holds one entry per channel, and every command of a batch its [Pending].
// When the request failed as a whole, each part's error is that error.
//
// Most callers need no more than err != nil: a broadcast retried with the
// same IdempotencyKey is not published twice into any channel. To go on
// despite some failed parts, ask the result whether Centrifugo replied, then
// read each part:
//
//	res, err := client.Broadcast(ctx, req)
//	if !res.Replied {
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
// gocent depends on no tracing library: every request goes through the
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
// caller's context: its span starts a trace of its own. Config.BatchContext
// makes its context from the callers' - with OpenTelemetry, a span with a
// link to each caller's span:
//
//	BatchContext: func(ctx context.Context, callers []context.Context) context.Context {
//		links := make([]trace.Link, 0, len(callers))
//		for _, c := range callers {
//			links = append(links, trace.LinkFromContext(c))
//		}
//		ctx, _ = tracer.Start(ctx, "gocent batch", trace.WithLinks(links...))
//		return ctx
//	},
//
// Config.OnRequest is told of every request - its method, how many commands
// it carried and how many failed, whether it was an automatic batch, its
// duration, status and error - for metrics without a transport of your own.
//
// A transport of your own works the same way. It must not modify the
// request it is given - net/http's RoundTripper contract - but clone it
// first, as otelhttp does.
//
// # Async consumers
//
// Instead of calling Centrifugo, an application can leave a command for its
// async consumers to run - in a PostgreSQL outbox table, a Kafka topic, a
// Redis stream - in the same transaction as the change it reports.
// [NewCommand] makes one from any request, checked as a call would be: a
// command Centrifugo could not run fails here, not in a consumer which would
// drop it.
//
//	cmd, err := gocent.NewCommand(gocent.PublishRequest{Channel: "news", Data: data})
//	if err != nil {
//		return err
//	}
//	_, err = tx.ExecContext(ctx,
//		"INSERT INTO centrifugo_outbox (method, payload, partition) VALUES ($1, $2, 0)",
//		cmd.Method, cmd.Payload)
//
// A [Command] encoded as JSON is the {"method": ..., "payload": ...} message
// other consumers take, Kafka's say. See [Command] for keeping a channel's
// commands in order.
package gocent
