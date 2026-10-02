// Command gen writes api_gen.go from api.proto: the request and result types of
// every Centrifugo server API method, and the Client and Batch methods sending
// them.
//
// It runs through go generate in the package directory:
//
//	go generate ./...
//
// The output is committed. `make generate-check` fails when it is stale.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"slices"
	"strings"
)

func main() {
	in := flag.String("in", "api.proto", "proto file to read")
	out := flag.String("out", "api_gen.go", "Go file to write")
	flag.Parse()
	if err := run(*in, *out); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	f, err := os.Open(in) // #nosec G304 -- path given by go generate
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	s, err := parse(f)
	if err != nil {
		return err
	}
	src, err := generate(s)
	if err != nil {
		return err
	}
	return os.WriteFile(out, src, 0o600)
}

var scalarTypes = map[string]string{
	"string": "string",
	"bool":   "bool",
	"int32":  "int32",
	"int64":  "int64",
	"uint32": "uint32",
	"uint64": "uint64",
	"double": "float64",
	"float":  "float32",
	// Payloads in the API are JSON. jsontext.Value keeps them raw and is
	// checked for validity when a request is encoded, so invalid JSON fails in
	// the client instead of reaching Centrifugo.
	"bytes": "jsontext.Value",
}

// omittedCommands are API commands the SDK does not offer. rpc calls
// server API extensions, which Centrifugo does not provide.
var omittedCommands = map[string]bool{"rpc": true}

// Messages the generator does not turn into exported types. Each is either
// written by hand (Error, BroadcastResult) or only used on the wire, where the
// package has its own unexported form of it (the batch envelope, and the
// error-or-result envelope every method replies with).
func skipType(s *schema, name string) bool {
	switch name {
	case "Error", "BroadcastResult", "Command", "Reply", "BatchRequest", "BatchResponse":
		return true
	}
	// The request and result of a command the SDK leaves out.
	if cmd, reply := s.byName["Command"], s.byName["Reply"]; cmd != nil && reply != nil {
		for _, set := range [][]*field{cmd.fields, reply.fields} {
			for _, f := range set {
				if omittedCommands[f.name] && f.typ == name {
					return true
				}
			}
		}
	}
	return isEnvelope(s, name)
}

// isEnvelope reports a method's response message: an error and a result.
func isEnvelope(s *schema, name string) bool {
	for _, rpc := range s.methods {
		if rpc.response == name {
			return true
		}
	}
	return false
}

// isWrapper reports a message holding a single "value" field, like BoolValue.
// Such a field is optional in a way its scalar is not: absent means "leave the
// server's setting alone", which a false or a zero cannot express. In Go it is
// a pointer.
func isWrapper(m *message) bool {
	return m != nil && strings.HasSuffix(m.name, "Value") && len(m.fields) == 1 && m.fields[0].name == "value"
}

// Handwritten forms of messages which appear inside generated types.
var handwritten = map[string]string{
	"Error":           "*Error",
	"BroadcastResult": "broadcastWire",
}

func goBaseType(s *schema, typ string) string {
	if t, ok := scalarTypes[typ]; ok {
		return t
	}
	if t, ok := handwritten[typ]; ok {
		return t
	}
	if isWrapper(s.byName[typ]) {
		return "*" + typ
	}
	return typ
}

func goFieldType(s *schema, f *field) string {
	base := goBaseType(s, f.typ)
	switch {
	case f.mapKey != "":
		return "map[string]" + base
	case f.repeated:
		return "[]" + base
	}
	return base
}

// Words written in Go with a fixed capitalization.
var initialisms = map[string]string{
	"id": "ID", "ids": "IDs", "uid": "UID", "url": "URL", "urls": "URLs",
	"ttl": "TTL", "api": "API", "json": "JSON", "http": "HTTP", "rpc": "RPC",
	"apns": "APNS", "fcm": "FCM", "hms": "HMS", "ip": "IP",
}

func goName(snake string) string {
	var b strings.Builder
	for part := range strings.SplitSeq(snake, "_") {
		if w, ok := initialisms[part]; ok {
			b.WriteString(w)
			continue
		}
		// Base64 variants of fields - b64data, b64info, b64stream_data -
		// keep the "B64" prefix whole: B64Info, not B64info.
		if rest, ok := strings.CutPrefix(part, "b64"); ok && rest != "" {
			b.WriteString("B64" + strings.ToUpper(rest[:1]) + rest[1:])
			continue
		}
		if part != "" {
			b.WriteString(strings.ToUpper(part[:1]) + part[1:])
		}
	}
	return b.String()
}

// command pairs an API method with its request and result, as the batch
// Command and Reply messages declare them: the field name is the method.
type command struct {
	method  string // "presence_stats"
	goName  string // "PresenceStats"
	request *message
	result  string // result message name
	// proOnly is set for a command its comment in Command says only
	// Centrifugo PRO has.
	proOnly bool
}

