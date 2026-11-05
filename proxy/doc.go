// Package proxy implements the application side of Centrifugo's HTTP proxy:
// the requests Centrifugo sends to the backend when a client connects,
// subscribes, publishes or calls an RPC, the results the backend answers
// with, and - from [Handlers] - an [http.Handler] for each proxy type which
// decodes the request, calls a function and encodes its answer.
//
// The types are generated from Centrifugo's proxy.proto, and keep its field
// names: SubscribeRequest.User is the request's "user".
//
//	h := proxy.Handlers{OnError: logProxyError}
//	mux := http.NewServeMux()
//	mux.Handle("/centrifugo/connect", h.Connect(
//		func(r *http.Request, req proxy.ConnectRequest) (proxy.ConnectResult, error) {
//			user, ok := sessionUser(r) // the client's cookies, when Centrifugo proxies them
//			if !ok {
//				return proxy.ConnectResult{}, &proxy.Disconnect{Code: 4501, Reason: "unauthorized"}
//			}
//			return proxy.ConnectResult{User: user}, nil
//		}))
//	mux.Handle("/centrifugo/subscribe", h.Subscribe(
//		func(r *http.Request, req proxy.SubscribeRequest) (proxy.SubscribeResult, error) {
//			if !canRead(r.Context(), req.User, req.Channel) {
//				return proxy.SubscribeResult{}, &proxy.Error{Code: 403, Message: "permission denied"}
//			}
//			return proxy.SubscribeResult{}, nil
//		}))
//
// and in Centrifugo's configuration, for example:
//
//	"client": {
//	  "proxy": {
//	    "connect": {"enabled": true, "endpoint": "http://backend:3000/centrifugo/connect", "http_headers": ["Cookie"]}
//	  }
//	}
//
// See https://centrifugal.dev/docs/server/proxy for every proxy type and its
// configuration.
//
// # Answering
//
// A function returns a result to accept the request, or an error:
//
//   - A *[Error] rejects the request with that error, which the client
//     receives: use codes in [400, 1999]. The connect, subscribe, publish,
//     RPC, map publish and map remove proxies pass it to the client; the
//     shared poll refresh and channel state proxies take it as a failure.
//   - A *[Disconnect] closes the client's connection with that code: use
//     codes in [4000, 4999] - [4500, 4999] to tell the client not to
//     reconnect. Only the proxies which pass an [Error] to the client honour
//     it.
//   - Any other error, or one a proxy cannot carry, fails the request with
//     HTTP status 500, and is passed to [Handlers.OnError] for logging. Centrifugo then answers the client with its temporary
//     internal error, unless the proxy's status_to_code_transforms say
//     otherwise; for refresh and sub refresh it keeps the connection or
//     subscription for another minute and asks again.
//
// Errors are found with errors.AsType, so a wrapped *Error counts too. The
// refresh and sub refresh proxies end a connection or subscription through
// their result's Expired, not an error.
//
// A handler always answers with exactly one of result, error and disconnect.
// Over raw HTTP, a response with none of them would accept the request as the
// client sent it - a handler cannot answer that way by mistake.
//
// # Requests
//
// The *http.Request a function receives is Centrifugo's request to the
// backend, its body already read. Its headers are those the proxy is
// configured to pass on from the client's connection - http_headers - and
// the static headers configured for it. Its context ends when Centrifugo stops
// waiting: after the proxy's timeout, one second by default.
//
// Payloads - data, info, meta - are JSON, kept raw as [jsontext.Value]. A
// proxy with binary_encoding enabled sends them in base64 in the B64 fields
// instead, B64Data say, and its Encoding is "binary"; a result may answer
// with either field.
//
// # Security
//
// Centrifugo trusts what a proxy endpoint answers, and the endpoint trusts
// what Centrifugo sends: a SubscribeRequest's User is the user Centrifugo
// authenticated. Anyone else able to reach the endpoint could claim any user.
// Keep proxy endpoints reachable by Centrifugo only - on an internal network,
// or behind a check of a secret Centrifugo sends as a static header.
//
// Centrifugo's GRPC proxy is not covered: generate GRPC code from
// proxy.proto for it.
package proxy
