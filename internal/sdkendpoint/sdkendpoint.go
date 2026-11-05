// Package sdkendpoint holds what the HTTP endpoints client SDKs call share:
// the token endpoints of package token and the signature endpoint of package
// sharedpoll. Each answers a JSON object, which must not be cached; an
// unauthorized user with 403, which tells an SDK to stop asking; and any
// other failure with 500, which an SDK retries.
package sdkendpoint

import (
	"encoding/json/v2"
	"errors"
	"net/http"
)

// MaxRequestBytes bounds a request's body: a channel and the keys of a
// shared poll subscription at most.
const MaxRequestBytes = 1 << 20

// Decode reads a POST's JSON body into v. On failure it answers 400 or 405
// and returns false; a malformed body is reported to onError.
func Decode(w http.ResponseWriter, r *http.Request, v any, onError func(*http.Request, error)) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "only POST is allowed", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, MaxRequestBytes), v); err != nil {
		Fail(w, r, http.StatusBadRequest, err, onError)
		return false
	}
	return true
}

// Answer writes the answer of a function which returned v and err: v as
// JSON, 403 for unauthorized - the package's ErrUnauthorized, also wrapped -
// or 500 for any other error, reported to onError.
func Answer(w http.ResponseWriter, r *http.Request, v any, err, unauthorized error, onError func(*http.Request, error)) {
	switch {
	case errors.Is(err, unauthorized):
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	case err != nil:
		Fail(w, r, http.StatusInternalServerError, err, onError)
		return
	}
	body, err := json.Marshal(v)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, err, onError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	// A token or signature belongs to one user: no cache may keep it.
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// Fail answers status, after reporting err to onError.
func Fail(w http.ResponseWriter, r *http.Request, status int, err error, onError func(*http.Request, error)) {
	if onError != nil {
		onError(r, err)
	}
	http.Error(w, http.StatusText(status), status)
}
