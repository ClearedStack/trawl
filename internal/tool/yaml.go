package tool

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The registry and the matching rules are written in a small, strict part
// of YAML, read here with the standard library alone:
//
//	key: value          a plain value on one line
//	key:                followed by an indented block, either
//	  nested: value       more keys, two spaces deeper, or
//	  - item              a list of plain values
//	# comment           a whole line
//
// Everything else is refused: tabs, quotes, flow lists, anchors, more than
// one document, a value and a block under one key. A file that parses here
// means the same thing to any YAML reader.

// node is a parsed value: a string, a []string, or a map[string]node.
type node any

type yamlLine struct {
	number int
	indent int
	text   string
}

// parseYAML reads a document whose top level is a mapping.
func parseYAML(data []byte) (map[string]node, error) {
	var lines []yamlLine
	for i, raw := range strings.Split(string(data), "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		if strings.ContainsRune(raw, '\t') {
			return nil, fmt.Errorf("line %d: tabs are not allowed", i+1)
		}
		text := strings.TrimRight(raw, " ")
		trimmed := strings.TrimLeft(text, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == "---" || trimmed == "..." {
			return nil, fmt.Errorf("line %d: only one document is allowed", i+1)
		}
		lines = append(lines, yamlLine{number: i + 1, indent: len(text) - len(trimmed), text: trimmed})
	}
	if len(lines) == 0 {
		return nil, errors.New("the document is empty")
	}
	if lines[0].indent != 0 {
		return nil, fmt.Errorf("line %d: the first key must not be indented", lines[0].number)
	}
	out, rest, err := parseMapping(lines, 0)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("line %d: unexpected indentation", rest[0].number)
	}
	return out, nil
}

// parseMapping reads the keys at one indentation and returns what follows.
func parseMapping(lines []yamlLine, indent int) (map[string]node, []yamlLine, error) {
	out := map[string]node{}
	for len(lines) > 0 && lines[0].indent == indent {
		line := lines[0]
		lines = lines[1:]
		if strings.HasPrefix(line.text, "-") {
			return nil, nil, fmt.Errorf("line %d: a list item where a key was expected", line.number)
		}
		colon := strings.IndexByte(line.text, ':')
		if colon <= 0 {
			return nil, nil, fmt.Errorf("line %d: expected \"key: value\"", line.number)
		}
		key, value := line.text[:colon], line.text[colon+1:]
		if !plain(key) {
			return nil, nil, fmt.Errorf("line %d: the key is not a plain word", line.number)
		}
		if _, dup := out[key]; dup {
			return nil, nil, fmt.Errorf("line %d: the key %q is given twice", line.number, key)
		}

		if value != "" {
			if !strings.HasPrefix(value, " ") || !plain(strings.TrimLeft(value, " ")) {
				return nil, nil, fmt.Errorf("line %d: the value is not a plain word", line.number)
			}
			if len(lines) > 0 && lines[0].indent > indent {
				return nil, nil, fmt.Errorf("line %d: a key has a value and a block", lines[0].number)
			}
			out[key] = strings.TrimLeft(value, " ")
			continue
		}

		// A block follows: a list or a nested mapping, two spaces in.
		if len(lines) == 0 || lines[0].indent != indent+2 {
			return nil, nil, fmt.Errorf("line %d: the key %q has no value", line.number, key)
		}
		if strings.HasPrefix(lines[0].text, "-") {
			var items []string
			for len(lines) > 0 && lines[0].indent == indent+2 {
				item, ok := strings.CutPrefix(lines[0].text, "- ")
				if !ok || !plain(item) {
					return nil, nil, fmt.Errorf("line %d: a list item must be \"- \" and a plain word", lines[0].number)
				}
				items = append(items, item)
				lines = lines[1:]
			}
			out[key] = items
			continue
		}
		nested, rest, err := parseMapping(lines, indent+2)
		if err != nil {
			return nil, nil, err
		}
		out[key] = nested
		lines = rest
	}
	if len(lines) > 0 && lines[0].indent > indent {
		return nil, nil, fmt.Errorf("line %d: unexpected indentation", lines[0].number)
	}
	return out, lines, nil
}

// plain reports whether s is a word of letters, digits, dots, hyphens,
// underscores and slashes: every name this project writes in YAML, and
// nothing a YAML reader could take for anything but a string of that
// spelling. Words YAML reads as other types are refused.
func plain(s string) bool {
	if s == "" {
		return false
	}
	hasLetter := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		hasLetter = hasLetter || letter
		if !letter && !(c >= '0' && c <= '9') && c != '.' && c != '-' && c != '_' && c != '/' {
			return false
		}
	}
	if !hasLetter || s[0] == '-' || s[0] == '.' {
		return false
	}
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "y", "n":
		return false
	}
	return true
}

// field helpers turn a parsed document into typed values and notice keys
// that should not be there.

type fields struct {
	values map[string]node
	used   map[string]bool
}

func newFields(values map[string]node) *fields {
	return &fields{values: values, used: map[string]bool{}}
}

func (f *fields) text(key string) (string, error) {
	f.used[key] = true
	v, ok := f.values[key]
	if !ok {
		return "", fmt.Errorf("%q is missing", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%q must be a single value", key)
	}
	return s, nil
}

func (f *fields) list(key string) ([]string, error) {
	f.used[key] = true
	v, ok := f.values[key]
	if !ok {
		return nil, fmt.Errorf("%q is missing", key)
	}
	l, ok := v.([]string)
	if !ok {
		return nil, fmt.Errorf("%q must be a list", key)
	}
	return l, nil
}

func (f *fields) mapping(key string) (*fields, error) {
	f.used[key] = true
	v, ok := f.values[key]
	if !ok {
		return nil, fmt.Errorf("%q is missing", key)
	}
	m, ok := v.(map[string]node)
	if !ok {
		return nil, fmt.Errorf("%q must hold keys of its own", key)
	}
	return newFields(m), nil
}

// done fails if the document has a key that was never asked for.
func (f *fields) done() error {
	var unknown []string
	for key := range f.values {
		if !f.used[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unknown key %q", unknown[0])
	}
	return nil
}
