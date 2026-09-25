package gocent_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCentrifugo answers like Centrifugo's HTTP API for the methods the tests
// use. Publishing into a channel starting with "bad" fails with code 102
// (unknown channel) and into one starting with "full" with code 108 (not
// available); everything else succeeds, with offsets counting up per channel.
type fakeCentrifugo struct {
	t      *testing.T
	server *httptest.Server
	key    string
	// bearer, when set, is the token accepted as "Authorization: Bearer".
	// As in Centrifugo PRO, a request with a bearer token is judged by it
	// alone, and one without falls back to the API key.
	bearer string

	// delay holds every request back this long, to let calls pile up.
	delay time.Duration

	mu       sync.Mutex
	offsets  map[string]uint64
	requests []fakeRequest

	inFlight    atomic.Int64
	maxInFlight atomic.Int64
}

type fakeRequest struct {
	Path   string
	Header http.Header
	Body   []byte
}

func newFake(t *testing.T) *fakeCentrifugo {
	t.Helper()
	f := &fakeCentrifugo{t: t, key: "secret", offsets: map[string]uint64{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeCentrifugo) addr() string { return f.server.URL + "/api" }

func (f *fakeCentrifugo) recorded() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRequest(nil), f.requests...)
}

func (f *fakeCentrifugo) serve(w http.ResponseWriter, r *http.Request) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		m := f.maxInFlight.Load()
		if n <= m || f.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Errorf("reading body: %v", err)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, fakeRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	authorized := r.Header.Get("X-API-Key") == f.key
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		authorized = f.bearer != "" && token == f.bearer
	}
	if !authorized {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	method, ok := strings.CutPrefix(r.URL.Path, "/api/")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var out any
	if method == "batch" {
		var req struct {
			Commands []map[string]jsontext.Value `json:"commands"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		replies := make([]map[string]any, len(req.Commands))
		for i, cmd := range req.Commands {
			if len(cmd) != 1 {
				f.t.Errorf("batch command %d has %d fields, want exactly one", i, len(cmd))
			}
			for m, params := range cmd {
				res, apiErr := f.handle(m, params)
				if apiErr != nil {
					replies[i] = map[string]any{"error": apiErr}
				} else {
					replies[i] = map[string]any{m: res}
				}
			}
		}
		out = map[string]any{"replies": replies}
	} else {
		res, apiErr := f.handle(method, body)
		if apiErr != nil {
			out = map[string]any{"error": apiErr}
		} else {
			out = map[string]any{"result": res}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.MarshalWrite(w, out); err != nil {
		f.t.Errorf("writing reply: %v", err)
	}
}

type fakeError struct {
	Code    uint32 `json:"code"`
	Message string `json:"message"`
}

func channelError(ch string) *fakeError {
	switch {
	case strings.HasPrefix(ch, "bad"):
		return &fakeError{Code: 102, Message: "unknown channel"}
	case strings.HasPrefix(ch, "full"):
		return &fakeError{Code: 108, Message: "not available"}
	}
	return nil
}

func (f *fakeCentrifugo) publish(ch string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offsets[ch]++
	return map[string]any{"offset": f.offsets[ch], "epoch": "e-" + ch}
}

func (f *fakeCentrifugo) handle(method string, params []byte) (any, *fakeError) {
	switch method {
	case "publish":
		var req struct {
			Channel string `json:"channel"`
		}
		if err := json.Unmarshal(params, &req); err != nil || req.Channel == "" {
			return nil, &fakeError{Code: 107, Message: "bad request"}
		}
		if e := channelError(req.Channel); e != nil {
			return nil, e
		}
		return f.publish(req.Channel), nil
	case "broadcast":
		var req struct {
			Channels []string `json:"channels"`
		}
		if err := json.Unmarshal(params, &req); err != nil || len(req.Channels) == 0 {
			return nil, &fakeError{Code: 107, Message: "bad request"}
		}
		if req.Channels[0] == "reject-all" {
			// Centrifugo refusing a broadcast as a whole.
			return nil, &fakeError{Code: 108, Message: "not available"}
		}
		responses := make([]map[string]any, len(req.Channels))
		for i, ch := range req.Channels {
			if e := channelError(ch); e != nil {
				responses[i] = map[string]any{"error": e}
				continue
			}
			responses[i] = map[string]any{"result": f.publish(ch)}
		}
		return map[string]any{"responses": responses}, nil
	case "presence_stats":
		return map[string]any{"num_clients": 3, "num_users": 2}, nil
	case "info":
		return map[string]any{"nodes": []map[string]any{{"uid": "n1", "name": "node", "num_clients": 1}}}, nil
	case "future_error":
		return nil, &fakeError{Code: 999, Message: "something new"}
	}
	return nil, &fakeError{Code: 104, Message: "not found"}
}
