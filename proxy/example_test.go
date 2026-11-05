package proxy_test

import (
	"context"
	"encoding/json/jsontext"
	"log"
	"log/slog"
	"net/http"

	"github.com/centrifugal/gocent/v4/proxy"
)

// A backend serving Centrifugo's connect and subscribe proxies: connections
// are authenticated from the session cookie Centrifugo passes on, and
// subscriptions checked against the user.
func Example() {
	h := proxy.Handlers{
		// Centrifugo only logs the status of a failed request: the cause is
		// seen here.
		OnError: func(r *http.Request, err error) {
			slog.Error("centrifugo proxy", "path", r.URL.Path, "err", err)
		},
	}
	mux := http.NewServeMux()
	mux.Handle("/centrifugo/connect", h.Connect(
		func(r *http.Request, req proxy.ConnectRequest) (proxy.ConnectResult, error) {
			user, ok := sessionUser(r)
			if !ok {
				// Closes the connection; 4500-4999 tells the client not to reconnect.
				return proxy.ConnectResult{}, &proxy.Disconnect{Code: 4501, Reason: "unauthorized"}
			}
			return proxy.ConnectResult{
				User: user,
				Info: jsontext.Value(`{"name":"Alice"}`),
				// Subscribed by the server, without the client asking.
				Channels: []string{"user:" + user},
			}, nil
		}))
	mux.Handle("/centrifugo/subscribe", h.Subscribe(
		func(r *http.Request, req proxy.SubscribeRequest) (proxy.SubscribeResult, error) {
			if !canRead(r.Context(), req.User, req.Channel) {
				// Rejects the subscription; the client receives the error.
				return proxy.SubscribeResult{}, &proxy.Error{Code: 403, Message: "permission denied"}
			}
			return proxy.SubscribeResult{
				Override: proxy.SubscribeOptionOverride{Presence: proxy.Bool(true)},
			}, nil
		}))
	// Reachable by Centrifugo only: anyone able to call these endpoints could
	// claim to be any user.
	log.Fatal(http.ListenAndServe("127.0.0.1:3000", mux)) //nolint:gosec // an example
}

func sessionUser(r *http.Request) (string, bool) {
	c, err := r.Cookie("session")
	if err != nil {
		return "", false
	}
	return c.Value, true // look the session up, in practice
}

func canRead(context.Context, string, string) bool { return true }
