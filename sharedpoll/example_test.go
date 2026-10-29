package sharedpoll_test

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"time"

	"github.com/centrifugal/gocent/v4/sharedpoll"
)

// The endpoint a shared poll subscription's getSignature calls: the user may
// track the posts they can read, for an hour.
func Example() {
	secret := []byte("shared_poll.hmac_secret_key")
	h := sharedpoll.Handlers{OnError: func(r *http.Request, err error) {
		slog.Error("shared poll signature", "err", err)
	}}
	mux := http.NewServeMux()
	mux.Handle("/centrifugo/shared_poll_signature", h.Signature(
		func(r *http.Request, channel string, keys []string) ([]string, string, error) {
			user, ok := sessionUser(r)
			if !ok {
				// centrifuge-js ends the subscription.
				return nil, "", sharedpoll.ErrUnauthorized
			}
			var allowed []string // in the order asked; the others are revoked
			for _, k := range keys {
				if canRead(r.Context(), user, k) {
					allowed = append(allowed, k)
				}
			}
			sig, err := sharedpoll.Sign(secret, user, channel, allowed, time.Now().Add(time.Hour))
			return allowed, sig, err
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
