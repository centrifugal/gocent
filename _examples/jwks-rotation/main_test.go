package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4/token"
)

func TestParseActivation(t *testing.T) {
	for id, want := range map[string]string{
		"2027-04-01":        "2027-04-01T00:00:00Z",
		"2027-04-01T15-30Z": "2027-04-01T15:30:00Z",
	} {
		got, err := parseActivation(id)
		if err != nil || got.Format(time.RFC3339) != want {
			t.Errorf("%s: %v, %v; want %s", id, got, err, want)
		}
	}
	for _, id := range []string{"key1", "2027-04-01T15:30Z", "2027-04-01T15-30", "2027-13-01", "27-04-01"} {
		if _, err := parseActivation(id); err == nil {
			t.Errorf("%s: parsed", id)
		}
	}
}

// genKeys writes keys named ids into a new directory.
func genKeys(t *testing.T, ids ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, id := range ids {
		if err := generateKey(filepath.Join(dir, id+".pem")); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The key signing at a time is the one activated most recently - whatever
// the form of the names, which order by time, not as strings.
func TestCurrent(t *testing.T) {
	keys, err := loadKeys(genKeys(t, "2026-10-01", "2027-04-01T15-30Z", "2027-04-01"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keys.ids(), " "); !strings.HasPrefix(got, "2026-10-01 ") || !strings.Contains(got, "2027-04-01 (from 2027-04-01T00:00:00Z) 2027-04-01T15-30Z") {
		t.Fatalf("order %s", got)
	}
	signer := func(now string) string {
		at, err := time.Parse(time.RFC3339, now)
		if err != nil {
			t.Fatal(err)
		}
		key, err := keys.current(at)
		if err != nil {
			return "error"
		}
		for _, k := range keys {
			if same(t, k.value, key) {
				return k.id
			}
		}
		return "?"
	}
	for now, want := range map[string]string{
		"2026-09-30T23:59:59Z": "error",
		"2026-10-01T00:00:00Z": "2026-10-01",
		"2027-03-31T23:59:59Z": "2026-10-01",
		"2027-04-01T00:00:00Z": "2027-04-01",
		"2027-04-01T15:29:59Z": "2027-04-01",
		"2027-04-01T15:30:00Z": "2027-04-01T15-30Z",
		"2030-01-01T00:00:00Z": "2027-04-01T15-30Z",
	} {
		if got := signer(now); got != want {
			t.Errorf("at %s: %s signs, want %s", now, got, want)
		}
	}
}

// same reports whether two keys sign as one: their JWKs, with one ID, match.
func same(t *testing.T, a, b token.Key) bool {
	t.Helper()
	ja, err := token.JWKS(a.WithKeyID("x"))
	if err != nil {
		t.Fatal(err)
	}
	jb, err := token.JWKS(b.WithKeyID("x"))
	if err != nil {
		t.Fatal(err)
	}
	return string(ja) == string(jb)
}

// Every key is published, those not active yet included.
func TestAllPublished(t *testing.T) {
	keys, err := loadKeys(genKeys(t, "2026-10-01", "2099-01-01"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := token.JWKS(keys.all()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{`"kid":"2026-10-01"`, `"kid":"2099-01-01"`} {
		if !strings.Contains(string(set), id) {
			t.Errorf("%s not published: %s", id, set)
		}
	}
}

func TestLoadKeysRefuses(t *testing.T) {
	if _, err := loadKeys(t.TempDir()); err == nil {
		t.Error("an empty directory loaded")
	}
	// The same time in both forms.
	if _, err := loadKeys(genKeys(t, "2027-04-01", "2027-04-01T00-00Z")); err == nil {
		t.Error("two keys activating together loaded")
	}
	dir := genKeys(t, "2026-10-01")
	if err := os.WriteFile(filepath.Join(dir, "rotate-me.pem"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKeys(dir); err == nil {
		t.Error("a key named by no time loaded")
	}
	if err := generateKey(filepath.Join(t.TempDir(), "newest.pem")); err == nil {
		t.Error("-genkey wrote a key named by no time")
	}
	path := filepath.Join(t.TempDir(), "2026-10-01.pem")
	if err := generateKey(path); err != nil {
		t.Fatal(err)
	}
	if err := generateKey(path); err == nil {
		t.Error("-genkey overwrote a key")
	}
}
