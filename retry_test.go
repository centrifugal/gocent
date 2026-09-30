package gocent_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
)

func TestRetryable(t *testing.T) {
	broadcast := func(errs ...error) *gocent.BroadcastError {
		be := &gocent.BroadcastError{Total: len(errs) + 1}
		for i, err := range errs {
			be.Failed = append(be.Failed, gocent.ChannelResult{Channel: fmt.Sprint("ch", i), Err: err})
		}
		return be
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"internal, marked temporary", &gocent.Error{Code: 100, Temporary: true}, true},
		{"internal, from a Centrifugo which does not mark", &gocent.Error{Code: 100}, true},
		{"too many requests", &gocent.Error{Code: 111, Temporary: true}, true},
		{"a new code marked temporary", &gocent.Error{Code: 4001, Temporary: true}, true},
		{"unknown channel", &gocent.Error{Code: 102}, false},
		{"bad request", &gocent.Error{Code: 107}, false},
		{"conflict", &gocent.Error{Code: 113}, false},
		{"a new code not marked", &gocent.Error{Code: 4001}, false},
		{"wrapped", fmt.Errorf("gocent: publish: %w", &gocent.Error{Code: 100}), true},
		{"HTTP 503", &gocent.HTTPError{StatusCode: http.StatusServiceUnavailable}, true},
		{"HTTP 429", &gocent.HTTPError{StatusCode: http.StatusTooManyRequests}, true},
		{"HTTP 401", &gocent.HTTPError{StatusCode: http.StatusUnauthorized}, false},
		{"HTTP 404", &gocent.HTTPError{StatusCode: http.StatusNotFound}, false},
		{"timeout", fmt.Errorf("gocent: publish: %w", context.DeadlineExceeded), true},
		{"cancelled", fmt.Errorf("gocent: publish: %w", context.Canceled), false},
		{"not Centrifugo's reply", &gocent.DecodeError{Err: errors.New("invalid character")}, false},
		{"invalid request", fmt.Errorf("batch command #1: %w", gocent.ErrInvalidRequest), false},
		{"invalid config", gocent.ErrInvalidConfig, false},
		{"batch sent twice", gocent.ErrBatchSent, false},
		{"connection refused", &url.Error{Op: "Post", URL: "http://x/api", Err: errors.New("connect: connection refused")}, true},
		{"invalid certificate", &url.Error{Op: "Post", URL: "https://x/api", Err: &tls.CertificateVerificationError{Err: errors.New("unknown authority")}}, false},
		{"broadcast, one channel temporary", broadcast(&gocent.Error{Code: 102}, &gocent.Error{Code: 100}), true},
		{"broadcast, only permanent", broadcast(&gocent.Error{Code: 102}, &gocent.Error{Code: 107}), false},
		{"batch holding a temporary broadcast", &gocent.BatchError{Total: 2, Failed: []gocent.CommandError{
			{Index: 0, Method: "publish", Err: &gocent.Error{Code: 107}},
			{Index: 1, Method: "broadcast", Err: broadcast(&gocent.Error{Code: 100})},
		}}, true},
		{"batch, only permanent", &gocent.BatchError{Total: 1, Failed: []gocent.CommandError{
			{Index: 0, Method: "publish", Err: &gocent.Error{Code: 102}},
		}}, false},
	} {
		if got := gocent.Retryable(tc.err); got != tc.want {
			t.Errorf("%s: Retryable(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// Errors the client really returns, not built by hand.
func TestRetryableFromClient(t *testing.T) {
	// Nothing listens: the connection is refused.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	c, err := gocent.New(gocent.Config{APIEndpoint: addr + "/api", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data})
	if err == nil || !gocent.Retryable(err) {
		t.Errorf("refused connection: Retryable(%v) = false", err)
	}

	// A malformed address from APIEndpointFunc is a mistake, not the
	// network's failure.
	c2, err := gocent.New(gocent.Config{
		APIEndpointFunc: func(context.Context) (string, error) { return "http://[bad", nil },
		APIKey:          "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c2.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data})
	if err == nil || gocent.Retryable(err) {
		t.Errorf("malformed address: Retryable(%v) = true", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = c.Publish(ctx, gocent.PublishRequest{Channel: "news", Data: data})
	if err == nil || gocent.Retryable(err) {
		t.Errorf("cancelled context: Retryable(%v) = true", err)
	}

	// A reply slower than the request timeout. The handler is released when
	// the test ends, so that closing the server does not wait for it.
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer slow.Close()
	defer close(release)
	c, err = gocent.New(gocent.Config{APIEndpoint: slow.URL + "/api", APIKey: "k", RequestTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Publish(t.Context(), gocent.PublishRequest{Channel: "news", Data: data})
	if err == nil || !gocent.Retryable(err) {
		t.Errorf("timeout: Retryable(%v) = false", err)
	}
}
