package gocent_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"testing"

	"github.com/centrifugal/gocent/v4"
)

// A command is the method and the request as JSON, and encodes as
// Centrifugo's {"method", "payload"} envelope - from a request value or
// pointer alike.
func TestNewCommand(t *testing.T) {
	req := gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{"text":"hi"}`), IdempotencyKey: "k1"}
	for name, r := range map[string]gocent.Request{"value": req, "pointer": &req} {
		cmd, err := gocent.NewCommand(r)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cmd.Method != "publish" || string(cmd.Payload) != `{"channel":"news","data":{"text":"hi"},"idempotency_key":"k1"}` {
			t.Errorf("%s: %+v", name, cmd)
		}
		envelope, err := json.Marshal(cmd)
		if err != nil {
			t.Fatal(err)
		}
		if string(envelope) != `{"method":"publish","payload":{"channel":"news","data":{"text":"hi"},"idempotency_key":"k1"}}` {
			t.Errorf("%s: envelope %s", name, envelope)
		}
	}

	cmd, err := gocent.NewCommand(gocent.InfoRequest{})
	if err != nil || cmd.Method != "info" || string(cmd.Payload) != `{}` {
		t.Errorf("info: %+v, %v", cmd, err)
	}
}

// An invalid request fails here, as it would in a call, never reaching a
// consumer.
func TestNewCommandInvalid(t *testing.T) {
	for name, r := range map[string]gocent.Request{
		"publish without channel": gocent.PublishRequest{Data: jsontext.Value(`{}`)},
		"publish without data":    gocent.PublishRequest{Channel: "news"},
		"invalid JSON":            gocent.PublishRequest{Channel: "news", Data: jsontext.Value(`{not json`)},
		"broadcast to nobody":     gocent.BroadcastRequest{Data: jsontext.Value(`{}`)},
	} {
		if _, err := gocent.NewCommand(r); !errors.Is(err, gocent.ErrInvalidRequest) {
			t.Errorf("%s: %v, want ErrInvalidRequest", name, err)
		}
	}
}
