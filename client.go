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
	// be set. With AutoBatch, a batch request is not any one caller's: its
	// context has no values of the callers' contexts.
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
	// With AutoBatch, a batch request is not any one caller's: its context
	// has no values of the callers' contexts, so a token must not depend on
	// them.
	BearerTokenFunc func(ctx context.Context) (string, error)

	// HTTPClient sends the requests. When nil, [DefaultHTTPClient] is used,
	// with a connection pool sized for concurrent use. http.DefaultClient is
	// never used: it keeps few connections to a host.
	//
	// Redirects are not followed: Centrifugo's API never redirects, so one
	// means APIEndpoint is wrong - http where a proxy wants https, say - and
	// following it would send the API key to wherever it points, and turn a
	// publication into a GET without a body. The redirect is returned as an
	// [HTTPError]. A client with a CheckRedirect of its own is left to it;
	// otherwise New uses a copy of the client which refuses redirects.
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
	// Authorization or Content-Type. A User-Agent set here replaces
	// gocent's own, and a Host is sent as the request's host instead of the
	// one of APIEndpoint.
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
// client without AutoBatch. Each call keeps its own deadline in a batch: the
// batch request lasts until the latest deadline of the calls in it, and a
// call without one gets RequestTimeout, as when sent alone. The values of a
// caller's context do not reach a batch request, though: not the
// BearerTokenFunc or APIEndpointFunc, and not an HTTP transport which reads
// them, for tracing say.
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
}

const (
	defaultRequestTimeout = 10 * time.Second
	defaultMaxBatchSize   = 1000

	// Bodies of unexpected responses are read up to this many bytes for the
	// error, then drained and discarded up to maxDrainBytes so the connection
	// can be reused.
	maxErrorBodyBytes = 512
	// A Centrifugo error in the body of an HTTP error status is read up to
	// this many bytes: its message can be longer than the diagnostic excerpt.
	maxAPIErrorBytes = 4 << 10
	maxDrainBytes    = 64 << 10
)

// DefaultHTTPClient returns the HTTP client a [Client] uses when
// Config.HTTPClient is nil: keep-alive connections, and a pool of up to 256
// idle ones to Centrifugo so that concurrent calls reuse them. It sets no
// timeouts of its own - each request's context bounds the whole request,
// connecting included, so a request has exactly one deadline. Each call
// returns a new client with its own connection pool.
func DefaultHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: refuseRedirect,
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

// refuseRedirect makes an http.Client return a redirect as the response
// instead of following it: see Config.HTTPClient.
func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// Client calls Centrifugo's HTTP server API. It is safe for concurrent use,
// and meant to be created once and shared.
type Client struct {
	addr     string
	addrFunc func(context.Context) (string, error)
	token    func(context.Context) (string, error)
	header   http.Header
	host     string
	http     *http.Client
	timeout  time.Duration
	batcher  *batcher
}

