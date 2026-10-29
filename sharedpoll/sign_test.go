package sharedpoll

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// Signatures Centrifugo's own verifier (verifyTrackSignature in
// internal/client) accepted, made at a fixed time with the secret "s3cret".
// The cases cover what the format must keep apart: colons in a user or
// channel, an anonymous user, no expiry, keys beyond ASCII.
func TestSignMatchesCentrifugo(t *testing.T) {
	now := time.Unix(1760000000, 0)
	exp := time.Unix(1760003600, 0)
	for _, c := range []struct {
		user, channel string
		keys          []string
		want          string
	}{
		{"42", "votes:feed1", []string{"post_1", "post_2"}, "1760000000:1760003600:59ae6606f7ed1d74541143f8c111bb31fa1cf36793e020fe8e1d27820b695df4"},
		{"", "votes:feed1", []string{"post_1"}, "1760000000:1760003600:8b1350c6410e3c78b1d3e0ed09d6399e57e50d176a73740731510a3504e7e8c0"},
		{"alice:news", "tech", []string{"k"}, "1760000000:1760003600:c7bb20b61ca4ceb78bb706cbde7759c419921e0d9818949af4b043741c532997"},
		{"alice", "news:tech", []string{"k"}, "1760000000:1760003600:d9561a54a3c3407a7eca91f8d8fc9fa40a3099aa0cd39a1390a7579fb5429007"},
		{"42", "poll", []string{"ключ", "鍵", "k:1"}, "1760000000:0:3cb57ff8462f3428acaba5528f1ac1d191b17cc12be8476a34d9234e9d6ba7ba"},
	} {
		e := exp
		if strings.Contains(c.want, ":0:") {
			e = time.Time{}
		}
		if got := signAt([]byte("s3cret"), c.user, c.channel, c.keys, e, now); got != c.want {
			t.Errorf("Sign(%q, %q, %q) = %s, want %s", c.user, c.channel, c.keys, got, c.want)
		}
	}
}

func TestSignTimes(t *testing.T) {
	before := time.Now().Unix()
	sig, err := SignNoExpiry([]byte("s"), "u", "c", []string{"k"})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(sig, ":")
	if len(parts) != 3 || parts[1] != "0" || len(parts[2]) != 64 {
		t.Fatalf("signature %q", sig)
	}
	if iat, perr := strconv.ParseInt(parts[0], 10, 64); perr != nil || iat < before || iat > time.Now().Unix() {
		t.Errorf("iat %s, want now", parts[0])
	}
	exp := time.Now().Add(time.Hour)
	sig, err = Sign([]byte("s"), "u", "c", nil, exp)
	if parts := strings.Split(sig, ":"); err != nil || parts[1] != strconv.FormatInt(exp.Unix(), 10) {
		t.Errorf("exp %s, %v, want %d", parts[1], err, exp.Unix())
	}
}

// An expiry is chosen, never left out: a zero time is refused, and so is an
// empty secret.
func TestSignRefusals(t *testing.T) {
	if _, err := Sign([]byte("s"), "u", "c", nil, time.Time{}); err == nil || !strings.Contains(err.Error(), "SignNoExpiry") {
		t.Errorf("zero expiry: %v", err)
	}
	if _, err := Sign(nil, "u", "c", nil, time.Now().Add(time.Hour)); err == nil {
		t.Error("empty secret signed")
	}
	if _, err := SignNoExpiry(nil, "u", "c", nil); err == nil {
		t.Error("empty secret signed")
	}
}

// The vectors of spec.md, section 15.1, which every SDK reproduces.
func TestSpecVectors(t *testing.T) {
	iat := time.Unix(1700000000, 0)
	for _, v := range []struct {
		user, channel string
		keys          []string
		exp           time.Time
		want          string
	}{
		{"42", "poll:prices", []string{"AAPL", "GOOG"}, time.Unix(1700003600, 0), "1700000000:1700003600:c61dbf52085a6973537e71644fa30a3bbfc29a52d7ce9f1b7618531596ce587d"},
		{"", "poll:prices", nil, time.Time{}, "1700000000:0:92dd64af5612afde06cad03bb63435d951e037cb55a087af05c2a1399abade0e"},
	} {
		if got := signAt([]byte("shared_poll_secret"), v.user, v.channel, v.keys, v.exp, iat); got != v.want {
			t.Errorf("%q %v: %s, want %s", v.user, v.keys, got, v.want)
		}
	}
}
