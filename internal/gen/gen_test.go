package main

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestParseRejectsUnsupported(t *testing.T) {
	for name, src := range map[string]string{
		"oneof":          "message A {\n oneof x {\n string a = 1;\n }\n}",
		"enum":           "enum E {\n A = 0;\n}",
		"nested message": "message A {\n message B {}\n}",
		"optional":       "message A {\n optional string a = 1;\n}",
		"unknown type":   "message A {\n Missing b = 1;\n}",
		"int map key":    "message A {\n map<int32, string> m = 1;\n}",
		"unterminated":   "message A {\n string a = 1;",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(strings.NewReader(src), "test.proto"); err == nil {
				t.Fatal("parsed without error")
			}
		})
	}
}

func TestParseComments(t *testing.T) {
	s, err := parse(strings.NewReader(`
// A is documented.
message A {
    // x is above.
    string x = 1;
    string y = 2; // y is beside.

    // Detached, then a blank line: documents nothing.

    string z = 3;
}`), "test.proto")
	if err != nil {
		t.Fatal(err)
	}
	m := s.byName["A"]
	if got := strings.Join(m.doc, " "); got != "A is documented." {
		t.Errorf("message doc %q", got)
	}
	for i, want := range []string{"x is above.", "y is beside.", ""} {
		if got := strings.Join(m.fields[i].doc, " "); got != want {
			t.Errorf("field %s doc %q, want %q", m.fields[i].name, got, want)
		}
	}
}

