package gocent

import (
	"errors"
	"strconv"
)

// ErrInvalidRequest is wrapped by the error for a request the client can tell
// is wrong without sending it: a publication without a channel or data, or
// data which is not valid JSON. Such a request is never sent.
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
}

func (e *requestError) Error() string {
	return "gocent: invalid " + e.method + " request: " + e.problem
}

func (e *requestError) Unwrap() error { return ErrInvalidRequest }

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
