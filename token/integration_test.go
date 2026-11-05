//go:build integration

package token_test

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
	"github.com/centrifugal/gocent/v4/token"
)

// A connection token from Connection, signed with the secret of
// testdata/centrifugo.json, connects to a real Centrifugo: the user and info
// are the token's, and its server-side channel delivers a publication.
func TestIntegrationConnectionToken(t *testing.T) {
	addr := os.Getenv("GOCENT_TEST_ADDR")
	if addr == "" {
		addr = "http://localhost:8100/api"
	}
	key := os.Getenv("GOCENT_TEST_API_KEY")
	if key == "" {
		key = "api_key"
	}
	user := "token-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	ch := user + "-news"
	tok, err := token.Connection(token.HMAC([]byte("client_secret")), token.ConnectionClaims{
		User:      user,
		ExpiresAt: time.Now().Add(time.Hour),
		Info:      jsontext.Value(`{"name":"Alice"}`),
		Channels:  []string{ch},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, //nolint:gosec // the test's own Centrifugo
		strings.TrimSuffix(addr, "/api")+"/connection/uni_http_stream", strings.NewReader(`{"token":"`+tok+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // the test's own Centrifugo
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	lines := bufio.NewScanner(resp.Body)
	if !lines.Scan() || !strings.Contains(lines.Text(), `"connect"`) || !strings.Contains(lines.Text(), ch) {
		t.Fatalf("connect reply %q", lines.Text())
	}

	api, err := gocent.New(gocent.Config{APIEndpoint: addr, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	presence, err := api.Presence(t.Context(), gocent.PresenceRequest{Channel: ch})
	if err != nil || len(presence.Presence) != 1 {
		t.Fatalf("presence %+v, %v", presence, err)
	}
	for _, info := range presence.Presence {
		if info.User != user || string(info.ConnInfo) != `{"name":"Alice"}` {
			t.Errorf("presence %+v", info)
		}
	}
	if _, err = api.Publish(t.Context(), gocent.PublishRequest{Channel: ch, Data: jsontext.Value(`{"text":"hello"}`)}); err != nil {
		t.Fatal(err)
	}
	for lines.Scan() {
		if strings.Contains(lines.Text(), `"hello"`) {
			return
		}
	}
	t.Fatal("no publication")
}
