//go:build integration

package gocent_test

import (
	"bufio"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
)

// redisXAdd adds an entry with fields to stream, speaking Redis's protocol
// over a plain connection: the tests need no Redis client.
func redisXAdd(t *testing.T, addr, stream string, fields ...string) {
	t.Helper()
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("connecting to Redis: %v", err)
	}
	defer func() { _ = conn.Close() }()
	args := append([]string{"XADD", stream, "*"}, fields...)
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err = conn.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.HasPrefix(reply, "$") {
		t.Fatalf("XADD: %q, %v", reply, err)
	}
}

// Commands from NewCommand run when Centrifugo's async consumer takes them
// from a Redis stream - with the method in its own field, and as the
// {"method", "payload"} envelope alone. testdata/centrifugo-pro.compose.yml
// configures the consumer.
func TestIntegrationCommandConsumed(t *testing.T) {
	addr := os.Getenv("GOCENT_TEST_REDIS")
	if addr == "" && os.Getenv("GOCENT_TEST_PRO") == "1" {
		addr = "localhost:46379"
	}
	if addr == "" {
		t.Skip("needs the Redis stream consumer of testdata/centrifugo-pro.compose.yml: set GOCENT_TEST_REDIS")
	}
	c := realClient(t, gocent.AutoBatch{})
	ch := uniqueChannel(t)

	apart, err := gocent.NewCommand(gocent.PublishRequest{Channel: ch, Data: jsontext.Value(`{"n":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	redisXAdd(t, addr, "gocent_commands", "method", apart.Method, "payload", string(apart.Payload))

	whole, err := gocent.NewCommand(gocent.PublishRequest{Channel: ch, Data: jsontext.Value(`{"n":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(whole)
	if err != nil {
		t.Fatal(err)
	}
	redisXAdd(t, addr, "gocent_commands", "payload", string(envelope))

	deadline := time.Now().Add(15 * time.Second)
	for {
		h, err := c.History(t.Context(), gocent.HistoryRequest{Channel: ch, Limit: -1})
		if err != nil {
			t.Fatal(err)
		}
		if len(h.Publications) == 2 {
			var got []string
			for _, p := range h.Publications {
				got = append(got, string(p.Data))
			}
			if strings.Join(got, ",") != `{"n":1},{"n":2}` {
				t.Errorf("publications %v", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of 2 commands consumed after 15s", len(h.Publications))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
