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
	sig := Sign([]byte("s"), "u", "c", []string{"k"}, time.Time{})
	parts := strings.Split(sig, ":")
	if len(parts) != 3 || parts[1] != "0" || len(parts[2]) != 64 {
		t.Fatalf("signature %q", sig)
	}
	if iat, err := strconv.ParseInt(parts[0], 10, 64); err != nil || iat < before || iat > time.Now().Unix() {
		t.Errorf("iat %s, want now", parts[0])
	}
	exp := time.Now().Add(time.Hour)
	if parts := strings.Split(Sign([]byte("s"), "u", "c", nil, exp), ":"); parts[1] != strconv.FormatInt(exp.Unix(), 10) {
		t.Errorf("exp %s, want %d", parts[1], exp.Unix())
	}
}
