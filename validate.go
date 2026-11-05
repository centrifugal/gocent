package gocent

import (
	"encoding/json/jsontext"
	"errors"
	"strconv"
)

// ErrInvalidRequest is wrapped by the error for a request the client can tell
// is wrong without sending it: one which cannot be encoded - a payload which
// is not valid JSON, in any request - one leaving unset a field the API marks
// "Required." in its documentation, or a publication without data. Such a
// request is never sent.
//
// A required field whose empty value has a meaning - the empty User of
// InvalidateUserTokensRequest, which means anonymous users - is not checked.
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

// validateRequest checks req's required fields, then its own rules if it has
// any.
func validateRequest(req request) error {
	if p := req.required(); p != "" {
		return invalidRequest(req.APIMethod(), p+" is required")
	}
	if v, ok := req.(validator); ok {
		return v.validate()
	}
	return nil
}

// indexPath names an element of a list in the path of a required field:
// Items[2].
func indexPath(name string, i int) string {
	return name + "[" + strconv.Itoa(i) + "]"
}

// The publish and broadcast rules beyond required fields: data, which the
// proto marks "Required unless b64data is set".

func (r *PublishRequest) validate() error {
	if len(r.Data) == 0 && r.B64Data == "" {
		return invalidRequest("publish", "Data or B64Data is required")
	}
	return validateData("publish", r.Data)
}

// validateData checks a payload to publish is valid JSON.
func validateData(method string, data jsontext.Value) error {
	if len(data) == 0 || data.IsValid() {
		return nil
	}
	return invalidRequest(method, "Data is not valid JSON")
}

func (r *BroadcastRequest) validate() error {
	if len(r.Data) == 0 && r.B64Data == "" {
		return invalidRequest("broadcast", "Data or B64Data is required")
	}
	for i, ch := range r.Channels {
		if ch == "" {
			return invalidRequest("broadcast", "Channels["+strconv.Itoa(i)+"] is empty")
		}
	}
	return validateData("broadcast", r.Data)
}