func commands(s *schema) ([]command, error) {
	cmd, reply := s.byName["Command"], s.byName["Reply"]
	if cmd == nil || reply == nil {
		return nil, fmt.Errorf("api.proto: Command and Reply messages are required")
	}
	results := map[string]string{}
	for _, f := range reply.fields {
		results[f.name] = f.typ
	}
	var out []command
	for _, f := range cmd.fields {
		if omittedCommands[f.name] {
			continue
		}
		req := s.byName[f.typ]
		res, ok := results[f.name]
		if req == nil || !ok {
			return nil, fmt.Errorf("api.proto: Command.%s has no request message or no Reply.%s", f.name, f.name)
		}
		if want := goName(f.name) + "Request"; !strings.EqualFold(f.typ, want) {
			return nil, fmt.Errorf("api.proto: Command.%s is %s, expected %s", f.name, f.typ, want)
		}
		out = append(out, command{
			method:  f.name,
			goName:  strings.TrimSuffix(f.typ, "Request"),
			request: req,
			result:  res,
			proOnly: strings.Contains(strings.Join(f.doc, " "), "Centrifugo PRO only"),
		})
	}
	return out, nil
}

type writer struct {
	bytes.Buffer
	// goNames maps every proto field name in the schema to its Go name, for
	// rewriting the names comments mention.
	goNames map[string]string
}

var (
	reBackticked = regexp.MustCompile("`([a-z0-9_]+)`")
	reSnake      = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b|\bb64[a-z]+\b`)
	// reOption matches what follows a configuration option's name, such as
	// "join_leave channel option": that name is Centrifugo's, not a field's.
	reOption = regexp.MustCompile(`^\s+(?:channel\s+)?option\b`)
)

// goDoc rewrites the proto names a comment mentions into Go ones, so that
// the documentation of UserID talks about UserID, not user_id. A comment
// opening with the documented field's own name has that name rewritten even
// when it is a single word ("version, when set, ..."); elsewhere only names
// which cannot be ordinary words are - backticked ones, and snake_case ones.
// A name followed by "option" is a configuration option's, and is kept.
// line may span several lines of a comment.
func (w *writer) goDoc(line, protoName, goIdent string) string {
	if protoName != "" && strings.HasPrefix(line, protoName) {
		rest := line[len(protoName):]
		if rest == "" || !isIdentByte(rest[0]) {
			line = goIdent + rest
		}
	}
	line = reBackticked.ReplaceAllStringFunc(line, func(m string) string {
		if g, ok := w.goNames[m[1:len(m)-1]]; ok {
			return g
		}
		return m
	})
	var b strings.Builder
	last := 0
	for _, loc := range reSnake.FindAllStringIndex(line, -1) {
		name := line[loc[0]:loc[1]]
		g, ok := w.goNames[name]
		if !ok || reOption.MatchString(line[loc[1]:]) {
			continue
		}
		b.WriteString(line[last:loc[0]])
		b.WriteString(g)
		last = loc[1]
	}
	b.WriteString(line[last:])
	return b.String()
}

func isIdentByte(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// article returns "an" for a word starting with a vowel, "a" otherwise.
func article(word string) string {
	if word != "" && strings.ContainsRune("aeiou", rune(word[0])) {
		return "an"
	}
	return "a"
}

func (w *writer) p(layout string, args ...any) { fmt.Fprintf(&w.Buffer, layout+"\n", args...) }

// doc writes a comment block, renaming the proto identifier it opens with to
// the Go one, so "user_id is ..." documents UserID as "UserID is ...".
func (w *writer) doc(indent string, lines []string, protoName, goIdent string) {
	// Rewritten as one text, so that a name is seen with what follows it on
	// the next line.
	joined := w.goDoc(strings.Join(lines, "\n"), protoName, goIdent)
	for l := range strings.SplitSeq(joined, "\n") {
		if l == "" {
			w.p("%s//", indent)
			continue
		}
		w.p("%s// %s", indent, l)
	}
}

func generate(s *schema) ([]byte, error) {
	cmds, err := commands(s)
	if err != nil {
		return nil, err
	}
	w := writer{goNames: map[string]string{}}
	for _, m := range s.messages {
		for _, f := range m.fields {
			w.goNames[f.name] = goName(f.name)
		}
	}
	w.p("// Code generated by internal/gen from api.proto. DO NOT EDIT.")
	w.p("")
	w.p("package gocent")
	w.p("")
	w.p("import (")
	w.p("\t\"context\"")
	w.p("\t\"encoding/json/jsontext\"")
	w.p(")")

	for _, m := range s.messages {
		if skipType(s, m.name) {
			continue
		}
		w.p("")
		if len(m.doc) > 0 {
			w.doc("", m.doc, m.name, m.name)
		} else {
			w.p("// %s is the %s message of the Centrifugo server API.", m.name, m.name)
		}
		w.p("type %s struct {", m.name)
		for i, f := range m.fields {
			if i > 0 && len(f.doc) > 0 {
				w.p("")
			}
			w.doc("\t", f.doc, f.name, goName(f.name))
			w.p("\t%s %s `json:\"%s,omitzero\"`", goName(f.name), goFieldType(s, f), f.name)
		}
		w.p("}")
	}

	// Every request knows its method name, and how it sits in a batch.
	for _, c := range cmds {
		w.p("")
		w.p("// APIMethod returns %q, the Centrifugo server API method a [%s] is sent to.", c.method, c.request.name)
		w.p("// With the request encoded as JSON it makes the method and payload of an")
		w.p("// event for Centrifugo's async consumers.")
		w.p("func (%s) APIMethod() string { return %q }", c.request.name, c.method)
		w.p("")
		w.p("func (r *%s) addTo(c *command) { c.%s = r }", c.request.name, c.goName)
	}

	w.p("")
	w.p("// command is one entry of a batch: exactly one of its fields is set.")
	w.p("type command struct {")
	for _, c := range cmds {
		w.p("\t%s *%s `json:\"%s,omitzero\"`", c.goName, c.request.name, c.method)
	}
	w.p("}")
	w.p("")
	w.p("// reply is one entry of a batch reply: an error, or the result of the")
	w.p("// command at the same position.")
	w.p("type reply struct {")
	w.p("\tError *Error `json:\"error,omitzero\"`")
	for _, c := range cmds {
		w.p("\t%s *%s `json:\"%s,omitzero\"`", c.goName, strings.TrimPrefix(goBaseType(s, c.result), "*"), c.method)
	}
	w.p("}")

	w.p("")
	w.p("// methodNames holds every API method, batch included, so that New can")
	w.p("// refuse an APIEndpoint which already ends with one. A method is true")
	w.p("// when only Centrifugo PRO has it.")
	w.p("var methodNames = map[string]bool{")
	w.p("\t\"batch\": false,")
	for _, c := range cmds {
		w.p("\t%q: %t,", c.method, c.proOnly)
	}
	w.p("}")

	for _, c := range cmds {
		if c.method == "broadcast" {
			continue // written by hand: its result is a result per channel
		}
		emitMethods(&w, s, c)
	}

	src, err := format.Source(w.Bytes())
	if err != nil {
		return nil, fmt.Errorf("formatting generated code: %w\n%s", err, w.Bytes())
	}
	return src, nil
}

