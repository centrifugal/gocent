package proxy

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
)

// Handlers builds an [http.Handler] for each proxy type, all sharing its
// settings. The zero value is ready to use:
//
//	h := proxy.Handlers{OnError: func(r *http.Request, err error) {
//		slog.Error("centrifugo proxy", "path", r.URL.Path, "err", err)
//	}}
//	mux.Handle("/centrifugo/connect", h.Connect(connect))
//	mux.Handle("/centrifugo/subscribe", h.Subscribe(subscribe))
type Handlers struct {
	// OnError, when set, is told of every request a handler could not
	// answer as the function meant: one failed with HTTP status 500 - by an
	// error which is not an *Error or *Disconnect the proxy type carries, or
	// by a result which cannot be encoded - and a malformed one, rejected with
	// status 400. Centrifugo only logs the status, so this is where the cause
	// is seen. It is called before the response is written.
	OnError func(r *http.Request, err error)

	// MaxRequestBytes bounds the body of a request. Zero means 16 MiB, far
	// above what Centrifugo's own limits on client messages let through: it
	// only stops a request which is not Centrifugo's from taking the memory
	// it likes.
	MaxRequestBytes int64
}

const defaultMaxRequestBytes = 16 << 20

// allowRepeatedNames lets an object repeat a name, in requests and results:
// payloads carry what clients and the application send, and whether one may
// repeat a key is Centrifugo's to decide.
var allowRepeatedNames = jsontext.AllowDuplicateNames(true)

// answers says which of error and disconnect a proxy type's response can
// carry: those Centrifugo acts on.
type answers uint8

const (
	// resultOnly: the response's error and disconnect are ignored, so the
	// function's errors can only fail the request.
	resultOnly answers = iota
	// errorOrResult: an *Error is the response's error.
	errorOrResult
	// errorDisconnectOrResult: an *Error is the response's error, a
	// *Disconnect its disconnect - both reach the client.
	errorDisconnectOrResult
)

// serve builds the handler of a proxy type: it decodes the request, calls f,
// and encodes its result, or the *Error or *Disconnect it returned, as the
// response wrap builds. Any other error fails the request with HTTP status
// 500, which Centrifugo takes as an internal error.
func serve[Req, Res, Resp any](h Handlers, name string, f func(*http.Request, Req) (Res, error), can answers, wrap func(*Res, *Error, *Disconnect) Resp) http.Handler {
	if f == nil {
		panic("proxy: nil function for the " + name + " proxy")
	}
	limit := h.MaxRequestBytes
	if limit <= 0 {
		limit = defaultMaxRequestBytes
	}
	fail := func(w http.ResponseWriter, r *http.Request, status int, err error) {
		if h.OnError != nil {
			h.OnError(r, err)
		}
		http.Error(w, http.StatusText(status), status)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "proxy: only POST is allowed", http.StatusMethodNotAllowed)
			return
		}
		var req Req
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, limit), &req, allowRepeatedNames); err != nil {
			fail(w, r, http.StatusBadRequest, fmt.Errorf("proxy: malformed %s request: %w", name, err))
			return
		}
		res, err := f(r, req)
		var resp Resp
		if err == nil {
			resp = wrap(&res, nil, nil)
		} else {
			d, isDisconnect := errors.AsType[*Disconnect](err)
			e, isError := errors.AsType[*Error](err)
			switch {
			case isDisconnect && can == errorDisconnectOrResult:
				resp = wrap(nil, nil, d)
			case isError && can != resultOnly:
				resp = wrap(nil, e, nil)
			case isDisconnect || isError:
				// Centrifugo would ignore it in this proxy's response.
				fail(w, r, http.StatusInternalServerError, fmt.Errorf("proxy: the %s proxy cannot answer with %w, failing the request", name, err))
				return
			default:
				fail(w, r, http.StatusInternalServerError, fmt.Errorf("proxy: %s: %w", name, err))
				return
			}
		}
		body, err := json.Marshal(resp, allowRepeatedNames)
		if err != nil {
			// A payload which is not valid JSON, say: Centrifugo could not
			// use the response.
			fail(w, r, http.StatusInternalServerError, fmt.Errorf("proxy: encoding the %s response: %w", name, err))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}

// Connect serves the connect proxy: f authenticates a connecting client
// and returns its user and initial state. Returning a *[Error] rejects the
// connection with that error; a *[Disconnect] closes it with that code.
func (h Handlers) Connect(f func(r *http.Request, req ConnectRequest) (ConnectResult, error)) http.Handler {
	return serve(h, "connect", f, errorDisconnectOrResult, func(res *ConnectResult, e *Error, d *Disconnect) ConnectResponse {
		return ConnectResponse{Result: res, Error: e, Disconnect: d}
	})
}

// Refresh serves the refresh proxy: f decides whether a connection
// about to expire lives on, and until when. To end the connection, return a
// result with Expired set. Any error fails the request, and Centrifugo keeps
// the connection for another minute before asking again: Centrifugo ignores
// an error or a disconnect in a refresh response.
func (h Handlers) Refresh(f func(r *http.Request, req RefreshRequest) (RefreshResult, error)) http.Handler {
	return serve(h, "refresh", f, resultOnly, func(res *RefreshResult, _ *Error, _ *Disconnect) RefreshResponse {
		return RefreshResponse{Result: res}
	})
}

