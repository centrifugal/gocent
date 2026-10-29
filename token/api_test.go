package token

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"
)

func apiKey(t *testing.T, kid string) Key {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Ed25519(priv).WithKeyID(kid)
}

// The claims are what Centrifugo PRO's http_api.jwks checks, with the key ID
// it finds the key by.
func TestAPIClaims(t *testing.T) {
	tok, err := API(apiKey(t, "api-1"), APIClaims{
		Audience: "centrifugo-api", Scope: []string{"centrifugo:api", "other"}, Issuer: "app", Subject: "billing",
		ExpiresAt: time.Unix(1700000300, 0), IssuedAt: time.Unix(1700000000, 0), ID: "t1",
	})
	if err != nil {
		t.Fatal(err)
	}
	header, _, raw, _, _ := parts(t, tok)
	if header["kid"] != "api-1" || header["alg"] != "EdDSA" {
		t.Errorf("header %v", header)
	}
	want := `{"sub":"billing","exp":1700000300,"iat":1700000000,"jti":"t1","aud":"centrifugo-api","iss":"app","scope":"centrifugo:api other"}`
	if string(raw) != want {
		t.Errorf("claims\n%s\nwant\n%s", raw, want)
	}

	tok, err = API(apiKey(t, "api-1"), APIClaims{Scope: []string{"centrifugo:api"}, ExpiresAt: time.Unix(1700000300, 0), IssuedAt: time.Unix(1700000000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, raw, _, _ = parts(t, tok); string(raw) != `{"sub":"","exp":1700000300,"iat":1700000000,"scope":"centrifugo:api"}` {
		t.Errorf("claims %s", raw)
	}
}

// API refuses what Centrifugo PRO can't verify, and tokens client tokens
// could pass for.
func TestAPIErrors(t *testing.T) {
	key := apiKey(t, "api-1")
	exp := time.Now().Add(time.Minute)
	for name, c := range map[string]struct {
		key    Key
		claims APIClaims
	}{
		"HMAC":                 {HMAC([]byte("k")).WithKeyID("h"), APIClaims{Audience: "a", ExpiresAt: exp}},
		"no key ID":            {apiKey(t, ""), APIClaims{Audience: "a", ExpiresAt: exp}},
		"zero key":             {Key{}, APIClaims{Audience: "a", ExpiresAt: exp}},
		"no audience or scope": {key, APIClaims{Issuer: "app", ExpiresAt: exp}},
		"no expiry":            {key, APIClaims{Audience: "a"}},
		"empty scope":          {key, APIClaims{Scope: []string{""}, ExpiresAt: exp}},
		"scope with a space":   {key, APIClaims{Scope: []string{"a b"}, ExpiresAt: exp}},
	} {
		if _, err := API(c.key, c.claims); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// APITokens reuses a token for half its TTL, then signs a new one with the
// current key.
func TestAPITokens(t *testing.T) {
	keys := []Key{apiKey(t, "k1"), apiKey(t, "k2")}
	current := 0
	s := &APITokens{
		CurrentKey: func() (Key, error) { return keys[current], nil },
		Claims:     APIClaims{Audience: "centrifugo-api"},
		TTL:        time.Hour,
	}
	first, err := s.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, claims, _, _, _ := parts(t, first)
	if exp, iat := claims["exp"].(float64), claims["iat"].(float64); exp-iat != 3600 {
		t.Errorf("exp %v, iat %v", exp, iat)
	}
	current = 1
	if again, _ := s.Token(t.Context()); again != first {
		t.Error("a new token before half the TTL passed")
	}

	s.refreshAt = time.Now() // half the TTL passed
	next, err := s.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if header, _, _, _, _ := parts(t, next); header["kid"] != "k2" {
		t.Errorf("signed with %v, not the current key", header["kid"])
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if tok, err := s.Token(t.Context()); err != nil || tok != next {
				t.Errorf("concurrent Token: %v", err)
			}
		})
	}
	wg.Wait()
}

func TestAPITokensErrors(t *testing.T) {
	failed := errors.New("no key")
	for name, s := range map[string]*APITokens{
		"ExpiresAt in claims": {Key: apiKey(t, "k"), Claims: APIClaims{Audience: "a", ExpiresAt: time.Now()}},
		"IssuedAt in claims":  {Key: apiKey(t, "k"), Claims: APIClaims{Audience: "a", IssuedAt: time.Now()}},
		"negative TTL":        {Key: apiKey(t, "k"), Claims: APIClaims{Audience: "a"}, TTL: -time.Second},
		"no audience":         {Key: apiKey(t, "k")},
		"CurrentKey fails":    {CurrentKey: func() (Key, error) { return Key{}, failed }, Claims: APIClaims{Audience: "a"}},
	} {
		if tok, err := s.Token(t.Context()); err == nil || tok != "" {
			t.Errorf("%s: %q, %v", name, tok, err)
		}
	}
	// Default TTL: five minutes.
	tok, err := (&APITokens{Key: apiKey(t, "k"), Claims: APIClaims{Audience: "a"}}).Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, claims, _, _, _ := parts(t, tok); claims["exp"].(float64)-claims["iat"].(float64) != 300 {
		t.Errorf("claims %v", claims)
	}
}
