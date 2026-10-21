package token

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math/big"
	"net/http"
)

// jwk is a public key as JSON Web Key (RFC 7517), the fields Centrifugo reads.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	// Use must be "sig": Centrifugo skips keys without it.
	Use string `json:"use"`
	Alg string `json:"alg"`
	Crv string `json:"crv,omitzero"`
	N   string `json:"n,omitzero"`
	E   string `json:"e,omitzero"`
	X   string `json:"x,omitzero"`
	Y   string `json:"y,omitzero"`
}

// JWKS returns the public keys of keys as a JSON Web Key Set, which
// Centrifugo fetches from client.token.jwks_public_endpoint to verify tokens
// by their "kid". Each key needs an ID - see [Key.WithKeyID] - unique in the
// set. HMAC keys have no public key to publish: JWKS refuses them, as it
// refuses a set without keys.
//
// A set is how keys rotate without restarting Centrifugo: publish the new key
// next to the old one, sign with the new one, and drop the old one once the
// tokens it signed have expired. Centrifugo fetches a key it does not know when
// a token names it, and keeps keys for an hour.
func JWKS(keys ...Key) (jsontext.Value, error) {
	if len(keys) == 0 {
		return nil, errors.New("token: a JWKS without keys")
	}
	set := struct {
		Keys []jwk `json:"keys"`
	}{Keys: make([]jwk, 0, len(keys))}
	seen := make(map[string]bool, len(keys))
	enc := base64.RawURLEncoding
	for i, k := range keys {
		switch {
		case k.public == nil:
			return nil, fmt.Errorf("token: JWKS key #%d has no public key to publish: an HMAC key, or a missing, invalid or unsupported one", i)
		case k.kid == "":
			return nil, fmt.Errorf("token: JWKS key #%d has no key ID: set one with WithKeyID", i)
		case seen[k.kid]:
			return nil, fmt.Errorf("token: JWKS key ID %q used twice", k.kid)
		}
		seen[k.kid] = true
		j := jwk{Kid: k.kid, Use: "sig", Alg: k.alg}
		switch pub := k.public.(type) {
		case *rsa.PublicKey:
			j.Kty = "RSA"
			j.N = enc.EncodeToString(pub.N.Bytes())
			j.E = enc.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
		case *ecdsa.PublicKey:
			point, err := pub.Bytes() // 0x04, then X and Y of fixed size
			if err != nil {
				return nil, fmt.Errorf("token: JWKS key %q: %w", k.kid, err)
			}
			size := (len(point) - 1) / 2
			j.Kty = "EC"
			j.Crv = pub.Curve.Params().Name
			j.X = enc.EncodeToString(point[1 : 1+size])
			j.Y = enc.EncodeToString(point[1+size:])
		case ed25519.PublicKey:
			j.Kty = "OKP"
			j.Crv = "Ed25519"
			j.X = enc.EncodeToString(pub)
		default:
			return nil, fmt.Errorf("token: JWKS key %q: unsupported key type %T", k.kid, pub)
		}
		set.Keys = append(set.Keys, j)
	}
	b, err := json.Marshal(set)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	return b, nil
}

// JWKS serves the JSON Web Key Set of keys - see the function [JWKS] - at
// the URL configured as Centrifugo's client.token.jwks_public_endpoint. It
// answers GET and HEAD requests, cacheable for five minutes. It panics when
// the function JWKS returns an error: the keys are the application's
// configuration.
func (h Handlers) JWKS(keys ...Key) http.Handler {
	set, err := JWKS(keys...)
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "only GET and HEAD are allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		if r.Method == http.MethodGet {
			_, _ = w.Write(set)
		}
	})
}
