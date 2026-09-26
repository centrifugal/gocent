package gocent

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config configures a [Client]. [New] validates it and reports every mistake
// it can see before any request is made.
type Config struct {
	// APIEndpoint is the base URL of Centrifugo's HTTP API, for example
	// "http://localhost:8000/api". Each method is sent to APIEndpoint plus
	// its name, such as "/publish". Exactly one of APIEndpoint and
	// APIEndpointFunc must be set.
	APIEndpoint string

	// APIEndpointFunc returns the base URL for each request, for when
	// Centrifugo's address is discovered rather than fixed. It must be safe
	// for concurrent use. Exactly one of APIEndpoint and APIEndpointFunc must
	// be set.
	APIEndpointFunc func(ctx context.Context) (string, error)

	// APIKey is sent with every request in the X-API-Key header. Leave it
	// and BearerTokenFunc unset when requests are authenticated some other
	// way - mutual TLS set up in HTTPClient, a proxy - or not at all.
	APIKey string

	// BearerTokenFunc returns the token sent with every request as
	// "Authorization: Bearer <token>", for Centrifugo PRO's JWKS
	// authentication of the API. It is called for each request, with that
	// request's context, so it should return a cached token and refresh it
	// before it expires - as an oauth2.TokenSource does. It must be safe for
	// concurrent use. An error from it fails the call before anything is
	// sent, and the error wraps it. It may not be set together with APIKey:
	// Centrifugo PRO judges a request with a bearer token by the token alone.
	BearerTokenFunc func(ctx context.Context) (string, error)

	// HTTPClient sends the requests. When nil, [DefaultHTTPClient] is used,
	// with a connection pool sized for concurrent use. http.DefaultClient is
	// never used: it keeps few connections to a host.
	//
	// However a request times out - its context's deadline, RequestTimeout,
	// or a timeout of this client's own, such as http.Client.Timeout - the
	// error matches errors.Is(err, context.DeadlineExceeded).
	HTTPClient *http.Client

	// RequestTimeout bounds each request whose context has no deadline of
	// its own. A context deadline always takes precedence. Zero means 10
	// seconds.
	RequestTimeout time.Duration

	// Header holds extra headers sent with every request, for a proxy in
	// front of Centrifugo for example. It may not set X-API-Key,
	// Authorization or Content-Type.
	Header http.Header

	// AutoBatch sends calls made concurrently in batches. See [AutoBatch].
	AutoBatch AutoBatch
}

// AutoBatch lets the client send concurrent calls together, as one batch
// request, once enough of them are in flight. The zero value disables it.
//
// Nothing waits for a batch to fill. While fewer than MaxInFlight requests are
// in flight, every call is sent at once, alone, exactly as without AutoBatch.
// Only a call made when MaxInFlight requests are already in flight waits - and
// it waits for one of them to finish, not for a timer. Then all the calls
// waiting at that moment go out together as a single batch. The busier the
// client, the larger the batches, and the fewer requests Centrifugo handles
// for the same work.
//
// Every call gets its own command's reply: its own result, or its own error.
// Calls made one after another keep their order: a call waits for its reply,
// so the next one can only join a later batch. The calls sharing a batch were
// made concurrently, with no order between them, and Centrifugo runs them in
// parallel.
//
// Calls sharing a batch share its request, though. When the batch request
// itself fails - the network, the credentials, a body too large for
// Centrifugo or a proxy in front of it - every call in it fails with that
// error. Keep payloads well within the body limit, or send large ones from a
// client without AutoBatch. A batch request is bounded by RequestTimeout, not
// by the deadlines of the calls in it: a call with a longer deadline can still
// fail after RequestTimeout once it went out in a batch.
type AutoBatch struct {
	// MaxInFlight is how many requests may be in flight at once before calls
	// start to wait and be batched. Zero disables AutoBatch.
	//
	// A smaller value batches more: calls are batched only once this many
	// requests are in flight, so a value above the concurrency the load needs
	// leaves nearly every call sent alone. A small value such as 8 suits most
	// applications.
	MaxInFlight int

	// MaxBatchSize caps how many calls one batch carries. Zero means 1000.
	MaxBatchSize int

	// GroupPublications asks Centrifugo PRO to send the publications of a
	// batch to its broker together rather than one by one, which saves
	// Centrifugo and Redis work. The saving grows with the batches: under
	// heavy load, when batches carry many publications, Redis does much less
	// work; under moderate load batches are small, and the gain is mostly
	// Centrifugo's. It changes two things: publications into
	// different channels may take effect in a different order than they were
	// made in, and an error for a group is reported to every publication in
	// it. Give publications an IdempotencyKey to retry them safely.
	// Centrifugo OSS ignores it.
	GroupPublications bool
}

