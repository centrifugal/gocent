package token_test

import (
	"context"
	"encoding/json/jsontext"
	"log"
	"log/slog"
	"net/http"
	"time"

	"github.com/centrifugal/gocent/v4/token"
)

// The endpoints the getToken callbacks of client SDKs call: a connection
// token for the session's user, and subscription tokens for the channels the
// user may read.
func Example() {
	key := token.HMAC([]byte("client.token.hmac_secret_key"))
	h := token.Handlers{OnError: func(r *http.Request, err error) {
		slog.Error("centrifugo token", "path", r.URL.Path, "err", err)
	}}
	mux := http.NewServeMux()
	mux.Handle("/centrifugo/connection_token", h.Connection(
		func(r *http.Request) (string, error) {
			user, ok := sessionUser(r)
			if !ok {
				return "", token.ErrUnauthorized // the client stops asking
			}
			return token.Connection(key, token.ConnectionClaims{
				User:      user,
				ExpiresAt: time.Now().Add(time.Hour), // then the client asks again
				Info:      jsontext.Value(`{"name":"Alice"}`),
			})
		}))
	mux.Handle("/centrifugo/subscription_token", h.Subscription(
		func(r *http.Request, channel string) (string, error) {
			user, ok := sessionUser(r)
			if !ok || !canRead(r.Context(), user, channel) {
				return "", token.ErrUnauthorized
			}
			return token.Subscription(key, token.SubscriptionClaims{
				User:      user,
				Channel:   channel,
				ExpiresAt: time.Now().Add(time.Hour),
			})
		}))
	log.Fatal(http.ListenAndServe("127.0.0.1:3000", mux)) //nolint:gosec // an example
}

func sessionUser(r *http.Request) (string, bool) {
	c, err := r.Cookie("session")
	if err != nil {
		return "", false
	}
	return c.Value, true
}

func canRead(context.Context, string, string) bool { return true }
