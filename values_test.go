package gocent_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/centrifugal/gocent/v4"
)

func TestBool(t *testing.T) {
	for _, tt := range []struct {
		override gocent.SubscribeOptionOverride
		want     string
	}{
		{gocent.SubscribeOptionOverride{}, `{}`},
		{gocent.SubscribeOptionOverride{Presence: gocent.Bool(true)}, `{"presence":{"value":true}}`},
		// An explicit false is still sent: it overrides the channel's option.
		{gocent.SubscribeOptionOverride{Presence: gocent.Bool(false)}, `{"presence":{}}`},
	} {
		b, err := json.Marshal(tt.override)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != tt.want {
			t.Errorf("got %s, want %s", b, tt.want)
		}
	}
}
