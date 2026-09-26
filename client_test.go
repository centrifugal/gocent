package gocent_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
)

func newClient(t *testing.T, f *fakeCentrifugo, mutate ...func(*gocent.Config)) *gocent.Client {
	t.Helper()
	cfg := gocent.Config{APIEndpoint: f.addr(), APIKey: f.key}
	for _, m := range mutate {
		m(&cfg)
	}
	c, err := gocent.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewRejectsMistakes(t *testing.T) {
	addrFunc := func(context.Context) (string, error) { return "http://x/api", nil }
	tokenFunc := func(context.Context) (string, error) { return "t", nil }
	cases := []struct {
		name string
		cfg  gocent.Config
		want string
	}{
		{"no address", gocent.Config{APIKey: "k"}, "APIEndpoint is required"},
		{"both addresses", gocent.Config{APIEndpoint: "http://x/api", APIEndpointFunc: addrFunc, APIKey: "k"}, "not both"},
		{"relative address", gocent.Config{APIEndpoint: "localhost:8000/api", APIKey: "k"}, "absolute"},
		{"no scheme", gocent.Config{APIEndpoint: "/api", APIKey: "k"}, "absolute"},
		{"query", gocent.Config{APIEndpoint: "http://x/api?k=1", APIKey: "k"}, "query"},
		{"credentials", gocent.Config{APIEndpoint: "http://u:p@x/api", APIKey: "k"}, "credentials"},
		{"method in address", gocent.Config{APIEndpoint: "http://x/api/publish", APIKey: "k"}, "ends with the method"},
		{"key and token", gocent.Config{APIEndpoint: "http://x/api", APIKey: "k", BearerTokenFunc: tokenFunc}, "not both"},
		{"header sets authorization", gocent.Config{APIEndpoint: "http://x/api", BearerTokenFunc: tokenFunc, Header: http.Header{"authorization": {"Bearer x"}}}, "use BearerTokenFunc"},
		{"key with newline", gocent.Config{APIEndpoint: "http://x/api", APIKey: "k\n"}, "line break"},
		{"negative timeout", gocent.Config{APIEndpoint: "http://x/api", APIKey: "k", RequestTimeout: -time.Second}, "negative"},
		{"header sets key", gocent.Config{APIEndpoint: "http://x/api", APIKey: "k", Header: http.Header{"x-api-key": {"other"}}}, "X-API-Key"},
		{"batch size without batching", gocent.Config{APIEndpoint: "http://x/api", APIKey: "k", AutoBatch: gocent.AutoBatch{MaxBatchSize: 10}}, "disables it"},
		{"grouping without batching", gocent.Config{APIEndpoint: "http://x/api", APIKey: "k", AutoBatch: gocent.AutoBatch{GroupPublications: true}}, "disables it"},
		{"batch of one", gocent.Config{APIEndpoint: "http://x/api", APIKey: "k", AutoBatch: gocent.AutoBatch{MaxInFlight: 4, MaxBatchSize: 1}}, "never batches"},
		{"negative in flight", gocent.Config{APIEndpoint: "http://x/api", APIKey: "k", AutoBatch: gocent.AutoBatch{MaxInFlight: -1}}, "negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := gocent.New(tc.cfg)
			if err == nil {
				t.Fatalf("New accepted the config, returned %v", c)
			}
			if !errors.Is(err, gocent.ErrInvalidConfig) {
				t.Errorf("error %q does not wrap ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestNewAcceptsValidConfigs(t *testing.T) {
	for _, cfg := range []gocent.Config{
		{APIEndpoint: "http://localhost:8000/api", APIKey: "k"},
		{APIEndpoint: "https://example.com/centrifugo/api/", APIKey: "k"},
		{APIEndpoint: "http://localhost:8000/api"}, // mutual TLS, a proxy, or http_api.insecure
		{APIEndpoint: "http://localhost:8000/api", BearerTokenFunc: func(context.Context) (string, error) { return "t", nil }},
		{APIEndpoint: "http://localhost:8000/api", APIKey: "k", AutoBatch: gocent.AutoBatch{MaxInFlight: 8, MaxBatchSize: 100, GroupPublications: true}},
	} {
		if _, err := gocent.New(cfg); err != nil {
			t.Errorf("New(%+v): %v", cfg, err)
		}
	}
}

func TestPublish(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f, func(cfg *gocent.Config) {
		cfg.Header = http.Header{"X-Tenant": {"acme"}}
	})
	res, err := c.Publish(t.Context(), gocent.PublishRequest{
		Channel:        "news",
		Data:           jsontext.Value(`{"text":"hello"}`),
		IdempotencyKey: "k1",
		Tags:           map[string]string{"kind": "update"},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.Offset != 1 || res.Epoch != "e-news" {
		t.Errorf("result %+v, want offset 1 epoch e-news", res)
	}

	reqs := f.recorded()
	if len(reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Path != "/api/publish" {
		t.Errorf("path %q, want /api/publish", r.Path)
	}
	for k, want := range map[string]string{
		"X-Api-Key": "secret", "Content-Type": "application/json", "User-Agent": "gocent/v4", "X-Tenant": "acme",
	} {
		if got := r.Header.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatal(err)
	}
	// Only set fields travel: zero values are omitted.
	want := map[string]any{
		"channel": "news", "data": map[string]any{"text": "hello"},
		"idempotency_key": "k1", "tags": map[string]any{"kind": "update"},
	}
	if got, _ := json.Marshal(body, json.Deterministic(true)); string(got) != mustMarshal(t, want) {
		t.Errorf("body %s, want %s", got, mustMarshal(t, want))
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAPIErrors(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)

	_, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "bad:1", Data: jsontext.Value(`{}`)})
	if !errors.Is(err, gocent.ErrUnknownChannel) {
		t.Fatalf("error %v is not ErrUnknownChannel", err)
	}
	if errors.Is(err, gocent.ErrNotAvailable) {
		t.Error("error matched a different code")
	}
	var apiErr *gocent.Error
	if !errors.As(err, &apiErr) || apiErr.Code != 102 {
		t.Fatalf("errors.As gave %+v", apiErr)
	}
	if err.Error() != "gocent: unknown channel (code 102)" {
		t.Errorf("message %q", err)
	}

	// A code this package has no value for is still an *Error.
	_, err = c.Info(t.Context())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
}

func TestUnknownErrorCode(t *testing.T) {
	var apiErr *gocent.Error
	err := error(&gocent.Error{Code: 999, Message: "something new"})
	if !errors.As(err, &apiErr) || apiErr.Code != 999 {
		t.Fatal("unknown code not reachable through errors.As")
	}
	for _, known := range []*gocent.Error{gocent.ErrInternal, gocent.ErrUnknownChannel, gocent.ErrNotFound, gocent.ErrBadRequest, gocent.ErrNotAvailable, gocent.ErrUnrecoverablePosition, gocent.ErrConflict} {
		if errors.Is(err, known) {
			t.Errorf("code 999 matched %v", known)
		}
	}
}

func TestHTTPErrors(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f, func(cfg *gocent.Config) { cfg.APIKey = "wrong" })
	_, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{}`)})
	if !errors.Is(err, gocent.ErrUnauthorized) {
		t.Fatalf("error %v is not ErrUnauthorized", err)
	}
	var httpErr *gocent.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("errors.As gave %+v", httpErr)
	}
}

func TestNotCentrifugo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>login</html>"))
	}))
	defer srv.Close()
	c, err := gocent.New(gocent.Config{APIEndpoint: srv.URL + "/api", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{}`)})
	var decErr *gocent.DecodeError
	if !errors.As(err, &decErr) {
		t.Fatalf("error %v is not a DecodeError", err)
	}
}

func TestInvalidJSONIsNotSent(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f)
	_, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{"broken"`)})
	if !errors.Is(err, gocent.ErrInvalidRequest) || err.Error() != "gocent: invalid publish request: Data is not valid JSON" {
		t.Fatalf("error %v, want ErrInvalidRequest for Data", err)
	}
	if n := len(f.recorded()); n != 0 {
		t.Errorf("%d requests sent, want none", n)
	}
}

func TestRequestTimeout(t *testing.T) {
	f := newFake(t)
	f.delay = 200 * time.Millisecond
	c := newClient(t, f, func(cfg *gocent.Config) { cfg.RequestTimeout = 20 * time.Millisecond })

	// No deadline on the context: RequestTimeout applies.
	_, err := c.Publish(context.Background(), gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{}`)})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v, want DeadlineExceeded", err)
	}

	// A deadline on the context wins over RequestTimeout.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := c.Publish(ctx, gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{}`)}); err != nil {
		t.Fatalf("with a longer context deadline: %v", err)
	}
}