func TestGoName(t *testing.T) {
	for in, want := range map[string]string{
		"channel":        "Channel",
		"user_id":        "UserID",
		"expire_at":      "ExpireAt",
		"b64data":        "B64Data",
		"b64info":        "B64Info",
		"b64stream_data": "B64StreamData",
		"ttl":            "TTL",
		"device_ids":     "DeviceIDs",
		"apns":           "APNS",
		"ios":            "IOS",
		"if_new":         "IfNew",
	} {
		if got := goName(in); got != want {
			t.Errorf("goName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListedValues(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  []string
		want []string
		err  bool
	}{
		"one line":     {doc: []string{"op is the operation. Required. One of: add, remove, set."}, want: []string{"add", "remove", "set"}},
		"wrapped":      {doc: []string{"provider is the provider. Required. One of: fcm,", "hms, apns, webpush."}, want: []string{"fcm", "hms", "apns", "webpush"}},
		"none":         {doc: []string{"channel is the channel. Required."}},
		"no period":    {doc: []string{"One of: a, b"}, err: true},
		"not a value":  {doc: []string{"One of: A, b."}, err: true},
		"listed twice": {doc: []string{"One of: a, a."}, err: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := listedValues(tc.doc)
			if (err != nil) != tc.err {
				t.Fatalf("err %v, want error %t", err, tc.err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConstGroups(t *testing.T) {
	s, err := parse(strings.NewReader(`
message A {
    string op = 1; // One of: add, remove.
}
message B {
    string op = 1; // One of: add, remove.
}
message C {
    string op = 1; // One of: and, or.
    string mode = 2; // One of: if_new.
}`), "test.proto")
	if err != nil {
		t.Fatal(err)
	}
	groups, err := constGroups(s, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range groups {
		for _, v := range g.values {
			names = append(names, g.constName(v))
		}
	}
	want := []string{"OpAdd", "OpRemove", "COpAnd", "COpOr", "ModeIfNew"}
	if !slices.Equal(names, want) {
		t.Errorf("constants %v, want %v", names, want)
	}

	// A tie names every group by its message.
	s, err = parse(strings.NewReader(`
message A {
    string op = 1; // One of: add.
}
message C {
    string op = 1; // One of: and.
}`), "test.proto")
	if err != nil {
		t.Fatal(err)
	}
	groups, err = constGroups(s, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	names = names[:0]
	for _, g := range groups {
		names = append(names, g.constName(g.values[0]))
	}
	if want := []string{"AOpAdd", "COpAnd"}; !slices.Equal(names, want) {
		t.Errorf("constants %v, want %v", names, want)
	}
}

// The committed output must be what the generator makes of the committed
// proto; `make generate` refreshes it.
func TestGeneratedIsCurrent(t *testing.T) {
	f, err := os.Open("../../api.proto")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	s, err := parse(f, "api.proto")
	if err != nil {
		t.Fatal(err)
	}
	want, err := generate(s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../api_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("api_gen.go is stale: run `make generate`")
	}
}

func TestGoDoc(t *testing.T) {
	w := &writer{goNames: map[string]string{"join_leave": "JoinLeave", "user_id": "UserID"}}
	cases := []struct{ in, want string }{
		// The field's own name, and a snake_case field name elsewhere.
		{"join_leave is set with user_id.", "JoinLeave is set with UserID."},
		// A configuration option keeps its name, also across a line break.
		{"join_leave overrides the join_leave channel option.", "JoinLeave overrides the join_leave channel option."},
		{"join_leave overrides the join_leave channel\noption.", "JoinLeave overrides the join_leave channel\noption."},
		{"see the join_leave option", "see the join_leave option"},
		// "options" is not "option": a field of that name is still a field.
		{"the join_leave options", "the JoinLeave options"},
	}
	for _, tc := range cases {
		if got := w.goDoc(tc.in, "join_leave", "JoinLeave"); got != tc.want {
			t.Errorf("goDoc(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A method may end with a semicolon or an empty body, and may stream either
// way: proxy.proto has all of them.
func TestParseMethods(t *testing.T) {
	s, err := parse(strings.NewReader(`
message Req {}
message Res {}
service S {
  rpc Plain(Req) returns (Res) {}
  rpc Semicolon(Req) returns (Res);
  rpc Out(Req) returns (stream Res);
  rpc Both(stream Req) returns (stream Res);
}`), "test.proto")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"Plain": false, "Semicolon": false, "Out": true, "Both": true}
	if len(s.methods) != len(want) {
		t.Fatalf("%d methods", len(s.methods))
	}
	for _, m := range s.methods {
		if streaming, ok := want[m.name]; !ok || m.streaming != streaming || m.request != "Req" || m.response != "Res" {
			t.Errorf("method %+v", *m)
		}
	}
}

// The proxy types leave out what only streaming methods use, and what nothing
// uses.
func TestProxyMessages(t *testing.T) {
	s, err := parse(strings.NewReader(`
message Req {
  Shared shared = 1;
}
message Res {}
message Shared {}
message StreamOnly {}
message Unused {}
service S {
  rpc Call(Req) returns (Res);
  rpc Stream(StreamOnly) returns (stream Shared);
}`), "test.proto")
	if err != nil {
		t.Fatal(err)
	}
	got := proxyMessages(s)
	for name, want := range map[string]bool{"Req": true, "Res": true, "Shared": true, "StreamOnly": false, "Unused": false} {
		if got[name] != want {
			t.Errorf("%s: %v, want %v", name, got[name], want)
		}
	}
}

// The presence wrappers and the required fields left unchecked are found in
// api.proto's comments. The sets are pinned here, so that a changed comment
// shows as a failed test rather than as a field changing type or check.
func TestPresenceFromComments(t *testing.T) {
	f, err := os.Open("../../api.proto")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	s, err := parse(f, "api.proto")
	if err != nil {
		t.Fatal(err)
	}
	var wrappers, unchecked []string
	for _, m := range s.messages {
		if isWrapper(m) {
			wrappers = append(wrappers, m.name)
		}
		for _, f := range m.fields {
			if required(f) && emptyHasMeaning(f) {
				unchecked = append(unchecked, m.name+"."+f.name)
			}
		}
	}
	wantWrappers := []string{"BoolValue", "Int32Value", "DeviceUserUpdate", "DeviceTimezoneUpdate", "DeviceLocaleUpdate", "DeviceMetaUpdate"}
	if !slices.Equal(wrappers, wantWrappers) {
		t.Errorf("wrappers %v, want %v", wrappers, wantWrappers)
	}
	wantUnchecked := []string{
		"InvalidateUserTokensRequest.user",
		"DeviceUserUpdate.user", "DeviceTimezoneUpdate.timezone", "DeviceLocaleUpdate.locale", "DeviceMetaUpdate.meta",
		"UserTopicUpdateRequest.user",
	}
	if !slices.Equal(unchecked, wantUnchecked) {
		t.Errorf("required fields left unchecked %v, want %v", unchecked, wantUnchecked)
	}
}
