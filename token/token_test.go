package token

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
)

// parts splits a token into its decoded header, claims and signature, and
// the signed input.
func parts(t *testing.T, tok string) (header, claims map[string]any, raw jsontext.Value, sig []byte, signed string) {
	t.Helper()
	p := strings.Split(tok, ".")
	if len(p) != 3 {
		t.Fatalf("token %q", tok)
	}
	dec := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if err := json.Unmarshal(dec(p[0]), &header); err != nil {
		t.Fatal(err)
	}
	raw = dec(p[1])
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return header, claims, raw, dec(p[2]), p[0] + "." + p[1]
}

// The claims are exactly what Centrifugo reads, with its names.
func TestClaims(t *testing.T) {
	iat := time.Unix(1700000000, 0)
	exp := time.Unix(1700003600, 0)
	conn, err := Connection(HMAC([]byte("k")), ConnectionClaims{
		User: "42", ExpiresAt: exp, ExpireAt: &time.Time{}, IssuedAt: iat, ID: "t1", Audience: "centrifugo", Issuer: "app",
		Info: jsontext.Value(`{"name":"Alice"}`), B64Info: "aW5mbw==", Meta: jsontext.Value(`{"m":1}`),
		Subs: map[string]SubscribeOptions{"chat": {Data: jsontext.Value(`{"hi":1}`), Override: gocent.SubscribeOptionOverride{Presence: gocent.Bool(false)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, raw, _, _ := parts(t, conn)
	want := `{"sub":"42","exp":1700003600,"iat":1700000000,"jti":"t1","aud":"centrifugo","iss":"app","expire_at":0,` +
		`"info":{"name":"Alice"},"b64info":"aW5mbw==","subs":{"chat":{"data":{"hi":1},"override":{"presence":{}}}},"meta":{"m":1}}`
	if string(raw) != want {
		t.Errorf("connection claims\n%s\nwant\n%s", raw, want)
	}

	at := time.Unix(1700007200, 0)
	sub, err := Subscription(HMAC([]byte("k")), SubscriptionClaims{
		User: "42", Channel: "chat:1", ExpiresAt: exp, ExpireAt: &at, IssuedAt: iat,
		Info: jsontext.Value(`{"role":"admin"}`), Data: jsontext.Value(`{"d":1}`),
		Override: gocent.SubscribeOptionOverride{JoinLeave: gocent.Bool(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, raw, _, _ = parts(t, sub)
	want = `{"sub":"42","exp":1700003600,"iat":1700000000,"channel":"chat:1","expire_at":1700007200,` +
		`"info":{"role":"admin"},"data":{"d":1},"override":{"join_leave":{"value":true}}}`
	if string(raw) != want {
		t.Errorf("subscription claims\n%s\nwant\n%s", raw, want)
	}
}

// Left unset, a claim is left out - but the user, empty for an anonymous
// connection, is always there, and the time of issue is now.
func TestClaimDefaults(t *testing.T) {
	before := time.Now().Unix()
	tok, err := Connection(HMAC([]byte("k")), ConnectionClaims{})
	if err != nil {
		t.Fatal(err)
	}
	_, claims, _, _, _ := parts(t, tok)
	if len(claims) != 2 || claims["sub"] != "" {
		t.Fatalf("claims %v, want only sub and iat", claims)
	}
	if iat := int64(claims["iat"].(float64)); iat < before || iat > time.Now().Unix() {
		t.Errorf("iat %d, want now", iat)
	}
}

func TestErrors(t *testing.T) {
	k := HMAC([]byte("k"))
	for name, err := range map[string]error{
		"channels and subs": func() error {
			_, err := Connection(k, ConnectionClaims{Channels: []string{"a"}, Subs: map[string]SubscribeOptions{"b": {}}})
			return err
		}(),
		"subscription without channel": func() error { _, err := Subscription(k, SubscriptionClaims{User: "42"}); return err }(),
		"zero key":                     func() error { _, err := Connection(Key{}, ConnectionClaims{}); return err }(),
		"empty secret":                 func() error { _, err := Connection(HMAC(nil), ConnectionClaims{}); return err }(),
		"invalid info": func() error {
			_, err := Connection(k, ConnectionClaims{Info: jsontext.Value(`{not json`)})
			return err
		}(),
		"nil RSA":   func() error { _, err := Connection(RSA(nil), ConnectionClaims{}); return err }(),
		"nil ECDSA": func() error { _, err := Connection(ECDSA(nil), ConnectionClaims{}); return err }(),
		"P-224": func() error {
			ek, _ := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
			_, err := Connection(ECDSA(ek), ConnectionClaims{})
			return err
		}(),
		"bad Ed25519": func() error { _, err := Connection(Ed25519(ed25519.PrivateKey{1}), ConnectionClaims{}); return err }(),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// Each key signs with its algorithm, as the standard library verifies it.
func TestSignatures(t *testing.T) {
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifyES := func(curve elliptic.Curve, hash func([]byte) []byte) func(*testing.T, Key) (Key, func([]byte, []byte) bool) {
		return func(t *testing.T, _ Key) (Key, func([]byte, []byte) bool) {
			ek, err := ecdsa.GenerateKey(curve, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			size := (curve.Params().BitSize + 7) / 8
			return ECDSA(ek), func(data, sig []byte) bool {
				if len(sig) != 2*size {
					return false
				}
				r, s := new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:])
				return ecdsa.Verify(&ek.PublicKey, hash(data), r, s)
			}
		}
	}
	sum256 := func(b []byte) []byte { s := sha256.Sum256(b); return s[:] }
	cases := []struct {
		alg  string
		make func(*testing.T, Key) (Key, func(data, sig []byte) bool)
	}{
		{"HS256", func(*testing.T, Key) (Key, func([]byte, []byte) bool) {
			return HMAC([]byte("secret")), func(data, sig []byte) bool {
				mac := hmac.New(sha256.New, []byte("secret"))
				mac.Write(data)
				return hmac.Equal(mac.Sum(nil), sig)
			}
		}},
		{"RS256", func(*testing.T, Key) (Key, func([]byte, []byte) bool) {
			return RSA(rk), func(data, sig []byte) bool {
				return rsa.VerifyPKCS1v15(&rk.PublicKey, crypto.SHA256, sum256(data), sig) == nil
			}
		}},
		{"ES256", verifyES(elliptic.P256(), sum256)},
		{"ES384", verifyES(elliptic.P384(), func(b []byte) []byte { s := sha512.Sum384(b); return s[:] })},
		{"ES512", verifyES(elliptic.P521(), func(b []byte) []byte { s := sha512.Sum512(b); return s[:] })},
		{"EdDSA", func(*testing.T, Key) (Key, func([]byte, []byte) bool) {
			return Ed25519(priv), func(data, sig []byte) bool { return ed25519.Verify(pub, data, sig) }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.alg, func(t *testing.T) {
			key, verify := tc.make(t, Key{})
			tok, err := Subscription(key.WithKeyID("key-1"), SubscriptionClaims{User: "42", Channel: "c"})
			if err != nil {
				t.Fatal(err)
			}
			header, _, _, sig, signed := parts(t, tok)
			if header["alg"] != tc.alg || header["typ"] != "JWT" || header["kid"] != "key-1" {
				t.Errorf("header %v", header)
			}
			if !verify([]byte(signed), sig) {
				t.Error("signature does not verify")
			}
			if verify([]byte(signed+"x"), sig) {
				t.Error("signature verifies other data")
			}
		})
	}
}

// Centrifugo PRO's claims, with its names.
func TestProClaims(t *testing.T) {
	iat := time.Unix(1700000000, 0)
	filter := gocent.FilterNode{Key: "role", Cmp: "eq", Val: "admin"}
	conn, err := Connection(HMAC([]byte("k")), ConnectionClaims{
		User: "42", IssuedAt: iat,
		Caps:   []ChannelsCapability{{Channels: []string{"user_*"}, Allow: []string{"sub", "pub"}, Match: "wildcard"}},
		Labels: map[string]string{"region": "eu"},
		Subs:   map[string]SubscribeOptions{"feed": {ServerTagsFilter: filter}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, raw, _, _ := parts(t, conn)
	want := `{"sub":"42","iat":1700000000,"subs":{"feed":{"server_tags_filter":{"key":"role","cmp":"eq","val":"admin"}}},` +
		`"caps":[{"channels":["user_*"],"allow":["sub","pub"],"match":"wildcard"}],"labels":{"region":"eu"}}`
	if string(raw) != want {
		t.Errorf("connection claims\n%s\nwant\n%s", raw, want)
	}
	sub, err := Subscription(HMAC([]byte("k")), SubscriptionClaims{User: "42", Channel: "c", IssuedAt: iat, Allow: []string{"pub"}, ServerTagsFilter: filter})
	if err != nil {
		t.Fatal(err)
	}
	_, _, raw, _, _ = parts(t, sub)
	want = `{"sub":"42","iat":1700000000,"channel":"c","allow":["pub"],"server_tags_filter":{"key":"role","cmp":"eq","val":"admin"}}`
	if string(raw) != want {
		t.Errorf("subscription claims\n%s\nwant\n%s", raw, want)
	}
}
