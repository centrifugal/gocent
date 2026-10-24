package token

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The keys come out as JWKs Centrifugo reads - each with its kid, use "sig"
// and alg - and decode back to the same public keys.
func TestJWKS(t *testing.T) {
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	set, err := JWKS(RSA(rk).WithKeyID("r"), ECDSA(ek).WithKeyID("e"), Ed25519(priv).WithKeyID("o"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Keys []map[string]string `json:"keys"`
	}
	if err = json.Unmarshal(set, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Keys) != 3 {
		t.Fatalf("%d keys", len(got.Keys))
	}
	dec := func(s string) []byte {
		b, decErr := base64.RawURLEncoding.DecodeString(s)
		if decErr != nil {
			t.Fatal(decErr)
		}
		return b
	}
	r, e, o := got.Keys[0], got.Keys[1], got.Keys[2]
	for _, k := range got.Keys {
		if k["use"] != "sig" {
			t.Errorf("key %v without use sig", k)
		}
	}
	if r["kty"] != "RSA" || r["kid"] != "r" || r["alg"] != "RS256" ||
		new(big.Int).SetBytes(dec(r["n"])).Cmp(rk.N) != 0 || new(big.Int).SetBytes(dec(r["e"])).Int64() != int64(rk.E) {
		t.Errorf("RSA %v", r)
	}
	ecPoint, err := ek.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if e["kty"] != "EC" || e["kid"] != "e" || e["alg"] != "ES384" || e["crv"] != "P-384" ||
		len(dec(e["x"])) != 48 || !bytes.Equal(append(append([]byte{4}, dec(e["x"])...), dec(e["y"])...), ecPoint) {
		t.Errorf("EC %v", e)
	}
	if o["kty"] != "OKP" || o["kid"] != "o" || o["alg"] != "EdDSA" || o["crv"] != "Ed25519" || !pub.Equal(ed25519.PublicKey(dec(o["x"]))) {
		t.Errorf("OKP %v", o)
	}

	// A token the published RSA key verifies.
	tok, err := Connection(RSA(rk).WithKeyID("r"), ConnectionClaims{User: "42", NoExpiry: true})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	published := &rsa.PublicKey{N: new(big.Int).SetBytes(dec(r["n"])), E: int(new(big.Int).SetBytes(dec(r["e"])).Int64())}
	if err := rsa.VerifyPKCS1v15(published, crypto.SHA256, sum[:], dec(parts[2])); err != nil {
		t.Errorf("the published key does not verify the token: %v", err)
	}
}

func TestJWKSRefuses(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p224, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ok := Ed25519(priv).WithKeyID("a")
	for name, keys := range map[string][]Key{
		"no keys":           nil,
		"HMAC":              {HMAC([]byte("s")).WithKeyID("h")},
		"no key ID":         {Ed25519(priv)},
		"same key ID":       {ok, ok},
		"invalid Ed25519":   {Ed25519(ed25519.PrivateKey{1}).WithKeyID("b")},
		"unsupported curve": {ECDSA(p224).WithKeyID("c")},
		"nil RSA":           {RSA(nil).WithKeyID("d")},
		"zero key":          {Key{}.WithKeyID("z")},
	} {
		if _, err := JWKS(keys...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestJWKSHandler(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := Ed25519(priv).WithKeyID("a")
	set, err := JWKS(key)
	if err != nil {
		t.Fatal(err)
	}
	h := Handlers{}.JWKS(key)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), method, "/", http.NoBody))
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "public, max-age=300" {
			t.Errorf("%s: %d %v", method, w.Code, w.Header())
		}
		if want := map[string]string{http.MethodGet: string(set), http.MethodHead: ""}[method]; w.Body.String() != want {
			t.Errorf("%s: body %q", method, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", http.NoBody))
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d %v", w.Code, w.Header())
	}
	defer func() {
		if recover() == nil {
			t.Error("no panic for an HMAC key")
		}
	}()
	Handlers{}.JWKS(HMAC([]byte("s")).WithKeyID("h"))
}
