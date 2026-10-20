package gocent

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"slices"
)

// Retryable reports whether retrying the call which returned err may succeed:
// the error came from a temporary condition, not from the request. It is
// false for nil.
//
// Retryable are:
//   - an [*Error] which Centrifugo marked [Error.Temporary], and the internal
//     error (code 100) from Centrifugo versions which did not mark errors yet;
//   - a network failure, and a timeout ([context.DeadlineExceeded]) - also
//     one of the caller's own context, which a retry needs a new deadline for;
//   - an [*HTTPError] with status 429 or 5xx, such as from a proxy in front
//     of Centrifugo;
//   - a [*BroadcastError] or [*BatchError] with at least one failed part
//     which is retryable.
//
// Not retryable are the request's own errors - an unknown channel, a bad
// request, [ErrInvalidRequest] - and a cancelled context, a reply which is
// not Centrifugo's ([*DecodeError]), rejected credentials and an invalid TLS
// certificate.
//
// An error from the caller's own Config.APIEndpointFunc or
// Config.BearerTokenFunc is judged by the same rules: retryable when it is a
// network failure or a timeout. Retryable cannot tell whether another error
// of theirs is temporary.
//
// Retrying is safe for a publication only with an IdempotencyKey: a
// retryable error does not mean nothing was done, and after a timeout or an
// internal error Centrifugo may have published it already. Back off between
// attempts, and bound their number. See "Retrying" in the package
// documentation for how to choose keys.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	if be, ok := errors.AsType[*BroadcastError](err); ok {
		return slices.ContainsFunc(be.Failed, func(f ChannelResult) bool { return Retryable(f.Err) })
	}
	if batchErr, ok := errors.AsType[*BatchError](err); ok {
		return slices.ContainsFunc(batchErr.Failed, func(f CommandError) bool { return Retryable(f.Err) })
	}
	if apiErr, ok := errors.AsType[*Error](err); ok {
		return apiErr.Temporary || apiErr.Code == ErrInternal.Code
	}
	if httpErr, ok := errors.AsType[*HTTPError](err); ok {
		return httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= http.StatusInternalServerError
	}
	switch {
	case errors.Is(err, context.Canceled):
		return false
	case errors.Is(err, context.DeadlineExceeded):
		return true
	}
	if _, ok := errors.AsType[*DecodeError](err); ok {
		return false
	}
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return false
	}
	// Any other failure of the HTTP request itself - a refused or reset
	// connection, a closed server - is the network's.
	_, ok := errors.AsType[*url.Error](err)
	return ok
}
