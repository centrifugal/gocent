//go:build integration

package gocent_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json/jsontext"
	"errors"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
	"github.com/centrifugal/gocent/v4/token"
)

// Tokens from token.APITokens authenticate API calls to Centrifugo PRO, which
// verifies them with the keys served here, at the endpoint of
// testdata/centrifugo-pro.compose.yml: port 3101 of the host, or
// GOCENT_TEST_API_JWKS_LISTEN. Tokens without the audience and scope it
// requires are refused, as are tokens signed with a key not published.
func TestIntegrationProAPITokens(t *testing.T) {
	proClient(t, gocent.AutoBatch{})
	// The same keys on every run, from fixed seeds: Centrifugo caches keys
	// by ID, and fetches the JWKS for an ID it does not know at most every
	// few seconds - a new key under a known ID, or new IDs run after run,
	// would be refused.
	newKey := func(kid string, seed byte) token.Key {
		return token.Ed25519(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))).WithKeyID(kid)
	}
	apiKey := newKey("gocent-api-test", 1)

	listen := os.Getenv("GOCENT_TEST_API_JWKS_LISTEN")
	if listen == "" {
		listen = ":3101"
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api_jwks", token.Handlers{}.JWKS(apiKey))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	bearer := func(f func(context.Context) (string, error)) *gocent.Client {
		c, newErr := gocent.New(gocent.Config{APIEndpoint: testAddr(), BearerTokenFunc: f})
		if newErr != nil {
			t.Fatal(newErr)
		}
		return c
	}
	publish := func(c *gocent.Client) error {
		_, pubErr := c.Publish(t.Context(), gocent.PublishRequest{Channel: uniqueChannel(t), Data: jsontext.Value(`{}`)})
		return pubErr
	}

	tokens := &token.APITokens{
		Key:    apiKey,
		Claims: token.APIClaims{Audience: "centrifugo-api", Scope: []string{"centrifugo:api"}},
	}
	if err = publish(bearer(tokens.Token)); err != nil {
		t.Fatalf("publish with an API token: %v", err)
	}

	exp := time.Now().Add(time.Minute)
	refused := map[string]token.APIClaims{
		"without the scope":    {Audience: "centrifugo-api", ExpiresAt: exp},
		"without the audience": {Scope: []string{"centrifugo:api"}, ExpiresAt: exp},
	}
	for name, claims := range refused {
		tok, signErr := token.API(apiKey, claims)
		if signErr != nil {
			t.Fatal(signErr)
		}
		pubErr := publish(bearer(func(context.Context) (string, error) { return tok, nil }))
		if !errors.Is(pubErr, gocent.ErrUnauthorized) {
			t.Errorf("token %s: %v, want ErrUnauthorized", name, pubErr)
		}
	}

	// A connection token signed with a published key: no audience or scope.
	conn, err := token.Connection(apiKey, token.ConnectionClaims{User: "42", ExpiresAt: exp})
	if err != nil {
		t.Fatal(err)
	}
	if err = publish(bearer(func(context.Context) (string, error) { return conn, nil })); !errors.Is(err, gocent.ErrUnauthorized) {
		t.Errorf("connection token: %v, want ErrUnauthorized", err)
	}

	unpublished := &token.APITokens{Key: newKey("gocent-unpublished", 2), Claims: tokens.Claims}
	if err = publish(bearer(unpublished.Token)); !errors.Is(err, gocent.ErrUnauthorized) {
		t.Errorf("token of an unpublished key: %v, want ErrUnauthorized", err)
	}
}
