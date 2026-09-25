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
// # Broadcast and batch
//
// Two methods do many things in one request, and each of those things can
// fail on its own. [Client.Broadcast] publishes the same data into many
// channels; a [Batch] sends many commands at once. For both, the rule is the
// same: when one or more parts failed - up to all of them - the method
// returns an error, a [*BroadcastError] or a [*BatchError], together with the
// complete result. Checking err is always enough to notice a failure, and the
// result holds every part's outcome: its result, or its own error. Any other
// error means the request failed as a whole and nothing was done. [IsPartial]
// tells the two apart:
//
//	res, err := client.Broadcast(ctx, req)
//	if err != nil && !gocent.IsPartial(err) {
//		return err // nothing was published
//	}
//	for _, ch := range res.Channels { ... } // every channel's outcome
//
// # Automatic batching
//
// With [AutoBatch] set, calls made concurrently are sent together once enough
// of them are in flight, which cuts the requests Centrifugo handles - and its
// CPU - under load, with no change to the code making the calls and no delay
// when the client is not busy.
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
