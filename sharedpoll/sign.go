package sharedpoll

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Sign returns the signature authorizing user to track keys in channel until
// expiresAt, as Centrifugo verifies it: "iat:exp:hmac", where iat is now and
// exp expiresAt in Unix seconds, zero for a zero expiresAt - a signature which
// does not expire. secret is Centrifugo's shared_poll.hmac_secret_key.
//
// The signature covers exactly keys, in their order: a client tracking other
// keys, or the same in another order, is refused. user is the ID of the
// connection's user, empty for an anonymous one.
func Sign(secret []byte, user, channel string, keys []string, expiresAt time.Time) string {
	return signAt(secret, user, channel, keys, expiresAt, time.Now())
}

func signAt(secret []byte, user, channel string, keys []string, expiresAt, now time.Time) string {
	iat := strconv.FormatInt(now.Unix(), 10)
	exp := "0"
	if !expiresAt.IsZero() {
		exp = strconv.FormatInt(expiresAt.Unix(), 10)
	}
	keysHash := sha256.Sum256([]byte(strings.Join(keys, "\x00")))
	// NUL-separated, so that a colon in a user or channel cannot move between
	// fields: ("alice", "news:tech") and ("alice:news", "tech") sign
	// differently.
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(iat + "\x00" + exp + "\x00" + user + "\x00" + channel + "\x00" + hex.EncodeToString(keysHash[:])))
	return iat + ":" + exp + ":" + hex.EncodeToString(mac.Sum(nil))
}
