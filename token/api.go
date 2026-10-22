package token

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// APIClaims are the claims of a token authenticating calls to the server API
// of Centrifugo PRO, which verifies it with the keys of http_api.jwks.endpoint
// (grpc_api.jwks.endpoint for the GRPC API). See
// https://centrifugal.dev/docs/pro/server_api_enhancements.
//
// A token passing Centrifugo PRO's checks can call every API method, and
// Centrifugo checks only what it is configured to check: the signature, by
// any key of the JWKS endpoint, and the audience, issuer and scope only when
// http_api.jwks sets them. So the token must carry a claim no other token
// carries - above all the connection and subscription tokens, which users
// hold: otherwise any user can call the API with their connection token.
//
//   - Sign API tokens with their own keys, published at their own JWKS
//     endpoint - never the one of client.token.jwks_public_endpoint.
//   - Set Audience, Scope or both, to values only API tokens carry, and
//     configure http_api.jwks.audience and http_api.jwks.scope to require
//     them. [API] refuses claims with neither.
type APIClaims struct {
	// Audience is the "aud" claim: http_api.jwks.audience.
	Audience string
	// Scope is the "scope" claim, its scopes separated by spaces:
	// http_api.jwks.scope is one of them.
	Scope []string
	// Issuer is the "iss" claim: http_api.jwks.issuer.
	Issuer string
	// Subject is the "sub" claim: who calls the API, for the application's
	// own logs. Centrifugo does not check it.
	Subject string
	// ExpiresAt is when the token expires: required, and best minutes
	// ahead. Centrifugo PRO would accept a token without "exp" for as long as
	// its key is published, so API never signs one.
	ExpiresAt time.Time
	// IssuedAt defaults to now.
	IssuedAt time.Time
	// ID is the "jti" claim.
	ID string
}

type apiWire struct {
	registered
	Scope string `json:"scope,omitzero"`
}

// API returns a token for the server API of Centrifugo PRO with c's claims,
// signed with key - see [APIClaims] for keeping it apart from client tokens,
// and [APITokens] for one to send with every call. Centrifugo PRO finds the
// key to verify it in its JWKS by the key ID, so key must be an asymmetric
// key with an ID - see [Key.WithKeyID] - published by [JWKS].
func API(key Key, c APIClaims) (string, error) {
	switch {
	case key.sign != nil && key.public == nil:
		return "", errors.New("token: an API token needs an RSA, ECDSA or Ed25519 key: Centrifugo PRO verifies it with the public keys of its JWKS endpoint")
	case key.sign != nil && key.kid == "":
		return "", errors.New("token: an API token needs a key ID, by which Centrifugo PRO finds the key in its JWKS: set one with WithKeyID")
	case c.Audience == "" && len(c.Scope) == 0:
		return "", errors.New("token: set Audience or Scope, required by http_api.jwks, so that client tokens signed with the same keys can't call the server API")
	case c.ExpiresAt.IsZero():
		return "", errors.New("token: set ExpiresAt: Centrifugo PRO accepts an API token without one for as long as its key is published")
	}
	for _, s := range c.Scope {
		if s == "" || strings.ContainsAny(s, " \t\n") {
			return "", errors.New("token: a scope is empty or has a space")
		}
	}
	return sign(key, apiWire{
		registered: newRegistered(c.Subject, c.ExpiresAt, c.IssuedAt, c.ID, c.Audience, c.Issuer),
		Scope:      strings.Join(c.Scope, " "),
	})
}

// APITokens issues the tokens a gocent.Client sends to Centrifugo PRO: its
// Token method is the Client's BearerTokenFunc. It signs a token with [API]
// and reuses it for half its lifetime, so a token sent has at least half of
// it left. It is safe for concurrent use; it must not be copied once used.
//
//	tokens := &token.APITokens{
//		Key:    apiKey, // its own key, not the client tokens' - see APIClaims
//		Claims: token.APIClaims{Audience: "centrifugo-api", Scope: []string{"centrifugo:api"}},
//	}
//	client, err := gocent.New(gocent.Config{
//		APIEndpoint:     "https://centrifugo.example.com/api",
//		BearerTokenFunc: tokens.Token,
//	})
type APITokens struct {
	// Key signs the tokens.
	Key Key
	// CurrentKey, when set, returns the key to sign each new token with,
	// instead of Key: the key active now, when keys rotate. A key must be
	// published in the JWKS before it signs: Centrifugo PRO refuses a token
	// whose key it can't find, and gocent does not retry the call.
	CurrentKey func() (Key, error)
	// Claims are each token's claims, but ExpiresAt and IssuedAt, which
	// must be zero: the tokens are issued when needed and expire TTL later.
	Claims APIClaims
	// TTL is how long a token is valid: 5 minutes when zero.
	TTL time.Duration

	mu        sync.Mutex
	token     string
	refreshAt time.Time
}

// Token returns a token valid for at least half of TTL, signing a new one
// when the previous one has less left.
func (s *APITokens) Token(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.token != "" && now.Before(s.refreshAt) {
		return s.token, nil
	}
	if !s.Claims.ExpiresAt.IsZero() || !s.Claims.IssuedAt.IsZero() {
		return "", errors.New("token: APITokens sets ExpiresAt and IssuedAt itself: leave them zero in Claims, and set TTL")
	}
	ttl := s.TTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	if ttl < 0 {
		return "", errors.New("token: APITokens with a negative TTL")
	}
	key := s.Key
	if s.CurrentKey != nil {
		var err error
		if key, err = s.CurrentKey(); err != nil {
			return "", err
		}
	}
	c := s.Claims
	c.IssuedAt = now
	c.ExpiresAt = now.Add(ttl)
	tok, err := API(key, c)
	if err != nil {
		return "", err
	}
	s.token, s.refreshAt = tok, now.Add(ttl/2)
	return tok, nil
}
