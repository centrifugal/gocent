// Package token issues the tokens Centrifugo authenticates clients with:
// connection tokens and subscription tokens, signed JWTs with Centrifugo's
// claims. See https://centrifugal.dev/docs/server/authentication.
//
// [Connection] and [Subscription] sign them with a [Key] - [HMAC], [RSA],
// [ECDSA] or [Ed25519] - matching how Centrifugo verifies tokens:
//
//	tok, err := token.Connection(token.HMAC(secret), token.ConnectionClaims{
//		User:      "42",
//		ExpiresAt: time.Now().Add(time.Hour),
//	})
//
// [Handlers] serves the endpoints the getToken callbacks of client SDKs call,
// around functions of the application's which decide who gets a token:
//
//	h := token.Handlers{OnError: logError}
//	mux.Handle("/centrifugo/connection_token", h.Connection(
//		func(r *http.Request) (string, error) {
//			user, ok := sessionUser(r)
//			if !ok {
//				return "", token.ErrUnauthorized
//			}
//			return token.Connection(key, token.ConnectionClaims{User: user, ExpiresAt: time.Now().Add(time.Hour)})
//		}))
//
// The two are independent: sign tokens anywhere, or serve tokens signed
// elsewhere.
//
// With asymmetric keys, [JWKS] and [Handlers.JWKS] publish the public keys
// for Centrifugo's client.token.jwks_public_endpoint, which is how keys
// rotate without restarting Centrifugo: publish the new key next to the old
// one, sign with the new one, drop the old one once its tokens expired.
package token
