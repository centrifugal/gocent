package token

import (
	"encoding/json/v2"
	"errors"
	"net/http"

	"github.com/centrifugal/gocent/v4/internal/sdkendpoint"
)

// ErrUnauthorized is returned - or wrapped - by a function a handler calls
// when the user may not have a token. It answers HTTP status 403, on which a
// client's getToken callback throws UnauthorizedError: client SDKs stop
// asking then, and disconnect or unsubscribe.
var ErrUnauthorized = errors.New("token: unauthorized")

// Handlers builds the [http.Handler]s of the endpoints client SDKs call for
// tokens: what their getToken callbacks fetch, on connecting and subscribing,
// and again before a token expires. The zero value is ready to use.
//
// A handler answers {"token": "..."}, never cached; [ErrUnauthorized] with
// status 403; and any other error with status 500, which a client retries.
type Handlers struct {
	// OnError, when set, is told of every request a handler could not answer
	// as the function meant: one failed with HTTP status 500 - by an error
	// other than ErrUnauthorized - and a malformed one, rejected with status
	// 400.
	OnError func(r *http.Request, err error)
}

type tokenResponse struct {
	Token string `json:"token"`
}

// Connection serves the endpoint the getToken callback of a client calls: f
// returns the connection token of the request's user - from [Connection],
// say. It takes GET and POST requests, their body ignored.
func (h Handlers) Connection(f func(r *http.Request) (string, error)) http.Handler {
	if f == nil {
		panic("token: nil function for the connection token endpoint")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "only GET and POST are allowed", http.StatusMethodNotAllowed)
			return
		}
		tok, err := f(r)
		sdkendpoint.Answer(w, r, tokenResponse{tok}, err, ErrUnauthorized, h.OnError)
	})
}

// Subscription serves the endpoint the getToken callback of a subscription
// calls: f returns the token of the request's user for channel - from
// [Subscription], say - or ErrUnauthorized when the user may not subscribe.
// The channel comes from a POST's JSON body, {"channel": "..."}, or from the
// "channel" query parameter of a GET.
func (h Handlers) Subscription(f func(r *http.Request, channel string) (string, error)) http.Handler {
	if f == nil {
		panic("token: nil function for the subscription token endpoint")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var channel string
		switch r.Method {
		case http.MethodGet:
			channel = r.URL.Query().Get("channel")
		case http.MethodPost:
			var req struct {
				Channel string `json:"channel"`
			}
			if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, sdkendpoint.MaxRequestBytes), &req); err != nil {
				sdkendpoint.Fail(w, r, http.StatusBadRequest, err, h.OnError)
				return
			}
			channel = req.Channel
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "only GET and POST are allowed", http.StatusMethodNotAllowed)
			return
		}
		if channel == "" {
			sdkendpoint.Fail(w, r, http.StatusBadRequest, errors.New("token: subscription token request without a channel"), h.OnError)
			return
		}
		tok, err := f(r, channel)
		sdkendpoint.Answer(w, r, tokenResponse{tok}, err, ErrUnauthorized, h.OnError)
	})
}