const (
	defaultRequestTimeout = 10 * time.Second
	defaultMaxBatchSize   = 1000

	// Bodies of unexpected responses are read up to this many bytes for the
	// error, then drained and discarded up to maxDrainBytes so the connection
	// can be reused.
	maxErrorBodyBytes = 512
	maxDrainBytes     = 64 << 10
)

// DefaultHTTPClient returns the HTTP client a [Client] uses when
// Config.HTTPClient is nil: keep-alive connections, and a pool of up to 256
// idle ones to Centrifugo so that concurrent calls reuse them. It sets no
// timeouts of its own - each request's context bounds the whole request,
// connecting included, so a request has exactly one deadline. Each call
// returns a new client with its own connection pool.
func DefaultHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 256,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// Client calls Centrifugo's HTTP server API. It is safe for concurrent use,
// and meant to be created once and shared.
type Client struct {
	addr     string
	addrFunc func(context.Context) (string, error)
	token    func(context.Context) (string, error)
	header   http.Header
	http     *http.Client
	timeout  time.Duration
	batcher  *batcher
	group    bool
}

// New returns a Client for cfg, or an error wrapping [ErrInvalidConfig] that
// names what is wrong with it.
func New(cfg Config) (*Client, error) {
	c := &Client{
		addrFunc: cfg.APIEndpointFunc,
		token:    cfg.BearerTokenFunc,
		http:     cfg.HTTPClient,
		timeout:  cfg.RequestTimeout,
		group:    cfg.AutoBatch.GroupPublications,
	}
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
	}

	switch {
	case cfg.APIEndpoint == "" && cfg.APIEndpointFunc == nil:
		return nil, invalid("APIEndpoint is required, for example http://localhost:8000/api")
	case cfg.APIEndpoint != "" && cfg.APIEndpointFunc != nil:
		return nil, invalid("set APIEndpoint or APIEndpointFunc, not both")
	case cfg.APIEndpoint != "":
		addr, err := checkAddr(cfg.APIEndpoint)
		if err != nil {
			return nil, invalid("APIEndpoint: %v", err)
		}
		c.addr = addr
	}

	switch {
	case cfg.APIKey != "" && cfg.BearerTokenFunc != nil:
		return nil, invalid("set APIKey or BearerTokenFunc, not both: a request with a bearer token is judged by the token alone")
	case strings.ContainsAny(cfg.APIKey, "\r\n"):
		return nil, invalid("APIKey contains a line break")
	}

	c.header = make(http.Header, len(cfg.Header)+3)
	for k, v := range cfg.Header {
		switch http.CanonicalHeaderKey(k) {
		case "X-Api-Key":
			return nil, invalid("Header may not set X-API-Key; use APIKey")
		case "Authorization":
			return nil, invalid("Header may not set Authorization; use BearerTokenFunc")
		case "Content-Type":
			return nil, invalid("Header may not set Content-Type")
		}
		c.header[http.CanonicalHeaderKey(k)] = append([]string(nil), v...)
	}
	c.header.Set("Content-Type", "application/json")
	c.header.Set("User-Agent", "gocent/v4")
	if cfg.APIKey != "" {
		c.header.Set("X-API-Key", cfg.APIKey)
	}

	switch {
	case c.timeout < 0:
		return nil, invalid("RequestTimeout is negative")
	case c.timeout == 0:
		c.timeout = defaultRequestTimeout
	}
	if c.http == nil {
		c.http = DefaultHTTPClient()
	}

	ab := cfg.AutoBatch
	switch {
	case ab.MaxInFlight < 0:
		return nil, invalid("AutoBatch.MaxInFlight is negative")
	case ab.MaxBatchSize < 0:
		return nil, invalid("AutoBatch.MaxBatchSize is negative")
	case ab.MaxInFlight == 0 && (ab.MaxBatchSize != 0 || ab.GroupPublications):
		return nil, invalid("AutoBatch options are set but AutoBatch.MaxInFlight is zero, which disables it")
	case ab.MaxBatchSize == 1:
		return nil, invalid("AutoBatch.MaxBatchSize of 1 never batches; use 2 or more")
	case ab.MaxInFlight > 0:
		size := ab.MaxBatchSize
		if size == 0 {
			size = defaultMaxBatchSize
		}
		c.batcher = &batcher{client: c, maxInFlight: ab.MaxInFlight, maxBatch: size}
	}
	return c, nil
}