func TestAddrFunc(t *testing.T) {
	f := newFake(t)
	calls := 0
	c, err := gocent.New(gocent.Config{
		APIEndpointFunc: func(context.Context) (string, error) { calls++; return f.addr(), nil },
		APIKey:          f.key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PresenceStats(t.Context(), gocent.PresenceStatsRequest{Channel: "news"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("APIEndpointFunc called %d times, want 1", calls)
	}
}

func TestAPIMethod(t *testing.T) {
	for _, tc := range []struct {
		req  interface{ APIMethod() string }
		want string
	}{
		{gocent.PublishRequest{}, "publish"},
		{gocent.BroadcastRequest{}, "broadcast"},
		{gocent.PresenceStatsRequest{}, "presence_stats"},
		{gocent.MapReadStateRequest{}, "map_read_state"},
	} {
		if got := tc.req.APIMethod(); got != tc.want {
			t.Errorf("%T.APIMethod() = %q, want %q", tc.req, got, tc.want)
		}
	}
}

func TestErrorMessages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&gocent.Error{Code: 102, Message: "unknown channel"}, "gocent: unknown channel (code 102)"},
		{&gocent.Error{Code: 999}, "gocent: server error (code 999)"},
		{&gocent.HTTPError{StatusCode: 502, Body: []byte("bad gateway")}, "gocent: unexpected HTTP status 502: bad gateway"},
		{&gocent.HTTPError{StatusCode: 401}, "gocent: unexpected HTTP status 401 (check the credentials: APIKey or BearerTokenFunc)"},
		{&gocent.HTTPError{StatusCode: 404, Body: []byte("404 page not found")}, "gocent: unexpected HTTP status 404 (is APIEndpoint the API base URL, such as http://localhost:8000/api?): 404 page not found"},
		{&gocent.DecodeError{Err: errors.New("invalid character")}, "gocent: decoding response: invalid character"},
		{&gocent.BatchError{Total: 3, Failed: []gocent.CommandError{
			{Index: 0, Method: "publish", Err: gocent.ErrUnknownChannel},
			{Index: 2, Method: "history", Err: gocent.ErrNotAvailable},
		}}, "gocent: batch: 2 of 3 commands failed: #0 publish: unknown channel (code 102); #2 history: not available (code 108)"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("%T message\n got %q\nwant %q", tc.err, got, tc.want)
		}
	}
}