// Subscribe serves the subscribe proxy: f decides whether a client may
// subscribe to a channel. Returning a *[Error] rejects the subscription with
// that error; a *[Disconnect] closes the connection with that code.
func (h Handlers) Subscribe(f func(r *http.Request, req SubscribeRequest) (SubscribeResult, error)) http.Handler {
	return serve(h, "subscribe", f, errorDisconnectOrResult, func(res *SubscribeResult, e *Error, d *Disconnect) SubscribeResponse {
		return SubscribeResponse{Result: res, Error: e, Disconnect: d}
	})
}

// Publish serves the publish proxy: f decides whether a client may
// publish into a channel, and may change the publication. Returning a
// *[Error] rejects the publication with that error; a *[Disconnect] closes the
// connection with that code.
func (h Handlers) Publish(f func(r *http.Request, req PublishRequest) (PublishResult, error)) http.Handler {
	return serve(h, "publish", f, errorDisconnectOrResult, func(res *PublishResult, e *Error, d *Disconnect) PublishResponse {
		return PublishResponse{Result: res, Error: e, Disconnect: d}
	})
}

// RPC serves the RPC proxy: f answers a client's RPC call. Returning a
// *[Error] fails the call with that error; a *[Disconnect] closes the
// connection with that code.
func (h Handlers) RPC(f func(r *http.Request, req RPCRequest) (RPCResult, error)) http.Handler {
	return serve(h, "rpc", f, errorDisconnectOrResult, func(res *RPCResult, e *Error, d *Disconnect) RPCResponse {
		return RPCResponse{Result: res, Error: e, Disconnect: d}
	})
}

// SubRefresh serves the sub refresh proxy: f decides whether a
// subscription about to expire lives on, and until when. To end the
// subscription, return a result with Expired set. Any error fails the
// request, and Centrifugo keeps the subscription for another minute before
// asking again: Centrifugo ignores an error or a disconnect in a sub refresh
// response.
func (h Handlers) SubRefresh(f func(r *http.Request, req SubRefreshRequest) (SubRefreshResult, error)) http.Handler {
	return serve(h, "sub refresh", f, resultOnly, func(res *SubRefreshResult, _ *Error, _ *Disconnect) SubRefreshResponse {
		return SubRefreshResponse{Result: res}
	})
}

// MapPublish serves the map publish proxy: f decides whether a client
// may publish a key into a map channel, and may change the publication.
// Returning a *[Error] rejects the publication with that error; a
// *[Disconnect] closes the connection with that code.
func (h Handlers) MapPublish(f func(r *http.Request, req MapPublishRequest) (MapPublishResult, error)) http.Handler {
	return serve(h, "map publish", f, errorDisconnectOrResult, func(res *MapPublishResult, e *Error, d *Disconnect) MapPublishResponse {
		return MapPublishResponse{Result: res, Error: e, Disconnect: d}
	})
}

// MapRemove serves the map remove proxy: f decides whether a client may
// remove a key from a map channel. Returning a *[Error] rejects the removal
// with that error; a *[Disconnect] closes the connection with that code.
func (h Handlers) MapRemove(f func(r *http.Request, req MapRemoveRequest) (MapRemoveResult, error)) http.Handler {
	return serve(h, "map remove", f, errorDisconnectOrResult, func(res *MapRemoveResult, e *Error, d *Disconnect) MapRemoveResponse {
		return MapRemoveResponse{Result: res, Error: e, Disconnect: d}
	})
}

// SharedPollRefresh serves the shared poll refresh proxy: f returns the
// current data of the items tracked in a shared poll channel. Returning a
// *[Error], or any other error, fails the refresh: Centrifugo tries again at
// the next interval.
func (h Handlers) SharedPollRefresh(f func(r *http.Request, req SharedPollRefreshRequest) (SharedPollRefreshResult, error)) http.Handler {
	return serve(h, "shared poll refresh", f, errorOrResult, func(res *SharedPollRefreshResult, e *Error, _ *Disconnect) SharedPollRefreshResponse {
		return SharedPollRefreshResponse{Result: res, Error: e}
	})
}

// NotifyCacheEmpty serves the cache empty proxy of Centrifugo PRO: f
// learns that a client subscribed to a channel in cache recovery mode with
// nothing to recover, and may publish the current state - then it returns a
// result with Populated set, for Centrifugo to recover once more. Any error
// fails the request, and the client which subscribed is disconnected to
// reconnect.
func (h Handlers) NotifyCacheEmpty(f func(r *http.Request, req NotifyCacheEmptyRequest) (NotifyCacheEmptyResult, error)) http.Handler {
	return serve(h, "cache empty", f, resultOnly, func(res *NotifyCacheEmptyResult, _ *Error, _ *Disconnect) NotifyCacheEmptyResponse {
		return NotifyCacheEmptyResponse{Result: res}
	})
}

// NotifyChannelState serves the channel state proxy of Centrifugo PRO,
// in preview: f receives channels becoming occupied or vacated. Returning a
// *[Error], or any other error, fails the request.
func (h Handlers) NotifyChannelState(f func(r *http.Request, req NotifyChannelStateRequest) (NotifyChannelStateResult, error)) http.Handler {
	return serve(h, "channel state", f, errorOrResult, func(res *NotifyChannelStateResult, e *Error, _ *Disconnect) NotifyChannelStateResponse {
		return NotifyChannelStateResponse{Result: res, Error: e}
	})
}
