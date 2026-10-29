package sharedpoll

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Sign returns the signature authorizing user to track keys in channel until
// expiresAt, as Centrifugo verifies it: "iat:exp:hmac", where iat is now and
// exp expiresAt in Unix seconds. secret is Centrifugo's
// shared_poll.hmac_secret_key.
//
// expiresAt is required, as a token's expiry is: a zero time is refused. A
// signature which never expires is chosen explicitly, with [SignNoExpiry].
//
// The signature covers exactly keys, in their order: a client tracking other
// keys, or the same in another order, is refused. user is the ID of the
// connection's user, empty for an anonymous one.
func Sign(secret []byte, user, channel string, keys []string, expiresAt time.Time) (string, error) {
	if expiresAt.IsZero() {
		return "", errors.New("sharedpoll: expiresAt is required: for a signature which never expires, use SignNoExpiry")
	}
	if len(secret) == 0 {
		return "", errEmptySecret
	}
	return signAt(secret, user, channel, keys, expiresAt, time.Now()), nil
}

// SignNoExpiry returns a signature as [Sign] does, which never expires: valid
// until the secret changes.
func SignNoExpiry(secret []byte, user, channel string, keys []string) (string, error) {
	if len(secret) == 0 {
		return "", errEmptySecret
	}
	return signAt(secret, user, channel, keys, time.Time{}, time.Now()), nil
}

var errEmptySecret = errors.New("sharedpoll: empty secret")

// signAt signs at now; a zero expiresAt signs one which never expires.
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