// checkAddr rejects addresses which would send requests somewhere other than
// the intended API, and returns it without a trailing slash.
func checkAddr(addr string) (string, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return "", err
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "", fmt.Errorf("%q must be an absolute http:// or https:// URL", addr)
	case u.Host == "":
		return "", fmt.Errorf("%q has no host", addr)
	case u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return "", fmt.Errorf("%q must not have a query, fragment or credentials; use APIKey", addr)
	}
	path := strings.TrimRight(u.Path, "/")
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		if _, isMethod := methodNames[path[i+1:]]; isMethod {
			return "", fmt.Errorf("%q ends with the method %q; APIEndpoint is the API base URL, such as http://localhost:8000/api", addr, path[i+1:])
		}
	}
	u.Path = path
	return u.String(), nil
}

// request is implemented by every request type: it names its method and knows
// its place in a batch.
type request interface {
	APIMethod() string
	addTo(*command)
}

// response is the envelope every method replies with.
type response[T any] struct {
	Error  *Error `json:"error,omitzero"`
	Result *T     `json:"result,omitzero"`
}

// invoke sends req and returns its result. W is the result as it is decoded
// off the wire; get finds it in a batch reply.
func invoke[W any](ctx context.Context, c *Client, req request, get func(*reply) *W) (W, error) {
	if err := validateRequest(req); err != nil {
		var zero W
		return zero, err
	}
	if c.batcher != nil {
		return batched(ctx, c.batcher, req, get)
	}
	return send(ctx, c, req, func(r *response[W]) (W, error) {
		if r.Error != nil {
			var zero W
			return zero, r.Error
		}
		return deref(r.Result), nil
	})
}

// send posts req to its method's endpoint and decodes the reply.
func send[W any](ctx context.Context, c *Client, req request, done func(*response[W]) (W, error)) (W, error) {
	var resp response[W]
	var zero W
	if err := c.post(ctx, req.APIMethod(), req, &resp); err != nil {
		return zero, err
	}
	return done(&resp)
}

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// maxPooledBuffer keeps a single huge request from pinning its buffer in the
// pool for good.
const maxPooledBuffer = 1 << 20

// post sends body to the method's endpoint and decodes the reply into out.
func (c *Client) post(ctx context.Context, method string, body, out any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	base := c.addr
	if c.addrFunc != nil {
		addr, err := c.addrFunc(ctx)
		if err != nil {
			return fmt.Errorf("gocent: APIEndpointFunc: %w", err)
		}
		if base, err = checkAddr(addr); err != nil {
			return fmt.Errorf("gocent: APIEndpointFunc: %w", err)
		}
	}
	var auth string
	if c.token != nil {
		token, err := c.token(ctx)
		if err != nil {
			return fmt.Errorf("gocent: bearer token: %w", err)
		}
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return errors.New("gocent: bearer token: BearerTokenFunc returned an empty token or one with a line break")
		}
		auth = "Bearer " + token
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= maxPooledBuffer {
			bufPool.Put(buf)
		}
	}()
	if err := json.MarshalWrite(buf, body); err != nil {
		return fmt.Errorf("gocent: encoding %s request: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+method, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return fmt.Errorf("gocent: %w", err)
	}
	req.Header = c.header.Clone()
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return transportError(method, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		// With http_api.error_mode "transport", or the X-Centrifugo-Error-Mode
		// header, Centrifugo replies to a failed call with an HTTP status and
		// the error alone as the body. It is the same error as in a 200 reply.
		var apiErr Error
		if json.Unmarshal(b, &apiErr) == nil && apiErr.Code != 0 {
			return &apiErr
		}
		return &HTTPError{StatusCode: resp.StatusCode, Body: bytes.TrimSpace(b)}
	}
	if err := json.UnmarshalRead(resp.Body, out); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
			// The body stopped arriving because the context ended.
			return fmt.Errorf("gocent: %s: %w", method, ctxErr)
		}
		if isTimeout(err) {
			return transportError(method, err)
		}
		return &DecodeError{Err: err}
	}
	return nil
}

// transportError wraps an error from sending a request or reading its reply.
// A timeout of the HTTP client's own - a dial timeout, http.Client.Timeout -
// is made to match context.DeadlineExceeded too, so that a caller has one
// check for "the request timed out" whichever layer's clock ran out.
func transportError(method string, err error) error {
	if isTimeout(err) && !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("gocent: %s: %w (%w)", method, context.DeadlineExceeded, err)
	}
	return fmt.Errorf("gocent: %s: %w", method, err)
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
