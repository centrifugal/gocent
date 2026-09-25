package main

import (
	"bytes"
	"os"
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
			if _, err := parse(strings.NewReader(src)); err == nil {
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
}`))
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
	} {
		if got := goName(in); got != want {
			t.Errorf("goName(%q) = %q, want %q", in, got, want)
		}
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
	s, err := parse(f)
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
