package proxy_test

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/centrifugal/gocent/v4/proxy"
)

// post sends body to h as Centrifugo does, and returns the status and body
// of the answer.
func post(t *testing.T, h http.Handler, body string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(body)))
	if w.Code == http.StatusOK {
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type %q", ct)
		}
	}
	return w.Code, strings.TrimSpace(w.Body.String())
}

// A request as Centrifugo encodes it reaches the function decoded, and the
// result goes back as Centrifugo decodes it.
func TestConnectHandler(t *testing.T) {
	var got proxy.ConnectRequest
	h := proxy.Handlers{}.Connect(func(r *http.Request, req proxy.ConnectRequest) (proxy.ConnectResult, error) {
		got = req
		return proxy.ConnectResult{
			User:     "42",
			ExpireAt: 1700000000,
			Info:     jsontext.Value(`{"name":"Alice"}`),
			Subs: map[string]proxy.SubscribeOptions{
				"news": {Override: proxy.SubscribeOptionOverride{Presence: proxy.Bool(false)}},
			},
		}, nil
	})
	status, body := post(t, h, `{"client":"c1","transport":"websocket","protocol":"json","encoding":"json",`+
		`"data":{"token":"t"},"name":"js","version":"5.8.0","channels":["news"],"unknown_future_field":1}`)
	want := proxy.ConnectRequest{
		Client: "c1", Transport: "websocket", Protocol: "json", Encoding: "json",
		Data: jsontext.Value(`{"token":"t"}`), Name: "js", Version: "5.8.0", Channels: []string{"news"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request %+v, want %+v", got, want)
	}
	wantBody := `{"result":{"user":"42","expire_at":1700000000,"info":{"name":"Alice"},` +
		`"subs":{"news":{"override":{"presence":{}}}}}}`
	if status != http.StatusOK || body != wantBody {
		t.Errorf("%d %s, want %s", status, body, wantBody)
	}
}

// An empty result still accepts the request: it is sent, not left out.
func TestEmptyResult(t *testing.T) {
	h := proxy.Handlers{}.Subscribe(func(*http.Request, proxy.SubscribeRequest) (proxy.SubscribeResult, error) {
		return proxy.SubscribeResult{}, nil
	})
	if status, body := post(t, h, `{"user":"42","channel":"news"}`); status != http.StatusOK || body != `{"result":{}}` {
		t.Errorf("%d %s", status, body)
	}
}

// every builds each handler around a function returning err, with what its
// response can carry: "ed" error and disconnect, "e" error only, "" neither.
func every(err error) map[string]struct {
	h   http.Handler
	can string
} {
	type entry = struct {
		h   http.Handler
		can string
	}
	return map[string]entry{
		"connect": {proxy.Handlers{}.Connect(func(*http.Request, proxy.ConnectRequest) (proxy.ConnectResult, error) {
			return proxy.ConnectResult{}, err
		}), "ed"},
		"refresh": {proxy.Handlers{}.Refresh(func(*http.Request, proxy.RefreshRequest) (proxy.RefreshResult, error) {
			return proxy.RefreshResult{}, err
		}), ""},
		"subscribe": {proxy.Handlers{}.Subscribe(func(*http.Request, proxy.SubscribeRequest) (proxy.SubscribeResult, error) {
			return proxy.SubscribeResult{}, err
		}), "ed"},
		"publish": {proxy.Handlers{}.Publish(func(*http.Request, proxy.PublishRequest) (proxy.PublishResult, error) {
			return proxy.PublishResult{}, err
		}), "ed"},
		"rpc": {proxy.Handlers{}.RPC(func(*http.Request, proxy.RPCRequest) (proxy.RPCResult, error) {
			return proxy.RPCResult{}, err
		}), "ed"},
		"sub refresh": {proxy.Handlers{}.SubRefresh(func(*http.Request, proxy.SubRefreshRequest) (proxy.SubRefreshResult, error) {
			return proxy.SubRefreshResult{}, err
		}), ""},
		"map publish": {proxy.Handlers{}.MapPublish(func(*http.Request, proxy.MapPublishRequest) (proxy.MapPublishResult, error) {
			return proxy.MapPublishResult{}, err
		}), "ed"},
		"map remove": {proxy.Handlers{}.MapRemove(func(*http.Request, proxy.MapRemoveRequest) (proxy.MapRemoveResult, error) {
			return proxy.MapRemoveResult{}, err
		}), "ed"},
		"shared poll refresh": {proxy.Handlers{}.SharedPollRefresh(func(*http.Request, proxy.SharedPollRefreshRequest) (proxy.SharedPollRefreshResult, error) {
			return proxy.SharedPollRefreshResult{}, err
		}), "e"},
		"notify cache empty": {proxy.Handlers{}.NotifyCacheEmpty(func(*http.Request, proxy.NotifyCacheEmptyRequest) (proxy.NotifyCacheEmptyResult, error) {
			return proxy.NotifyCacheEmptyResult{}, err
		}), ""},
		"notify channel state": {proxy.Handlers{}.NotifyChannelState(func(*http.Request, proxy.NotifyChannelStateRequest) (proxy.NotifyChannelStateResult, error) {
			return proxy.NotifyChannelStateResult{}, err
		}), "e"},
	}
}

// Every handler answers a result as {"result":...}, and turns each kind of
// error into what Centrifugo acts on for its proxy type - an error or
// disconnect where the response honours one, a failed request otherwise.
func TestErrors(t *testing.T) {
	errDenied := &proxy.Error{Code: 403, Message: "permission denied"}
	const (
		denied = `{"error":{"code":403,"message":"permission denied"}}`
		gone   = `{"disconnect":{"code":4501,"reason":"unauthorized"}}`
		fail   = "500"
	)
	cases := []struct {
		name string
		err  error
		// want for a response carrying error and disconnect, error only,
		// and neither.
		want map[string]string
	}{
		{"none", nil, map[string]string{"ed": `{"result":{}}`, "e": `{"result":{}}`, "": `{"result":{}}`}},
		{"error", errDenied, map[string]string{"ed": denied, "e": denied, "": fail}},
		{"wrapped error", fmt.Errorf("checking: %w", errDenied), map[string]string{"ed": denied, "e": denied, "": fail}},
		{"disconnect", &proxy.Disconnect{Code: 4501, Reason: "unauthorized"}, map[string]string{"ed": gone, "e": fail, "": fail}},
		{"other", errors.New("database down"), map[string]string{"ed": fail, "e": fail, "": fail}},
	}
	for _, tc := range cases {
		for name, e := range every(tc.err) {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				status, body := post(t, e.h, `{}`)
				if w := tc.want[e.can]; w == fail {
					if status != http.StatusInternalServerError {
						t.Errorf("%d %s, want status 500", status, body)
					}
				} else if status != http.StatusOK || body != w {
					t.Errorf("%d %s, want %s", status, body, w)
				}
			})
		}
	}
}

