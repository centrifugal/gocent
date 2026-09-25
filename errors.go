package gocent

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Error is an error returned by Centrifugo itself: the server understood the
// request and refused it or failed to carry it out. It is the same whether
// Centrifugo puts it in a 200 reply or, in its transport error mode, in the
// body of a reply with an HTTP error status.
//
// Centrifugo identifies an error by its Code. The codes known when this
// package was written have values below, such as [ErrUnknownChannel], and
// [errors.Is] compares an Error against them by code alone:
//
//	if errors.Is(err, gocent.ErrUnknownChannel) { ... }
//
// Centrifugo adds codes from time to time, so an Error may carry a code with
// no value here. Match on the codes you handle and treat the rest by their
// class - use [errors.As] to reach Code and Message:
//
//	var apiErr *gocent.Error
//	if errors.As(err, &apiErr) { log.Println(apiErr.Code, apiErr.Message) }
type Error struct {
	// Code identifies the error. It is stable; Message is not.
	Code uint32 `json:"code"`
	// Message describes the error for people.
	Message string `json:"message,omitzero"`
}

func (e *Error) Error() string {
	var b strings.Builder
	b.Grow(len("gocent: ") + len(e.Message) + len(" (code 000)"))
	b.WriteString("gocent: ")
	if e.Message != "" {
		b.WriteString(e.Message)
	} else {
		b.WriteString("server error")
	}
	b.WriteString(" (code ")
	b.WriteString(strconv.FormatUint(uint64(e.Code), 10))
	b.WriteByte(')')
	return b.String()
}

// Is reports whether target is an *Error with the same Code. Messages are not
// compared: they are for people and may change between Centrifugo versions.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t != nil && t.Code == e.Code
}

// Errors Centrifugo returns, matched by code with [errors.Is]. See [Error] for
// why this list is not exhaustive.
var (
	ErrInternal              = &Error{Code: 100, Message: "internal server error"}
	ErrUnknownChannel        = &Error{Code: 102, Message: "unknown channel"}
	ErrNotFound              = &Error{Code: 104, Message: "not found"}
	ErrBadRequest            = &Error{Code: 107, Message: "bad request"}
	ErrNotAvailable          = &Error{Code: 108, Message: "not available"}
	ErrUnrecoverablePosition = &Error{Code: 112, Message: "unrecoverable position"}
	ErrConflict              = &Error{Code: 113, Message: "conflict"}
)

// HTTPError reports a response with an HTTP status other than 200 OK: the
// request did not reach the API method at all. A 401 means the API key was
// rejected - see [ErrUnauthorized].
type HTTPError struct {
	StatusCode int
	// Body holds the start of the response body, for diagnostics.
	Body []byte
}

func (e *HTTPError) Error() string {
	var b strings.Builder
	b.WriteString("gocent: unexpected HTTP status ")
	b.WriteString(strconv.Itoa(e.StatusCode))
	switch e.StatusCode {
	case http.StatusNotFound:
		// The usual cause: APIEndpoint lacks the API prefix, so every method is
		// looked for where Centrifugo has none.
		b.WriteString(" (is APIEndpoint the API base URL, such as http://localhost:8000/api?)")
	case http.StatusUnauthorized:
		b.WriteString(" (check the credentials: APIKey or BearerTokenFunc)")
	}
	if len(e.Body) > 0 {
		b.WriteString(": ")
		b.Write(e.Body)
	}
	return b.String()
}

// Is makes errors.Is(err, ErrUnauthorized) true for a 401 response.
func (e *HTTPError) Is(target error) bool {
	return target == ErrUnauthorized && e.StatusCode == http.StatusUnauthorized
}

// ErrUnauthorized is matched by an [HTTPError] with status 401: Centrifugo did
// not accept the credentials - the API key, or the bearer token.
var ErrUnauthorized = errors.New("gocent: unauthorized, check the credentials")

// DecodeError reports a response which is not a Centrifugo API reply. The
// usual cause is an APIEndpoint which does not point at Centrifugo's API, so
// that something else - a proxy, a web page - answers instead.
type DecodeError struct {
	Err error
}

func (e *DecodeError) Error() string {
	return "gocent: decoding response: " + e.Err.Error()
}

func (e *DecodeError) Unwrap() error { return e.Err }

// ErrInvalidConfig is wrapped by every error [New] returns.
var ErrInvalidConfig = errors.New("gocent: invalid config")

// maxErrorItems bounds how many failures an aggregate error names in its
// message; all of them stay available in the error itself.
const maxErrorItems = 3
