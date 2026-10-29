package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// schema is what the generator needs from a proto file: its messages and the
// methods of its service, each with the comment written above it.
type schema struct {
	messages []*message
	byName   map[string]*message
	methods  []*method
}

type message struct {
	name   string
	doc    []string
	fields []*field
}

type field struct {
	name     string // as in the proto, snake case
	typ      string // scalar or message type; the value type for maps
	repeated bool
	mapKey   string // set for map fields
	doc      []string
}

type method struct {
	name     string
	request  string
	response string
	// streaming is set for a method streaming its request or response:
	// one only GRPC can call.
	streaming bool
}

// api.proto and proxy.proto use a small part of the proto3 language: top-level
// messages with scalar, message, repeated and map fields, and one service whose
// methods may stream. The parser accepts
// exactly that and rejects anything else, so that a construct it does not
// understand - a oneof, an enum, a nested message - fails the generation rather
// than being dropped from the SDK without a word.
var (
	reMessageOpen  = regexp.MustCompile(`^message\s+(\w+)\s*\{\s*(\})?$`)
	reField        = regexp.MustCompile(`^(repeated\s+)?(\w+)\s+(\w+)\s*=\s*\d+\s*;$`)
	reMapField     = regexp.MustCompile(`^map\s*<\s*(\w+)\s*,\s*(\w+)\s*>\s+(\w+)\s*=\s*\d+\s*;$`)
	reReserved     = regexp.MustCompile(`^reserved\s+[\d\s,to]+;$`)
	reServiceOpen  = regexp.MustCompile(`^service\s+\w+\s*\{$`)
	reRPC          = regexp.MustCompile(`^rpc\s+(\w+)\s*\(\s*(stream\s+)?(\w+)\s*\)\s*returns\s*\(\s*(stream\s+)?(\w+)\s*\)\s*(?:\{\s*\}|;)$`)
	reTopLevelSkip = regexp.MustCompile(`^(syntax|package|option)\b.*;$`)
)

// parse reads a proto file; name is how errors refer to it.
func parse(r io.Reader, name string) (*schema, error) {
	s := &schema{byName: map[string]*message{}}
	sc := bufio.NewScanner(r)
	var (
		lineNo    int
		comment   []string
		current   *message
		inService bool
	)
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s:%d: %s", name, lineNo, fmt.Sprintf(format, args...))
	}
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			// A blank line ends a comment block: only a comment directly above
			// a declaration documents it.
			comment = nil
			continue
		case strings.HasPrefix(line, "//"):
			comment = append(comment, strings.TrimSpace(strings.TrimPrefix(line, "//")))
			continue
		case strings.Contains(line, "//"):
			// A trailing comment documents its line when nothing above does.
			i := strings.Index(line, "//")
			if len(comment) == 0 {
				comment = []string{strings.TrimSpace(line[i+2:])}
			}
			line = strings.TrimSpace(line[:i])
		}

		switch {
		case current == nil && !inService:
			if reTopLevelSkip.MatchString(line) {
				break
			}
			if m := reMessageOpen.FindStringSubmatch(line); m != nil {
				msg := &message{name: m[1], doc: comment}
				if s.byName[msg.name] != nil {
					return nil, fail("message %s declared twice", msg.name)
				}
				s.messages = append(s.messages, msg)
				s.byName[msg.name] = msg
				if m[2] == "" {
					current = msg
				}
				break
			}
			if reServiceOpen.MatchString(line) {
				inService = true
				break
			}
			return nil, fail("unsupported top-level declaration: %q", line)
		case inService:
			if line == "}" {
				inService = false
				break
			}
			m := reRPC.FindStringSubmatch(line)
			if m == nil {
				return nil, fail("unsupported service declaration: %q", line)
			}
			s.methods = append(s.methods, &method{name: m[1], request: m[3], response: m[5], streaming: m[2] != "" || m[4] != ""})
		default:
			if line == "}" {
				current = nil
				break
			}
			if reReserved.MatchString(line) {
				break
			}
			if m := reMapField.FindStringSubmatch(line); m != nil {
				current.fields = append(current.fields, &field{name: m[3], typ: m[2], mapKey: m[1], doc: comment})
				break
			}
			if m := reField.FindStringSubmatch(line); m != nil {
				current.fields = append(current.fields, &field{name: m[3], typ: m[2], repeated: m[1] != "", doc: comment})
				break
			}
			return nil, fail("unsupported declaration in message %s: %q", current.name, line)
		}
		comment = nil
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if current != nil || inService {
		return nil, fmt.Errorf("%s: unexpected end of file", name)
	}
	if err := s.check(name); err != nil {
		return nil, err
	}
	return s, nil
}

// check verifies every message type a field or method refers to is declared.
func (s *schema) check(name string) error {
	for _, m := range s.messages {
		for _, f := range m.fields {
			if _, scalar := scalarTypes[f.typ]; !scalar && s.byName[f.typ] == nil {
				return fmt.Errorf("%s: %s.%s has unknown type %s", name, m.name, f.name, f.typ)
			}
			if f.mapKey != "" && f.mapKey != "string" {
				return fmt.Errorf("%s: %s.%s: only string map keys are supported", name, m.name, f.name)
			}
		}
	}
	for _, rpc := range s.methods {
		if s.byName[rpc.request] == nil || s.byName[rpc.response] == nil {
			return fmt.Errorf("%s: rpc %s refers to an undeclared message", name, rpc.name)
		}
	}
	return nil
}
