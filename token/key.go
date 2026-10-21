package token

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"
)

// Key signs tokens: one of [HMAC], [RSA], [ECDSA] and [Ed25519], matching the
// key Centrifugo verifies with. The zero Key signs nothing: [Connection] and
// [Subscription] return an error.
type Key struct {
	alg  string
	kid  string
	sign func(data []byte) ([]byte, error)
	// public is the key verifying the signatures, for JWKS; nil for HMAC.
	public crypto.PublicKey
}

// HMAC returns a key signing with HS256 and secret, Centrifugo's
// client.token.hmac_secret_key.
func HMAC(secret []byte) Key {
	return Key{alg: "HS256", sign: func(data []byte) ([]byte, error) {
		if len(secret) == 0 {
			return nil, errors.New("token: empty HMAC secret")
		}
		mac := hmac.New(sha256.New, secret)
		mac.Write(data)
		return mac.Sum(nil), nil
	}}
}

// RSA returns a key signing with RS256 and private, whose public key is
// Centrifugo's client.token.rsa_public_key.
func RSA(private *rsa.PrivateKey) Key {
	k := Key{alg: "RS256", sign: func(data []byte) ([]byte, error) {
		if private == nil {
			return nil, errors.New("token: nil RSA key")
		}
		sum := sha256.Sum256(data)
		return rsa.SignPKCS1v15(nil, private, crypto.SHA256, sum[:])
	}}
	if private != nil {
		k.public = &private.PublicKey
	}
	return k
}

// ECDSA returns a key signing with private, whose public key is Centrifugo's
// client.token.ecdsa_public_key: with ES256 for a P-256 key, ES384 for P-384
// and ES512 for P-521.
func ECDSA(private *ecdsa.PrivateKey) Key {
	if private == nil {
		return Key{alg: "ES256", sign: func([]byte) ([]byte, error) { return nil, errors.New("token: nil ECDSA key") }}
	}
	var (
		alg  string
		hash func([]byte) []byte
	)
	switch bits := private.Curve.Params().BitSize; bits {
	case 256:
		alg, hash = "ES256", func(b []byte) []byte { s := sha256.Sum256(b); return s[:] }
	case 384:
		alg, hash = "ES384", func(b []byte) []byte { s := sha512.Sum384(b); return s[:] }
	case 521:
		alg, hash = "ES512", func(b []byte) []byte { s := sha512.Sum512(b); return s[:] }
	default:
		return Key{alg: "ES256", sign: func([]byte) ([]byte, error) {
			return nil, fmt.Errorf("token: ECDSA curve of %d bits, want P-256, P-384 or P-521", bits)
		}}
	}
	size := (private.Curve.Params().BitSize + 7) / 8
	return Key{alg: alg, public: &private.PublicKey, sign: func(data []byte) ([]byte, error) {
		r, s, err := ecdsa.Sign(rand.Reader, private, hash(data))
		if err != nil {
			return nil, fmt.Errorf("token: %w", err)
		}
		// JWS wants r and s as fixed-size big-endian halves, not ASN.1.
		sig := make([]byte, 2*size)
		r.FillBytes(sig[:size])
		s.FillBytes(sig[size:])
		return sig, nil
	}}
}

// Ed25519 returns a key signing with EdDSA and private. Centrifugo verifies
// EdDSA tokens with keys from a JWKS endpoint - publish the key with [JWKS],
// with its ID set by [Key.WithKeyID].
func Ed25519(private ed25519.PrivateKey) Key {
	k := Key{alg: "EdDSA", sign: func(data []byte) ([]byte, error) {
		if len(private) != ed25519.PrivateKeySize {
			return nil, errors.New("token: invalid Ed25519 key")
		}
		return ed25519.Sign(private, data), nil
	}}
	if len(private) == ed25519.PrivateKeySize {
		k.public = private.Public()
	}
	return k
}

// WithKeyID returns the key with kid set as the "kid" of the tokens it signs:
// the ID of its public key in the JWKS Centrifugo verifies with.
func (k Key) WithKeyID(kid string) Key {
	k.kid = kid
	return k
}