func TestInvalidRequestsAreNotSent(t *testing.T) {
	f := newFake(t)
	c := newClient(t, f, autoBatch(gocent.AutoBatch{MaxInFlight: 2}))
	for _, tc := range []struct {
		call func() error
		want string
	}{
		{func() error { _, err := c.Publish(t.Context(), gocent.PublishRequest{Data: data}); return err },
			"gocent: invalid publish request: Channel is required"},
		{func() error { _, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news"}); return err },
			"gocent: invalid publish request: Data or B64Data is required"},
		{func() error { _, err := c.Broadcast(t.Context(), gocent.BroadcastRequest{Data: data}); return err },
			"gocent: invalid broadcast request: Channels is required"},
		{func() error {
			_, err := c.Broadcast(t.Context(), gocent.BroadcastRequest{Channels: []string{"a", ""}, Data: data})
			return err
		}, "gocent: invalid broadcast request: Channels[1] is empty"},
	} {
		err := tc.call()
		if !errors.Is(err, gocent.ErrInvalidRequest) || err.Error() != tc.want {
			t.Errorf("error %v\nwant %q", err, tc.want)
		}
	}
	// B64Data alone is enough.
	if _, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", B64Data: "aGk="}); err != nil {
		t.Errorf("publish with B64Data: %v", err)
	}

	b := c.NewBatch(gocent.BatchOptions{})
	pending := b.Publish(gocent.PublishRequest{Channel: "news", Data: data})
	b.Publish(gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{`)})
	err := b.Send(t.Context())
	if !errors.Is(err, gocent.ErrInvalidRequest) || !strings.Contains(err.Error(), "batch command #1") {
		t.Fatalf("batch with an invalid command: %v", err)
	}
	if _, err := pending.Result(); !errors.Is(err, gocent.ErrInvalidRequest) {
		t.Errorf("Result of a batch not sent for an invalid command: %v", err)
	}
	if got := len(f.recorded()); got != 1 {
		t.Errorf("%d requests sent, want only the B64Data publish", got)
	}
}

// However a request times out, errors.Is(err, context.DeadlineExceeded) says
// so - including a timeout of a caller's own HTTP client, which net/http does
// not report as a context error.
func TestHTTPClientTimeoutIsDeadlineExceeded(t *testing.T) {
	f := newFake(t)
	f.delay = 200 * time.Millisecond
	c := newClient(t, f, func(cfg *gocent.Config) {
		cfg.HTTPClient = &http.Client{Timeout: 20 * time.Millisecond}
	})
	_, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v, want DeadlineExceeded", err)
	}
	var netErr interface{ Timeout() bool }
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("the original timeout error is lost: %v", err)
	}
}

func bearerClient(t *testing.T, f *fakeCentrifugo, token func(context.Context) (string, error), mutate ...func(*gocent.Config)) *gocent.Client {
	t.Helper()
	return newClient(t, f, append([]func(*gocent.Config){func(cfg *gocent.Config) {
		cfg.APIKey = ""
		cfg.BearerTokenFunc = token
	}}, mutate...)...)
}

func TestBearerToken(t *testing.T) {
	f := newFake(t)
	f.bearer = "tok-1"
	var calls atomic.Int64
	c := bearerClient(t, f, func(context.Context) (string, error) {
		calls.Add(1)
		return "tok-1", nil
	})
	if _, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := c.Info(t.Context()); err != nil {
		t.Fatalf("Info: %v", err)
	}
	// Asked for every request, so a refreshed token is used at once.
	if n := calls.Load(); n != 2 {
		t.Errorf("BearerTokenFunc called %d times, want 2", n)
	}
	for _, r := range f.recorded() {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
			t.Errorf("Authorization %q", got)
		}
		if r.Header.Get("X-API-Key") != "" {
			t.Error("an API key was sent with a bearer token")
		}
	}
}

func TestBearerTokenRejected(t *testing.T) {
	f := newFake(t)
	f.bearer = "tok-1"
	c := bearerClient(t, f, func(context.Context) (string, error) { return "expired", nil })
	_, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data})
	if !errors.Is(err, gocent.ErrUnauthorized) {
		t.Fatalf("error %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), "expired") {
		t.Errorf("the token leaked into the error: %v", err)
	}
}

func TestBearerTokenErrorSendsNothing(t *testing.T) {
	f := newFake(t)
	cause := errors.New("identity provider down")
	c := bearerClient(t, f, func(context.Context) (string, error) { return "", cause })
	_, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data})
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "bearer token") {
		t.Fatalf("error %v, want one wrapping the token error", err)
	}
	if n := len(f.recorded()); n != 0 {
		t.Errorf("%d requests sent without a token", n)
	}
}

func TestBearerTokenMalformedSendsNothing(t *testing.T) {
	for _, token := range []string{"", "tok\r\nX-Injected: 1"} {
		f := newFake(t)
		c := bearerClient(t, f, func(context.Context) (string, error) { return token, nil })
		if _, err := c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data}); err == nil {
			t.Errorf("token %q accepted", token)
		}
		if n := len(f.recorded()); n != 0 {
			t.Errorf("token %q: %d requests sent", token, n)
		}
	}
}

// The token is asked for with the request's context, so a caller's deadline
// bounds fetching it too.
func TestBearerTokenGetsRequestContext(t *testing.T) {
	f := newFake(t)
	f.bearer = "tok-1"
	c := bearerClient(t, f, func(ctx context.Context) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			return "", errors.New("no deadline")
		}
		if v, _ := ctx.Value(ctxKey{}).(string); v != "caller" {
			return "", errors.New("not the caller's context")
		}
		return "tok-1", nil
	})
	ctx := context.WithValue(t.Context(), ctxKey{}, "caller")
	if _, err := c.Publish(ctx, gocent.PublishRequest{Channel: "news", Data: data}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

type ctxKey struct{}

// An automatic batch asks for its token on its own context, not a caller's:
// a batch outlives the callers whose commands it carries.
func TestBearerTokenAutoBatch(t *testing.T) {
	f := newFake(t)
	f.bearer = "tok-1"
	f.delay = 20 * time.Millisecond
	var fromCaller atomic.Int64
	c := bearerClient(t, f, func(ctx context.Context) (string, error) {
		if ctx.Value(ctxKey{}) != nil {
			fromCaller.Add(1)
		}
		return "tok-1", nil
	}, func(cfg *gocent.Config) { cfg.AutoBatch = gocent.AutoBatch{MaxInFlight: 1} })

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			ctx := context.WithValue(t.Context(), ctxKey{}, "caller")
			if _, err := c.Publish(ctx, gocent.PublishRequest{Channel: "news", Data: data}); err != nil {
				t.Errorf("Publish: %v", err)
			}
		})
	}
	wg.Wait()
	var batches int
	for _, r := range f.recorded() {
		if r.Path == "/api/batch" {
			batches++
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
			t.Errorf("%s: Authorization %q", r.Path, got)
		}
	}
	if batches == 0 {
		t.Fatal("no batch was sent; the test did not exercise AutoBatch")
	}
	// Only the direct request may have used a caller's context.
	if n := fromCaller.Load(); n > int64(len(f.recorded())-batches) {
		t.Errorf("%d tokens fetched with a caller's context, %d direct requests", n, len(f.recorded())-batches)
	}
}

// In its transport error mode Centrifugo replies to a failed call with an HTTP
// status and the error alone as the body. That is still a Centrifugo error.
func TestTransportErrorMode(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   *gocent.Error
	}{
		{http.StatusNotFound, `{"code":102,"message":"unknown channel"}`, gocent.ErrUnknownChannel},
		{http.StatusBadRequest, `{"code":107,"message":"bad request"}`, gocent.ErrBadRequest},
		// A code gocent has no value for.
		{http.StatusInternalServerError, `{"code":4001,"message":"custom"}`, &gocent.Error{Code: 4001}},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		c, err := gocent.New(gocent.Config{APIEndpoint: srv.URL + "/api", APIKey: "k"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data})
		var apiErr *gocent.Error
		if !errors.As(err, &apiErr) || !errors.Is(err, tc.want) {
			t.Errorf("%d %s: got %v, want %v", tc.status, tc.body, err, tc.want)
		}
		srv.Close()
	}
}

// A body which is not a Centrifugo error - a proxy's page, a router's 404 -
// stays an HTTPError.
func TestNonCentrifugoErrorBody(t *testing.T) {
	for _, body := range []string{"404 page not found", `{"error":"nope"}`, `{"code":0}`, ""} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(body))
		}))
		c, err := gocent.New(gocent.Config{APIEndpoint: srv.URL + "/api", APIKey: "k"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data})
		var httpErr *gocent.HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
			t.Errorf("body %q: got %v, want an HTTPError", body, err)
		}
		srv.Close()
	}
}