// Only a POST with a JSON body is a proxy request; a result which cannot be
// encoded fails rather than sending Centrifugo something it cannot read.
func TestBadRequests(t *testing.T) {
	ok := proxy.Handlers{}.RPC(func(*http.Request, proxy.RPCRequest) (proxy.RPCResult, error) {
		return proxy.RPCResult{}, nil
	})
	w := httptest.NewRecorder()
	ok.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
		t.Errorf("GET: %d, Allow %q", w.Code, w.Header().Get("Allow"))
	}
	for name, body := range map[string]string{
		"empty":      ``,
		"not JSON":   `<html>`,
		"wrong type": `{"user":42}`,
		"too large":  `{"data":"` + strings.Repeat("x", 17<<20) + `"}`,
	} {
		if status, _ := post(t, ok, body); status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, status)
		}
	}
	invalid := proxy.Handlers{}.RPC(func(*http.Request, proxy.RPCRequest) (proxy.RPCResult, error) {
		return proxy.RPCResult{Data: jsontext.Value(`{not json`)}, nil
	})
	if status, _ := post(t, invalid, `{}`); status != http.StatusInternalServerError {
		t.Errorf("invalid result data: status %d, want 500", status)
	}
}

// The function gets Centrifugo's request: its headers and context.
func TestRequestPassedOn(t *testing.T) {
	var cookie string
	h := proxy.Handlers{}.Connect(func(r *http.Request, _ proxy.ConnectRequest) (proxy.ConnectResult, error) {
		if c, err := r.Cookie("session"); err == nil {
			cookie = c.Value
		}
		return proxy.ConnectResult{}, r.Context().Err()
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: "session", Value: "s1", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || cookie != "s1" {
		t.Errorf("%d, cookie %q", w.Code, cookie)
	}
}

func TestNilFunctionPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	proxy.Handlers{}.Publish(nil)
}

// Whatever arrives, a handler whose function succeeds answers with its
// result or rejects the request as malformed - it never fails or panics.
func FuzzHandler(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"client":"c","data":{"a":1},"channels":["x"]}`, `{"data":null}`,
		`{"user":"1","user":"2"}`, `{"subs":{}}`, `[]`, `null`, ``, `{`, `{"data":"\ud800"}`,
	} {
		f.Add([]byte(seed))
	}
	h := proxy.Handlers{}.Connect(func(_ *http.Request, req proxy.ConnectRequest) (proxy.ConnectResult, error) {
		return proxy.ConnectResult{User: req.Name, Data: req.Data}, nil
	})
	f.Fuzz(func(t *testing.T, body []byte) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(string(body))))
		if w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
			t.Fatalf("status %d for %q", w.Code, body)
		}
	})
}

// OnError hears of every request a handler could not answer as the function
// meant, with the cause - and of nothing else.
func TestOnError(t *testing.T) {
	var got []error
	h := proxy.Handlers{OnError: func(_ *http.Request, err error) { got = append(got, err) }}
	errDown := errors.New("database down")
	rpc := func(err error, data string) http.Handler {
		return h.RPC(func(*http.Request, proxy.RPCRequest) (proxy.RPCResult, error) {
			return proxy.RPCResult{Data: jsontext.Value(data)}, err
		})
	}
	refresh := h.Refresh(func(*http.Request, proxy.RefreshRequest) (proxy.RefreshResult, error) {
		return proxy.RefreshResult{}, &proxy.Disconnect{Code: 4501, Reason: "gone"}
	})
	cases := []struct {
		name   string
		h      http.Handler
		body   string
		status int
		want   string // in the error OnError gets; empty: not called
		is     error
	}{
		{"success", rpc(nil, `{}`), `{}`, http.StatusOK, "", nil},
		{"error the client gets", rpc(&proxy.Error{Code: 403, Message: "no"}, ``), `{}`, http.StatusOK, "", nil},
		{"other error", rpc(errDown, ``), `{}`, http.StatusInternalServerError, "proxy: rpc: database down", errDown},
		{"error the proxy cannot carry", refresh, `{}`, http.StatusInternalServerError, "the refresh proxy cannot answer with", nil},
		{"result not JSON", rpc(nil, `{not json`), `{}`, http.StatusInternalServerError, "encoding the rpc response", nil},
		{"malformed request", rpc(nil, `{}`), `{"user":`, http.StatusBadRequest, "malformed rpc request", nil},
	}
	for _, tc := range cases {
		got = nil
		status, _ := post(t, tc.h, tc.body)
		if status != tc.status {
			t.Errorf("%s: status %d, want %d", tc.name, status, tc.status)
		}
		switch {
		case tc.want == "" && len(got) != 0:
			t.Errorf("%s: OnError got %v", tc.name, got)
		case tc.want != "" && (len(got) != 1 || !strings.Contains(got[0].Error(), tc.want)):
			t.Errorf("%s: OnError got %v, want one containing %q", tc.name, got, tc.want)
		case tc.is != nil && !errors.Is(got[0], tc.is):
			t.Errorf("%s: %v does not wrap %v", tc.name, got[0], tc.is)
		}
	}
	// A request which is not a POST is not Centrifugo's: not reported.
	got = nil
	w := httptest.NewRecorder()
	rpc(nil, `{}`).ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	if w.Code != http.StatusMethodNotAllowed || len(got) != 0 {
		t.Errorf("GET: %d, OnError got %v", w.Code, got)
	}
}

func TestMaxRequestBytes(t *testing.T) {
	h := proxy.Handlers{MaxRequestBytes: 64}.RPC(func(*http.Request, proxy.RPCRequest) (proxy.RPCResult, error) {
		return proxy.RPCResult{}, nil
	})
	if status, _ := post(t, h, `{"data":"`+strings.Repeat("x", 40)+`"}`); status != http.StatusOK {
		t.Errorf("under the limit: status %d", status)
	}
	if status, _ := post(t, h, `{"data":"`+strings.Repeat("x", 80)+`"}`); status != http.StatusBadRequest {
		t.Errorf("over the limit: status %d, want 400", status)
	}
}
