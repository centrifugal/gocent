package token_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json/jsontext"
	"log"
	"log/slog"
	"net/http"
	"time"

	"github.com/centrifugal/gocent/v4"
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

// Keys published for Centrifugo's JWKS endpoint, mid-rotation: both keys are
// published, and the new one signs - once every instance of the application
// publishes it. The old one is dropped once the tokens it signed have
// expired.
func ExampleJWKS() {
	_, previousKey, _ := ed25519.GenerateKey(nil)
	_, currentKey, _ := ed25519.GenerateKey(nil)
	previous := token.Ed25519(previousKey).WithKeyID("2026-04")
	current := token.Ed25519(currentKey).WithKeyID("2026-10")

	mux := http.NewServeMux()
	// Centrifugo: "client": {"token": {"jwks_public_endpoint": "http://backend:3000/centrifugo/jwks"}}
	mux.Handle("/centrifugo/jwks", token.Handlers{}.JWKS(current, previous))
	mux.Handle("/centrifugo/connection_token", token.Handlers{}.Connection(
		func(r *http.Request) (string, error) {
			user, ok := sessionUser(r)
			if !ok {
				return "", token.ErrUnauthorized
			}
			return token.Connection(current, token.ConnectionClaims{User: user, ExpiresAt: time.Now().Add(time.Hour)})
		}))
	log.Fatal(http.ListenAndServe("127.0.0.1:3000", mux)) //nolint:gosec // an example
}

// A gocent.Client calling the server API of Centrifugo PRO with tokens from
// APITokens. The API keys are published at an endpoint of their own, never
// the one of client.token.jwks_public_endpoint, and Centrifugo requires an
// audience and a scope only API tokens carry: otherwise a user's connection
// token would pass as an API token.
func ExampleAPITokens() {
	_, apiPrivate, _ := ed25519.GenerateKey(nil)
	apiKey := token.Ed25519(apiPrivate).WithKeyID("api-2026-10")

	// Centrifugo PRO: "http_api": {"jwks": {"enabled": true,
	//   "endpoint": "http://backend:3000/centrifugo/api_jwks",
	//   "audience": "centrifugo-api", "scope": "centrifugo:api"}}
	mux := http.NewServeMux()
	mux.Handle("/centrifugo/api_jwks", token.Handlers{}.JWKS(apiKey))
	go func() { log.Fatal(http.ListenAndServe("127.0.0.1:3000", mux)) }() //nolint:gosec // an example

	apiTokens := &token.APITokens{
		Key:    apiKey,
		Claims: token.APIClaims{Audience: "centrifugo-api", Scope: []string{"centrifugo:api"}},
	}
	client, err := gocent.New(gocent.Config{
		APIEndpoint:     "http://centrifugo:8000/api",
		BearerTokenFunc: apiTokens.Token,
	})
	if err != nil {
		log.Fatal(err)
	}
	_, err = client.Publish(context.Background(), gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{"text":"hello"}`)})
	if err != nil {
		log.Fatal(err)
	}
}