func emitMethods(w *writer, s *schema, c command) {
	result := goBaseType(s, c.result)
	summary := methodSummary(c)
	empty := len(c.request.fields) == 0

	w.p("")
	for _, l := range summary {
		w.p("// %s", w.goDoc(l, "", ""))
	}
	if empty {
		w.p("func (c *Client) %s(ctx context.Context) (%s, error) {", c.goName, result)
		w.p("\treturn invoke(ctx, c, &%s{}, func(r *reply) *%s { return r.%s })", c.request.name, result, c.goName)
	} else {
		w.p("func (c *Client) %s(ctx context.Context, req %s) (%s, error) {", c.goName, c.request.name, result)
		w.p("\treturn invoke(ctx, c, &req, func(r *reply) *%s { return r.%s })", result, c.goName)
	}
	w.p("}")

	w.p("")
	w.p("// %s adds %s %s command to the batch. Its outcome is what [Client.%s]", c.goName, article(c.method), c.method, c.goName)
	w.p("// would have returned; read it with [Pending.Result] once the batch is sent.")
	if empty {
		w.p("func (b *Batch) %s() *Pending[%s] {", c.goName, result)
		w.p("\treturn add(b, &%s{}, func(r *reply) (%s, error) { return deref(r.%s), nil })", c.request.name, result, c.goName)
	} else {
		w.p("func (b *Batch) %s(req %s) *Pending[%s] {", c.goName, c.request.name, result)
		w.p("\treturn add(b, &req, func(r *reply) (%s, error) { return deref(r.%s), nil })", result, c.goName)
	}
	w.p("}")
}

// methodSummary turns the request's comment, "PublishRequest publishes data
// into a channel.", into the method's, "Publish publishes data into a
// channel."
func methodSummary(c command) []string {
	doc := slices.Clone(c.request.doc)
	if len(doc) > 0 && strings.HasPrefix(doc[0], c.request.name+" ") {
		doc[0] = c.goName + strings.TrimPrefix(doc[0], c.request.name)
		return doc
	}
	return []string{fmt.Sprintf("%s calls the %s method of the Centrifugo server API.", c.goName, c.method)}
}
