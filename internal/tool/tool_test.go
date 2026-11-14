package tool

import (
	"strings"
	"testing"

	"github.com/ClearedStack/trawl/internal/errs"
)

type fake struct{ name string }

func (f fake) Name() string                  { return f.name }
func (f fake) Extract(Input) (Output, error) { return Output{}, nil }

const ruleYAML = `
tool: made-up
match:
  source: made-up-source
  formats:
    - made-up.v1
    - made-up.v2
input: either
`

func TestParseRule(t *testing.T) {
	r, err := ParseRule([]byte(ruleYAML))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Matches("made-up-source", "made-up.v1") || !r.Matches("made-up-source", "made-up.v2") {
		t.Error("rule should match its formats")
	}
	for _, c := range [][2]string{
		{"made-up-source", "made-up.v3"},
		{"other", "made-up.v1"},
		{"Made-Up-Source", "made-up.v1"},
		{"made-up-source", ""},
		{"", ""},
	} {
		if r.Matches(c[0], c[1]) {
			t.Errorf("rule matched %q %q", c[0], c[1])
		}
	}
}

func TestParseRuleRefuses(t *testing.T) {
	bad := map[string]string{
		"unknown key":        ruleYAML + "extra: word\n",
		"unknown nested key": "tool: a\nmatch:\n  source: a\n  formats:\n    - x\n  extra: word\ninput: inline\n",
		"no formats":         "tool: a\nmatch:\n  source: a\ninput: inline\n",
		"flow list":          "tool: a\nmatch:\n  source: a\n  formats: [x]\ninput: inline\n",
		"quoted value":       "tool: 'a'\nmatch:\n  source: a\n  formats:\n    - x\ninput: inline\n",
		"bad input":          "tool: a\nmatch:\n  source: a\n  formats:\n    - x\ninput: sometimes\n",
		"no tool":            "match:\n  source: a\n  formats:\n    - x\ninput: inline\n",
		"two documents":      ruleYAML + "---\ntool: b\n",
		"repeated key":       ruleYAML + "tool: again\n",
		"tab":                "tool:\ta\n",
		"value and block":    "tool: a\n  more: b\n",
		"odd indentation":    "tool: a\nmatch:\n   source: a\n",
		"list as value":      "tool:\n  - a\nmatch:\n  source: a\n  formats:\n    - x\ninput: inline\n",
		"word yaml retypes":  "tool: yes\nmatch:\n  source: a\n  formats:\n    - x\ninput: inline\n",
		"number":             "tool: 7\nmatch:\n  source: a\n  formats:\n    - x\ninput: inline\n",
		"empty":              "# nothing here\n",
		"not yaml":           ":\n  - [",
	}
	for name, text := range bad {
		if _, err := ParseRule([]byte(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseYAMLShapes(t *testing.T) {
	doc, err := parseYAML([]byte("# comment\na: one\n\nb:\n  c: two\n  d:\n    - x.v1\n    - y/z\n  e:\n    f: three\ng: four\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := doc["b"].(map[string]node)
	if doc["a"] != "one" || doc["g"] != "four" || b["c"] != "two" ||
		strings.Join(b["d"].([]string), ",") != "x.v1,y/z" || b["e"].(map[string]node)["f"] != "three" {
		t.Errorf("doc = %#v", doc)
	}
}

func TestParseRegistry(t *testing.T) {
	r, err := ParseRegistry([]byte("contract: v1\ntools:\n  - a\n  - b\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Contract != "v1" || strings.Join(r.Tools, ",") != "a,b" {
		t.Errorf("registry = %+v", r)
	}
	for _, bad := range []string{"tools:\n  - a\n", "contract: v1\n", "contract: v1\ntools:\n  - a\nmore: word\n", "contract: v1\ntools: a\n"} {
		if _, err := ParseRegistry([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func entry(name, source, format string) Entry {
	var r Rule
	r.Tool = name
	r.Match.Source = source
	r.Match.Formats = []string{format}
	r.Input = Inline
	return Entry{Rule: r, Tool: fake{name}}
}

func TestSetMatch(t *testing.T) {
	set, err := NewSet(entry("one", "s1", "f1"), entry("two", "s2", "f2"), entry("twin", "s2", "f2"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := set.Match("s1", "f1")
	if err != nil || got.Tool.Name() != "one" {
		t.Fatalf("Match = %v, %v", got, err)
	}

	_, err = set.Match("s9", "f9")
	e, ok := errs.As(err)
	if !ok || e.Code != errs.NeedsSubtool || e.Reason != "no_matching_rule" {
		t.Fatalf("unknown item: %v", err)
	}
	_, again := set.Match("s9", "f9")
	if again.Error() != err.Error() {
		t.Error("the same unknown kind must give the same fingerprint")
	}
	_, other := set.Match("s9", "f8")
	if other.Error() == err.Error() {
		t.Error("a different unknown kind must give a different fingerprint")
	}

	_, err = set.Match("s2", "f2")
	e, ok = errs.As(err)
	if !ok || e.Code != errs.NeedsSubtool || e.Reason != "ambiguous_rule" {
		t.Fatalf("two matching rules: %v", err)
	}
}

func TestSetSource(t *testing.T) {
	set, err := NewSet(entry("one", "s1", "f1"), entry("two", "s2", "f2"), entry("twin", "s2", "f3"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := set.Source("s1"); err != nil || got.Tool.Name() != "one" {
		t.Fatalf("Source = %v, %v", got, err)
	}
	_, err = set.Source("s9")
	if e, ok := errs.As(err); !ok || e.Code != errs.NeedsSubtool || e.Reason != "no_matching_rule" {
		t.Errorf("unknown source: %v", err)
	}
	_, err = set.Source("s2")
	if e, ok := errs.As(err); !ok || e.Code != errs.NeedsSubtool || e.Reason != "ambiguous_rule" {
		t.Errorf("two rules for one source: %v", err)
	}
}

func TestNewSetRefuses(t *testing.T) {
	if _, err := NewSet(entry("one", "s", "f"), entry("one", "s2", "f2")); err == nil {
		t.Error("duplicate name accepted")
	}
	mismatch := entry("one", "s", "f")
	mismatch.Tool = fake{"other"}
	if _, err := NewSet(mismatch); err == nil {
		t.Error("rule and logic with different names accepted")
	}
}
