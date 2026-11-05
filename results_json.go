package gocent

import (
	"encoding/json/v2"
	"errors"
)

// Results render as JSON by their wire names - to a frontend, a log, a queue -
// with encoding/json and encoding/json/v2 alike. An error in a result, a
// channel's or a command's, renders as {"code": ..., "message": ...} for an
// [*Error], and {"message": ...} for any other.

// errorJSON is how an error in a result renders.
type errorJSON struct {
	Code    uint32 `json:"code,omitzero"`
	Message string `json:"message"`
}

func toErrorJSON(err error) *errorJSON {
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return &errorJSON{Code: e.Code, Message: e.Message}
	}
	return &errorJSON{Message: err.Error()}
}

// MarshalJSON renders the channel's outcome: {"channel", "result" when it
// succeeded, "error" when it failed, "attempts"}.
func (r ChannelResult) MarshalJSON() ([]byte, error) {
	out := struct {
		Channel  string         `json:"channel"`
		Result   *PublishResult `json:"result,omitzero"`
		Error    *errorJSON     `json:"error,omitzero"`
		Attempts int            `json:"attempts"`
	}{Channel: r.Channel, Error: toErrorJSON(r.Err), Attempts: r.Attempts}
	if r.Err == nil {
		out.Result = &r.Result
	}
	return json.Marshal(out)
}

// MarshalJSON renders the batch's outcome: {"replied", "failed"}.
func (r BatchResult) MarshalJSON() ([]byte, error) {
	failed := r.failed
	if failed == nil {
		failed = []CommandError{}
	}
	return json.Marshal(struct {
		Replied bool           `json:"replied"`
		Failed  []CommandError `json:"failed"`
	}{r.Replied, failed})
}

// MarshalJSON renders a failed command: {"index", "method", "error"}.
func (e CommandError) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Index  int        `json:"index"`
		Method string     `json:"method"`
		Error  *errorJSON `json:"error"`
	}{e.Index, e.Method, toErrorJSON(e.Err)})
}
