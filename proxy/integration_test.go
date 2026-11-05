//go:build integration

package proxy_test

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
	"github.com/centrifugal/gocent/v4/proxy"
)

// The tests run the backend side of Centrifugo's proxies: Centrifugo, set up
// as testdata/centrifugo.json says, calls the connect and refresh proxies at
// GOCENT_TEST_PROXY_LISTEN - all interfaces, port 3100, by default - which it
// reaches as host.docker.internal from a container.
func listen(t *testing.T, mux *http.ServeMux) {
	t.Helper()
	addr := os.Getenv("GOCENT_TEST_PROXY_LISTEN")
	if addr == "" {
		addr = ":3100"
	}
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("listening for Centrifugo's proxy requests: %v", err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
}

func apiAddr() string {
	if addr := os.Getenv("GOCENT_TEST_ADDR"); addr != "" {
		return addr
	}
	return "http://localhost:8100/api"
}

// stream is a client connection over unidirectional HTTP streaming, without
// a token: Centrifugo authenticates it through the connect proxy.
type stream struct {
	lines *bufio.Scanner
}

func connect(t *testing.T, data string) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(apiAddr(), "/api")+"/connection/uni_http_stream", strings.NewReader(`{"data":`+data+`}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // the test's own Centrifugo
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return &stream{lines: bufio.NewScanner(resp.Body)}
}

// next returns the next message, or ok false once the connection is closed.
func (s *stream) next(t *testing.T) (msg map[string]jsontext.Value, ok bool) {
	t.Helper()
	for s.lines.Scan() {
		line := s.lines.Bytes()
		if len(line) == 0 || string(line) == "{}" { // a ping
			continue
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatalf("message %q: %v", line, err)
		}
		return msg, true
	}
	return nil, false
}

func TestIntegrationConnectAndRefresh(t *testing.T) {
	user := "proxy-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	ch := user + "-news"
	connects := make(chan proxy.ConnectRequest, 2)
	refreshes := make(chan proxy.RefreshRequest, 1)
	mux := http.NewServeMux()
	mux.Handle("/connect", proxy.Handlers{}.Connect(func(r *http.Request, req proxy.ConnectRequest) (proxy.ConnectResult, error) {
		connects <- req
		var data struct {
			Allow bool `json:"allow"`
		}
		if err := json.Unmarshal(req.Data, &data); err != nil {
			return proxy.ConnectResult{}, err
		}
		if !data.Allow {
			return proxy.ConnectResult{}, &proxy.Disconnect{Code: 4501, Reason: "unauthorized"}
		}
		return proxy.ConnectResult{
			User:     user,
			ExpireAt: time.Now().Add(2 * time.Second).Unix(),
			Info:     jsontext.Value(`{"name":"Alice"}`),
			Data:     jsontext.Value(`{"welcome":true}`),
			Channels: []string{ch},
		}, nil
	}))
	mux.Handle("/refresh", proxy.Handlers{}.Refresh(func(r *http.Request, req proxy.RefreshRequest) (proxy.RefreshResult, error) {
		refreshes <- req
		return proxy.RefreshResult{Expired: true}, nil
	}))
	listen(t, mux)

	// Refused: the connection is closed with the disconnect's code.
	refused := connect(t, `{"allow":false}`)
	if req := <-connects; req.Transport != "uni_http_stream" || req.Protocol != "json" || req.Encoding != "json" || req.Client == "" {
		t.Errorf("connect request %+v", req)
	}
	if msg, ok := refused.next(t); !ok || !strings.Contains(string(msg["disconnect"]), `"code":4501`) {
		t.Errorf("refused connection got %v", msg)
	}

	// Accepted: the result's user, data and server-side channel reach the
	// client, and so does a publication into that channel.
	s := connect(t, `{"allow":true}`)
	<-connects
	msg, ok := s.next(t)
	if !ok {
		t.Fatal("connection closed before its connect reply")
	}
	var reply struct {
		Connect struct {
			Client string                    `json:"client"`
			Data   jsontext.Value            `json:"data"`
			Subs   map[string]jsontext.Value `json:"subs"`
		} `json:"connect"`
	}
	if err := json.Unmarshal(mustJSON(t, msg), &reply); err != nil {
		t.Fatal(err)
	}
	if string(reply.Connect.Data) != `{"welcome":true}` || reply.Connect.Subs[ch] == nil {
		t.Fatalf("connect reply %s", mustJSON(t, msg))
	}
	api, err := gocent.New(gocent.Config{APIEndpoint: apiAddr(), APIKey: apiKey()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.Publish(t.Context(), gocent.PublishRequest{Channel: ch, Data: jsontext.Value(`{"text":"hello"}`)}); err != nil {
		t.Fatal(err)
	}
	if msg, ok := s.next(t); !ok || !strings.Contains(string(mustJSON(t, msg)), `"hello"`) {
		t.Fatalf("publication: %v", msg)
	}
	presence, err := api.Presence(t.Context(), gocent.PresenceRequest{Channel: ch})
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range presence.Presence {
		if info.User != user || string(info.ConnInfo) != `{"name":"Alice"}` {
			t.Errorf("presence %+v", info)
		}
	}

	// It expires: Centrifugo asks the refresh proxy, which ends it.
	select {
	case req := <-refreshes:
		if req.User != user || req.Client != reply.Connect.Client {
			t.Errorf("refresh request %+v", req)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no refresh request")
	}
	for {
		if _, ok := s.next(t); !ok {
			break
		}
	}
}

func apiKey() string {
	if key := os.Getenv("GOCENT_TEST_API_KEY"); key != "" {
		return key
	}
	return "api_key"
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
