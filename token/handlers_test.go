package token_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/centrifugal/gocent/v4/token"
)

func call(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body)))
	return w
}

// What the endpoints answer for each thing the function can return.
func TestHandlerAnswers(t *testing.T) {
	errDown := errors.New("database down")
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		onError bool
	}{
		{"token", nil, http.StatusOK, false},
		{"unauthorized", token.ErrUnauthorized, http.StatusForbidden, false},
		{"wrapped unauthorized", fmt.Errorf("session: %w", token.ErrUnauthorized), http.StatusForbidden, false},
		{"other error", errDown, http.StatusInternalServerError, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reported []error
			h := token.Handlers{OnError: func(_ *http.Request, err error) { reported = append(reported, err) }}
			conn := h.Connection(func(*http.Request) (string, error) { return "conn-token", tc.err })
			sub := h.Subscription(func(_ *http.Request, channel string) (string, error) { return "sub-token:" + channel, tc.err })
			for _, c := range []struct {
				h            http.Handler
				method, path string
				body, token  string
			}{
				{conn, http.MethodGet, "/", "", "conn-token"},
				{conn, http.MethodPost, "/", `{"anything":1}`, "conn-token"},
				{sub, http.MethodPost, "/", `{"channel":"chat:1"}`, "sub-token:chat:1"},
				{sub, http.MethodGet, "/?channel=chat%3A1", "", "sub-token:chat:1"},
			} {
				reported = nil
				w := call(t, c.h, c.method, c.path, c.body)
				if w.Code != tc.status {
					t.Fatalf("%s %s: status %d, want %d", c.method, c.path, w.Code, tc.status)
				}
				if tc.status == http.StatusOK {
					if got := strings.TrimSpace(w.Body.String()); got != `{"token":"`+c.token+`"}` {
						t.Errorf("%s %s: body %s", c.method, c.path, got)
					}
					if w.Header().Get("Cache-Control") != "no-store" {
						t.Errorf("Cache-Control %q", w.Header().Get("Cache-Control"))
					}
				}
				if (len(reported) > 0) != tc.onError || (tc.onError && !errors.Is(reported[0], errDown)) {
					t.Errorf("%s %s: OnError got %v", c.method, c.path, reported)
				}
			}
		})
	}
}

func TestHandlerBadRequests(t *testing.T) {
	var reported []error
	h := token.Handlers{OnError: func(_ *http.Request, err error) { reported = append(reported, err) }}
	never := func(*http.Request, string) (string, error) { t.Error("called for a bad request"); return "", nil }
	sub := h.Subscription(never)
	conn := h.Connection(func(*http.Request) (string, error) { t.Error("called for a bad request"); return "", nil })
	for _, hh := range []http.Handler{conn, sub} {
		if w := call(t, hh, http.MethodPut, "/", ""); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, POST" {
			t.Errorf("PUT: %d, Allow %q", w.Code, w.Header().Get("Allow"))
		}
	}
	for name, c := range map[string]struct{ method, target, body string }{
		"no channel in query": {http.MethodGet, "/", ""},
		"no channel in body":  {http.MethodPost, "/", `{}`},
		"not JSON":            {http.MethodPost, "/", `{`},
	} {
		reported = nil
		if w := call(t, sub, c.method, c.target, c.body); w.Code != http.StatusBadRequest || len(reported) != 1 {
			t.Errorf("%s: %d, OnError got %v", name, w.Code, reported)
		}
	}
}

func TestHandlerNilFunctionsPanic(t *testing.T) {
	for name, f := range map[string]func(){
		"connection":   func() { token.Handlers{}.Connection(nil) },
		"subscription": func() { token.Handlers{}.Subscription(nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			f()
		}()
	}
}