// New returns a Client for cfg, or an error wrapping [ErrInvalidConfig] that
// names what is wrong with it.
func New(cfg Config) (*Client, error) {
	c := &Client{
		addrFunc: cfg.APIEndpointFunc,
		token:    cfg.BearerTokenFunc,
		http:     cfg.HTTPClient,
		timeout:  cfg.RequestTimeout,
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
	case !validHeaderValue(cfg.APIKey):
		return nil, invalid("APIKey contains a line break or another control character")
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
		if !validHeaderName(k) {
			return nil, invalid("Header name %q is not a valid HTTP header name", k)
		}
		for _, value := range v {
			if !validHeaderValue(value) {
				return nil, invalid("Header %s has a value with a line break or another control character", http.CanonicalHeaderKey(k))
			}
		}
		if http.CanonicalHeaderKey(k) == "Host" {
			// net/http sends a request's Host field, never a Host header.
			if len(v) != 1 || v[0] == "" {
				return nil, invalid("Header Host must have exactly one non-empty value")
			}
			c.host = v[0]
			continue
		}
		c.header[http.CanonicalHeaderKey(k)] = append([]string(nil), v...)
	}
	c.header.Set("Content-Type", "application/json")
	if c.header.Get("User-Agent") == "" {
		c.header.Set("User-Agent", "gocent/v4")
	}
	if cfg.APIKey != "" {
		c.header.Set("X-API-Key", cfg.APIKey)
	}

	switch {
	case c.timeout < 0:
		return nil, invalid("RequestTimeout is negative")
	case c.timeout == 0:
		c.timeout = defaultRequestTimeout
	}
	switch {
	case c.http == nil:
		c.http = DefaultHTTPClient()
	case c.http.CheckRedirect == nil:
		hc := *c.http
		hc.CheckRedirect = refuseRedirect
		c.http = &hc
	}

	ab := cfg.AutoBatch
	switch {
	case ab.MaxInFlight < 0:
		return nil, invalid("AutoBatch.MaxInFlight is negative")
	case ab.MaxBatchSize < 0:
		return nil, invalid("AutoBatch.MaxBatchSize is negative")
	case ab.MaxInFlight == 0 && ab.MaxBatchSize != 0:
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
		// Not the *url.Error itself: that is what a failed HTTP request
		// returns too, and Retryable takes it for the network's.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return "", fmt.Errorf("%q is not a valid URL: %w", addr, err)
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
		if token == "" || !validHeaderValue(token) {
			return errors.New("gocent: bearer token: BearerTokenFunc returned an empty token or one with a line break or another control character")
		}
		auth = "Bearer " + token
	}

	// The body is not pooled: the transport may go on reading it after Do
	// returns - when the server replies before reading it all, or to retry
	// the request - so it must stay untouched for as long as the request
	// lives, which only the garbage collector knows.
	encoded, err := json.Marshal(body)
	if err != nil {
		return encodingError(method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+method, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("gocent: %w", err)
	}
	// Each request has headers of its own: a cookie jar adds to them, and so
	// may an HTTP transport of the caller's - against the RoundTripper
	// contract, but a shared map written concurrently is a crash.
	req.Header = c.header.Clone()
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if c.host != "" {
		req.Host = c.host
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxAPIErrorBytes))
		if apiErr := transportModeError(resp.StatusCode, b); apiErr != nil {
			return apiErr
		}
		if len(b) > maxErrorBodyBytes {
			b = b[:maxErrorBodyBytes]
		}
		httpErr := &HTTPError{StatusCode: resp.StatusCode, Body: bytes.TrimSpace(b)}
		if methodNames[method] {
			httpErr.proMethod = method
		}
		return httpErr
	}
	if err := json.UnmarshalRead(resp.Body, out); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
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

// transportModeError returns the Centrifugo error in the body of a reply with
// an HTTP error status, or nil if it holds none.
//
// With http_api.error_mode "transport", or the X-Centrifugo-Error-Mode header,
// Centrifugo replies to a failed call with an HTTP status and the error alone
// as the body - the same error as in a 200 reply. Only the statuses Centrifugo
// maps its errors to are taken for one: a proxy or gateway in front of it may
// answer with a JSON body of the same shape - a 503 with {"code":503}, say -
// which is an HTTPError all the same. Any code from 100 up is kept, known or
// not: Centrifugo adds codes over time. Except a code which is an HTTP error
// status, from 400 to 599 - {"code":429} from a rate limiting gateway: no
// Centrifugo API error has such a code, so it is the gateway's, and stays an
// HTTPError, which Retryable judges by its status.
func transportModeError(status int, body []byte) *Error {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict,
		http.StatusRequestedRangeNotSatisfiable, http.StatusTooManyRequests,
		http.StatusInternalServerError:
	default:
		return nil
	}
	var apiErr Error
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Code >= minErrorCode && !isHTTPErrorStatus(apiErr.Code) {
		return &apiErr
	}
	return nil
}

// minErrorCode is the lowest code of a Centrifugo error.
const minErrorCode = 100

// isHTTPErrorStatus reports whether code is an HTTP client or server error
// status, which a gateway puts into its JSON error bodies.
func isHTTPErrorStatus(code uint32) bool {
	return code >= 400 && code < 600
}

// encodingError reports a request which cannot be encoded: a jsontext.Value
// which is not valid JSON, or a string which is not valid UTF-8.
func encodingError(method string, err error) error {
	return &requestError{method: method, problem: "cannot be encoded", err: err}
}

// validHeaderName reports whether name is a valid HTTP header name: one or
// more token characters.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c <= ' ' || c >= 0x7f || strings.IndexByte(`"(),/:;<=>?@[\]{}`, c) >= 0 {
			return false
		}
	}
	return true
}

// validHeaderValue reports whether value holds no control characters other
// than tab - a line break in particular.
func validHeaderValue(value string) bool {
	for i := range len(value) {
		if c := value[i]; (c < ' ' && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
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
