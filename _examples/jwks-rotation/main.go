// Command jwks-rotation is a backend issuing Centrifugo tokens signed with
// rotating keys, and publishing their public keys at the endpoint Centrifugo
// fetches them from: client.token.jwks_public_endpoint. See README.md.
//
// Keys are PEM files in a directory, named by the time they start signing:
//
//	go run . -genkey keys/2026-10-01.pem
//	go run . -keys keys
package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/centrifugal/gocent/v4/token"
)

func main() {
	keysDir := flag.String("keys", "keys", "directory of PEM private keys, named by activation time")
	addr := flag.String("addr", ":3000", "address to listen on")
	genkey := flag.String("genkey", "", "write a new Ed25519 private key to this file and exit")
	flag.Parse()

	if *genkey != "" {
		if err := generateKey(*genkey); err != nil {
			log.Fatal(err)
		}
		return
	}

	keys, err := loadKeys(*keysDir)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := keys.current(time.Now()); err != nil {
		log.Fatal(err)
	}

	h := token.Handlers{OnError: func(r *http.Request, err error) {
		slog.Error("centrifugo token", "path", r.URL.Path, "err", err)
	}}
	mux := http.NewServeMux()

	// Centrifugo's client.token.jwks_public_endpoint: every key, those not
	// active yet included - so that every instance publishes a key before
	// any signs with it.
	mux.Handle("/centrifugo/jwks", h.JWKS(keys.all()...))

	// What the getToken callbacks of client SDKs fetch, signed with the key
	// active now.
	mux.Handle("/centrifugo/connection_token", h.Connection(func(r *http.Request) (string, error) {
		user, ok := sessionUser(r)
		if !ok {
			return "", token.ErrUnauthorized
		}
		key, err := keys.current(time.Now())
		if err != nil {
			return "", err
		}
		return token.Connection(key, token.ConnectionClaims{
			User:      user,
			ExpiresAt: time.Now().Add(15 * time.Minute),
		})
	}))
	mux.Handle("/centrifugo/subscription_token", h.Subscription(func(r *http.Request, channel string) (string, error) {
		user, ok := sessionUser(r)
		if !ok || !canRead(r.Context(), user, channel) {
			return "", token.ErrUnauthorized
		}
		key, err := keys.current(time.Now())
		if err != nil {
			return "", err
		}
		return token.Subscription(key, token.SubscriptionClaims{
			User:      user,
			Channel:   channel,
			ExpiresAt: time.Now().Add(15 * time.Minute),
		})
	}))

	log.Printf("publishing keys %s", strings.Join(keys.ids(), ", "))
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// activationLayouts are the forms of a key's file name: the day it starts
// signing, at 00:00 UTC, or a time of that day in UTC - hyphens instead of
// colons, which file names can't always have.
var activationLayouts = []string{"2006-01-02", "2006-01-02T15-04Z"}

// parseActivation returns the time the key named id starts signing.
func parseActivation(id string) (time.Time, error) {
	for _, layout := range activationLayouts {
		if t, err := time.Parse(layout, id); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("key name %q is neither YYYY-MM-DD nor YYYY-MM-DDTHH-MMZ (UTC)", id)
}

// activeKey is a key and the time it starts signing.
type activeKey struct {
	id    string
	from  time.Time
	value token.Key
}

// keySet holds the keys, ordered by activation time.
type keySet []activeKey

// current returns the key signing at now: the one activated most recently.
func (s keySet) current(now time.Time) (token.Key, error) {
	for i := len(s) - 1; i >= 0; i-- {
		if !s[i].from.After(now) {
			return s[i].value, nil
		}
	}
	return token.Key{}, errors.New("no key is active yet: name one by a time already passed")
}

func (s keySet) all() []token.Key {
	keys := make([]token.Key, len(s))
	for i, k := range s {
		keys[i] = k.value
	}
	return keys
}

func (s keySet) ids() []string {
	ids := make([]string, len(s))
	for i, k := range s {
		ids[i] = k.id + " (from " + k.from.Format(time.RFC3339) + ")"
	}
	return ids
}

// loadKeys reads every PEM private key in dir. A file's name, without
// ".pem", is its key ID and the time it starts signing.
func loadKeys(dir string) (keySet, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.pem"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no keys in %s: create one with -genkey %s/YYYY-MM-DD.pem", dir, dir)
	}
	keys := make(keySet, 0, len(files))
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".pem")
		from, err := parseActivation(id)
		if err != nil {
			return nil, err
		}
		key, err := readKey(f)
		if err != nil {
			return nil, err
		}
		keys = append(keys, activeKey{id: id, from: from, value: key.WithKeyID(id)})
	}
	slices.SortFunc(keys, func(a, b activeKey) int { return a.from.Compare(b.from) })
	for i := 1; i < len(keys); i++ {
		if keys[i].from.Equal(keys[i-1].from) {
			return nil, fmt.Errorf("keys %s and %s activate at the same time", keys[i-1].id, keys[i].id)
		}
	}
	return keys, nil
}

// readKey reads a PEM private key, PKCS #8 - as -genkey and openssl genpkey
// write it.
func readKey(path string) (token.Key, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the app's own key directory
	if err != nil {
		return token.Key{}, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return token.Key{}, fmt.Errorf("%s: not PEM", path)
	}
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return token.Key{}, fmt.Errorf("%s: %w", path, err)
	}
	key, err := keyOf(priv)
	if err != nil {
		return token.Key{}, fmt.Errorf("%s: %w", path, err)
	}
	return key, nil
}

func keyOf(priv crypto.PrivateKey) (token.Key, error) {
	switch k := priv.(type) {
	case ed25519.PrivateKey:
		return token.Ed25519(k), nil
	case *ecdsa.PrivateKey:
		return token.ECDSA(k), nil
	case *rsa.PrivateKey:
		return token.RSA(k), nil
	}
	return token.Key{}, fmt.Errorf("unsupported key type %T", priv)
}

// generateKey writes a new Ed25519 private key, PKCS #8 in PEM, to path,
// whose name must be an activation time.
func generateKey(path string) error {
	if _, err := parseActivation(strings.TrimSuffix(filepath.Base(path), ".pem")); err != nil {
		return err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o700); mkdirErr != nil {
		return mkdirErr
	}
	// O_EXCL: never overwrite a key in use.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a path the user gives
	if err != nil {
		return err
	}
	if encodeErr := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); encodeErr != nil {
		_ = f.Close()
		return encodeErr
	}
	return f.Close()
}

// sessionUser stands for the application's own authentication: here, a
// session cookie naming the user.
func sessionUser(r *http.Request) (string, bool) {
	c, err := r.Cookie("session")
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

// canRead stands for the application's own permission check.
func canRead(_ context.Context, user, channel string) bool {
	return channel == "personal:"+user || strings.HasPrefix(channel, "public:")
}
