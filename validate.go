package gocent

import (
	"errors"
	"strconv"
)

// ErrInvalidRequest is wrapped by the error for a request the client can tell
// is wrong without sending it: one which cannot be encoded - a payload which
// is not valid JSON, in any request - or a publication without a channel or
// data. Such a request is never sent.
//
// Only rules which hold for every Centrifugo are checked here; everything
// else Centrifugo checks itself, and reports as [ErrBadRequest].
var ErrInvalidRequest = errors.New("gocent: invalid request")

// validator is implemented by requests with rules the client checks.
type validator interface {
	validate() error
}

func invalidRequest(method, problem string) error {
	return &requestError{method: method, problem: problem}
}

type requestError struct {
	method, problem string
	// err is what found the problem, if anything did: the JSON encoder.
	err error
}

func (e *requestError) Error() string {
	msg := "gocent: invalid " + e.method + " request: " + e.problem
	if e.err != nil {
		msg += ": " + e.err.Error()
	}
	return msg
}

func (e *requestError) Unwrap() []error {
	if e.err == nil {
		return []error{ErrInvalidRequest}
	}
	return []error{ErrInvalidRequest, e.err}
}

// validateRequest validates req if it has rules to check.
func validateRequest(req request) error {
	if v, ok := req.(validator); ok {
		return v.validate()
	}
	return nil
}

func (r *PublishRequest) validate() error {
	switch {
	case r.Channel == "":
		return invalidRequest("publish", "Channel is required")
	case len(r.Data) == 0 && r.B64Data == "":
		return invalidRequest("publish", "Data or B64Data is required")
	case len(r.Data) > 0 && !r.Data.IsValid():
		return invalidRequest("publish", "Data is not valid JSON")
	}
	return nil
}

func (r *BroadcastRequest) validate() error {
	switch {
	case len(r.Channels) == 0:
		return invalidRequest("broadcast", "Channels is required")
	case len(r.Data) == 0 && r.B64Data == "":
		return invalidRequest("broadcast", "Data or B64Data is required")
	case len(r.Data) > 0 && !r.Data.IsValid():
		return invalidRequest("broadcast", "Data is not valid JSON")
	}
	for i, ch := range r.Channels {
		if ch == "" {
			return invalidRequest("broadcast", "Channels["+strconv.Itoa(i)+"] is empty")
		}
	}
	return nil
}
