package sharedpoll

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/centrifugal/gocent/v4/internal/sdkendpoint"
	"github.com/centrifugal/gocent/v4/token"
)

// ErrUnauthorized is returned - or wrapped - by a function a handler calls
// when the user may not use the subscription at all. It answers HTTP status
// 403.
// centrifuge-js ends the subscription then, when the getSignature callback
// throws UnauthorizedError for it. It is [token.ErrUnauthorized]: an
// application returning either from either package's handlers gets a 403.
var ErrUnauthorized = token.ErrUnauthorized

// Handlers builds the [http.Handler] of the endpoint client SDKs call for
// signatures. The zero value is ready to use.
type Handlers struct {
	// OnError, when set, is told of every request a handler could not answer
	// as the function meant: one failed with HTTP status 500 - by an error
	// other than ErrUnauthorized, or by keys the function had no request for -
	// and a malformed one, rejected with status 400. It must not panic.
	OnError func(r *http.Request, err error)
}

// signatureRequest is what a client sends: the channel of its subscription
// and the keys it tracks.
type signatureRequest struct {
	Channel string   `json:"channel"`
	Keys    []string `json:"keys"`
}

// signatureResponse is what getSignature returns to the SDK.
type signatureResponse struct {
	Signature string   `json:"signature"`
	Keys      []string `json:"keys"`
}

// Signature serves the endpoint a shared poll subscription's getSignature
// callback calls: it takes a POST of {"channel": ..., "keys": [...]} and
// answers {"signature": ..., "keys": [...]}, never cached.
//
// f decides which of keys the user may track in channel, and signs them -
// with [Sign], say. It returns them, in the order signed, with the signature;
// the keys it leaves out are revoked: the client stops tracking them. To end
// the subscription instead, it returns [ErrUnauthorized]; any other error fails
// the request with status 500, and the client asks again.
//
// f may return only keys it was asked for: the client tracks no others, and a
// signature covering one would be refused. A key it was not asked for fails
// the request.
func (h Handlers) Signature(f func(r *http.Request, channel string, keys []string) (allowed []string, signature string, err error)) http.Handler {
	if f == nil {
		panic("sharedpoll: nil function for the signature endpoint")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req signatureRequest
		if !sdkendpoint.Decode(w, r, &req, h.OnError) {
			return
		}
		if req.Channel == "" {
			sdkendpoint.Fail(w, r, http.StatusBadRequest, errors.New("sharedpoll: signature request without a channel"), h.OnError)
			return
		}
		allowed, signature, err := f(r, req.Channel, req.Keys)
		if err == nil {
			for _, k := range allowed {
				if !slices.Contains(req.Keys, k) {
					err = fmt.Errorf("sharedpoll: signed key %q the client did not ask for", k)
					break
				}
			}
		}
		if allowed == nil {
			allowed = []string{} // the SDK reads the keys as an array
		}
		sdkendpoint.Answer(w, r, signatureResponse{Signature: signature, Keys: allowed}, err, ErrUnauthorized, h.OnError)
	})
}
