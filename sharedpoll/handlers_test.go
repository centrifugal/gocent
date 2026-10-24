package sharedpoll_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/centrifugal/gocent/v4/sharedpoll"
)

func call(t *testing.T, h http.Handler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), method, "/", strings.NewReader(body)))
	return w
}

// What the endpoint answers for each thing the function can return.
func TestSignature(t *testing.T) {
	errDown := errors.New("database down")
	cases := []struct {
		name    string
		allowed []string
		sig     string
		err     error
		status  int
		body    string
		onError bool
	}{
		{"all keys", []string{"k1", "k2"}, "sig", nil, http.StatusOK, `{"signature":"sig","keys":["k1","k2"]}`, false},
		{"some revoked", []string{"k2"}, "sig", nil, http.StatusOK, `{"signature":"sig","keys":["k2"]}`, false},
		{"all revoked", nil, "", nil, http.StatusOK, `{"signature":"","keys":[]}`, false},
		{"unauthorized", nil, "", sharedpoll.ErrUnauthorized, http.StatusForbidden, "", false},
		{"wrapped unauthorized", nil, "", fmt.Errorf("session: %w", sharedpoll.ErrUnauthorized), http.StatusForbidden, "", false},
		{"other error", nil, "", errDown, http.StatusInternalServerError, "", true},
		{"key not asked for", []string{"k1", "extra"}, "sig", nil, http.StatusInternalServerError, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotChannel string
			var gotKeys []string
			var reported []error
			h := sharedpoll.Handlers{OnError: func(_ *http.Request, err error) { reported = append(reported, err) }}.Signature(
				func(_ *http.Request, channel string, keys []string) ([]string, string, error) {
					gotChannel, gotKeys = channel, keys
					return tc.allowed, tc.sig, tc.err
				})
			w := call(t, h, http.MethodPost, `{"channel":"votes:1","keys":["k1","k2"]}`)
			if gotChannel != "votes:1" || strings.Join(gotKeys, ",") != "k1,k2" {
				t.Errorf("function got %q %q", gotChannel, gotKeys)
			}
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d", w.Code, tc.status)
			}
			if tc.body != "" {
				if got := strings.TrimSpace(w.Body.String()); got != tc.body {
					t.Errorf("body %s, want %s", got, tc.body)
				}
				if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
					t.Errorf("headers %v", w.Header())
				}
			}
			if (len(reported) > 0) != tc.onError {
				t.Errorf("OnError got %v", reported)
			}
			if errors.Is(tc.err, errDown) && (len(reported) != 1 || !errors.Is(reported[0], errDown)) {
				t.Errorf("OnError got %v, want the function's error", reported)
			}
		})
	}
}

func TestSignatureBadRequests(t *testing.T) {
	var reported []error
	h := sharedpoll.Handlers{OnError: func(_ *http.Request, err error) { reported = append(reported, err) }}.Signature(
		func(*http.Request, string, []string) ([]string, string, error) {
			t.Error("called for a bad request")
			return nil, "", nil
		})
	if w := call(t, h, http.MethodGet, ""); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
		t.Errorf("GET: %d, Allow %q", w.Code, w.Header().Get("Allow"))
	}
	for name, body := range map[string]string{
		"not JSON":   `{`,
		"no channel": `{"keys":["k"]}`,
		"too large":  `{"channel":"c","keys":["` + strings.Repeat("k", 2<<20) + `"]}`,
	} {
		reported = nil
		if w := call(t, h, http.MethodPost, body); w.Code != http.StatusBadRequest || len(reported) != 1 {
			t.Errorf("%s: %d, OnError got %v", name, w.Code, reported)
		}
	}
}

func TestSignatureNilFunctionPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	sharedpoll.Handlers{}.Signature(nil)
}
