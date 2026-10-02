package gocent_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/centrifugal/gocent/v4"
)

// echoCentrifugo succeeds at everything with an empty result, and records the
// method each request went to and, for a batch, the command it carried.
type echoCentrifugo struct {
	mu       sync.Mutex
	paths    []string
	commands []string
}

func (e *echoCentrifugo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.paths = append(e.paths, strings.TrimPrefix(r.URL.Path, "/api/"))
	// A broadcast replies per channel; minimalRequest sends it to one.
	const broadcastResult = `{"responses":[{"result":{}}]}`
	switch r.URL.Path {
	case "/api/broadcast":
		_, _ = w.Write([]byte(`{"result":` + broadcastResult + `}`))
		return
	case "/api/batch":
	default:
		_, _ = w.Write([]byte(`{"result":{}}`))
		return
	}
	var req struct {
		Commands []map[string]jsontext.Value `json:"commands"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	replies := make([]string, len(req.Commands))
	for i, cmd := range req.Commands {
		replies[i] = "{}"
		for k := range cmd {
			e.commands = append(e.commands, k)
			if k == "broadcast" {
				replies[i] = `{"broadcast":` + broadcastResult + `}`
			}
		}
	}
	_, _ = w.Write([]byte(`{"replies":[` + strings.Join(replies, ",") + `]}`))
}

func (e *echoCentrifugo) take() (paths, commands []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	paths, commands = e.paths, e.commands
	e.paths, e.commands = nil, nil
	return paths, commands
}

var (
	ctxType    = reflect.TypeFor[context.Context]()
	errorType  = reflect.TypeFor[error]()
	methodType = reflect.TypeFor[interface{ APIMethod() string }]()
)

// apiMethodOf returns the API method a request type is sent to, or false for
// a Go method which is not an API call.
func apiMethodOf(t reflect.Type) (string, bool) {
	if !t.Implements(methodType) {
		return "", false
	}
	return reflect.Zero(t).Interface().(interface{ APIMethod() string }).APIMethod(), true
}

// TestEveryMethodReachesItsEndpoint calls every API method of Client, and
// adds every one to a Batch, checking each goes where its request type says.
// It covers the generated methods as a whole, so a command added to api.proto
// is checked without a test of its own.
func TestEveryMethodReachesItsEndpoint(t *testing.T) {
	echo := &echoCentrifugo{}
	srv := httptest.NewServer(echo)
	defer srv.Close()
	c, err := gocent.New(gocent.Config{APIEndpoint: srv.URL + "/api", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}

	clientMethods := 0
	cv := reflect.ValueOf(c)
	for i := range cv.NumMethod() {
		m, mt := cv.Method(i), cv.Type().Method(i)
		typ := m.Type()
		if typ.NumIn() == 0 || typ.In(0) != ctxType || typ.NumOut() != 2 || typ.Out(1) != errorType {
			continue
		}
		var want string
		args := []reflect.Value{reflect.ValueOf(t.Context())}
		switch typ.NumIn() {
		case 1: // a method without parameters, such as Info
			want = snake(mt.Name)
		case 2:
			var ok bool
			if want, ok = apiMethodOf(typ.In(1)); !ok {
				t.Errorf("Client.%s takes %s, which has no APIMethod", mt.Name, typ.In(1))
				continue
			}
			args = append(args, minimalRequest(typ.In(1)))
		default:
			continue
		}
		out := m.Call(args)
		if err, _ := out[1].Interface().(error); err != nil {
			t.Errorf("Client.%s: %v", mt.Name, err)
		}
		paths, _ := echo.take()
		if len(paths) != 1 || paths[0] != want {
			t.Errorf("Client.%s went to %v, want [%s]", mt.Name, paths, want)
		}
		clientMethods++
	}

	b := c.NewBatch(gocent.BatchOptions{})
	var want []string
	var pendings []reflect.Value
	bv := reflect.ValueOf(b)
	for i := range bv.NumMethod() {
		m, mt := bv.Method(i), bv.Type().Method(i)
		typ := m.Type()
		if typ.NumOut() != 1 || !strings.HasPrefix(typ.Out(0).String(), "*gocent.Pending[") {
			continue
		}
		var args []reflect.Value
		if typ.NumIn() == 1 {
			method, ok := apiMethodOf(typ.In(0))
			if !ok {
				t.Errorf("Batch.%s takes %s, which has no APIMethod", mt.Name, typ.In(0))
				continue
			}
			want = append(want, method)
			args = append(args, minimalRequest(typ.In(0)))
		} else {
			want = append(want, snake(mt.Name))
		}
		pendings = append(pendings, m.Call(args)[0])
	}
	if err := b.Send(t.Context()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	for _, p := range pendings {
		if err, _ := p.MethodByName("Result").Call(nil)[1].Interface().(error); err != nil {
			t.Errorf("%s: %v", p.Type(), err)
		}
	}
	_, commands := echo.take()
	if !reflect.DeepEqual(commands, want) {
		t.Errorf("batch commands %v\nwant           %v", commands, want)
	}

	// Every API method has both forms.
	if clientMethods != len(want) || clientMethods == 0 {
		t.Errorf("%d Client methods and %d Batch methods", clientMethods, len(want))
	}
}

// minimalRequest is the zero request of a type, or for the types the client
// checks before sending, the least that passes the check.
func minimalRequest(t reflect.Type) reflect.Value {
	switch t {
	case reflect.TypeFor[gocent.PublishRequest]():
		return reflect.ValueOf(gocent.PublishRequest{Channel: "c", Data: data})
	case reflect.TypeFor[gocent.BroadcastRequest]():
		return reflect.ValueOf(gocent.BroadcastRequest{Channels: []string{"c"}, Data: data})
	}
	return reflect.Zero(t)
}

// snake turns "PresenceStats" into "presence_stats", for methods which take
// no request to ask.
func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if 'A' <= r && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}
