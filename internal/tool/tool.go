// Package tool defines what a sub-tool is: a matching rule that says which
// items it handles, and logic that turns one item into candidates. The
// sub-tools themselves live under tools/, one folder each.
package tool

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"

	"github.com/ClearedStack/trawl/internal/errs"
)

// Input is one item as a sub-tool sees it. The logic of a sub-tool is a
// pure function of this value.
type Input struct {
	ItemID string
	Source string
	Format string
	// URL is the canonical address Body was read from. It is empty when
	// the body arrived inline with the item.
	URL         string
	ContentType string
	Body        []byte
	// ObservedAt is the time the hub attached to the item, in RFC 3339
	// form. Sub-tools never read the clock.
	ObservedAt string
}

// Candidate is an address a sub-tool found, exactly as it found it.
type Candidate struct {
	URL   string
	Kind  string
	Title string
	// Via says where the address was seen, for example an issue number.
	Via string
}

// Proposal is a validated edit to one field of an existing record. It is
// passed on for owner approval and changes nothing by itself.
type Proposal struct {
	Target string `json:"target"`
	Field  string `json:"field"`
	Value  string `json:"value"`
}

// Output is what a sub-tool extracted from one item.
type Output struct {
	Candidates []Candidate
	Proposals  []Proposal
	// Rejects are entries inside the item that were broken. The rest of
	// the item still counts.
	Rejects []*errs.Error
	// RateKey, when set, names a counter the item must be counted against
	// before it is accepted, and RateLimit is the most the counter allows.
	RateKey   string
	RateLimit int
}

// Tool is the logic half of a sub-tool.
type Tool interface {
	Name() string
	Extract(in Input) (Output, error)
}

// Input modes a matching rule can ask for.
const (
	Inline = "inline"
	Fetch  = "fetch"
	Either = "either"
)

// Rule is the matching half of a sub-tool, read from its match.yaml.
type Rule struct {
	Tool  string `yaml:"tool"`
	Match struct {
		Source  string   `yaml:"source"`
		Formats []string `yaml:"formats"`
	} `yaml:"match"`
	// Input says whether items carry their document inline, name an
	// address to read it from, or may do either.
	Input string `yaml:"input"`
}

// ParseRule reads a match.yaml. Unknown keys are an error.
func ParseRule(data []byte) (Rule, error) {
	var r Rule
	if err := strictYAML(data, &r); err != nil {
		return Rule{}, err
	}
	if r.Tool == "" || r.Match.Source == "" || len(r.Match.Formats) == 0 {
		return Rule{}, errors.New("match rule needs tool, match.source and match.formats")
	}
	for _, f := range r.Match.Formats {
		if f == "" {
			return Rule{}, errors.New("match rule has an empty format")
		}
	}
	switch r.Input {
	case Inline, Fetch, Either:
	default:
		return Rule{}, errors.New("match rule input must be inline, fetch or either")
	}
	return r, nil
}

// Matches reports whether the rule covers an item with this source and
// format. Both must be equal, byte for byte.
func (r Rule) Matches(source, format string) bool {
	if source != r.Match.Source {
		return false
	}
	for _, f := range r.Match.Formats {
		if f == format {
			return true
		}
	}
	return false
}

// Registry is tools/registry.yaml: the list of sub-tools, by folder name.
type Registry struct {
	Contract string   `yaml:"contract"`
	Tools    []string `yaml:"tools"`
}

// ParseRegistry reads a registry.yaml. Unknown keys are an error.
func ParseRegistry(data []byte) (Registry, error) {
	var r Registry
	if err := strictYAML(data, &r); err != nil {
		return Registry{}, err
	}
	if r.Contract == "" || len(r.Tools) == 0 {
		return Registry{}, errors.New("registry needs contract and tools")
	}
	return r, nil
}

func strictYAML(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("more than one YAML document")
	}
	return nil
}

// Entry is a registered sub-tool: its rule and its logic.
type Entry struct {
	Rule Rule
	Tool Tool
}

// Set is the collection of registered sub-tools.
type Set struct {
	entries []Entry
}

// NewSet builds a set. Each entry's rule must name its tool, and no name
// may appear twice.
func NewSet(entries ...Entry) (*Set, error) {
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Tool == nil || e.Rule.Tool != e.Tool.Name() {
			return nil, fmt.Errorf("sub-tool %q: rule and logic disagree on the name", e.Rule.Tool)
		}
		if seen[e.Rule.Tool] {
			return nil, fmt.Errorf("sub-tool %q is registered twice", e.Rule.Tool)
		}
		seen[e.Rule.Tool] = true
	}
	return &Set{entries: entries}, nil
}

// Names lists the registered sub-tools in registry order.
func (s *Set) Names() []string {
	names := make([]string, len(s.entries))
	for i, e := range s.entries {
		names[i] = e.Rule.Tool
	}
	return names
}

// Entries returns the registered sub-tools in registry order.
func (s *Set) Entries() []Entry { return s.entries }

// Match finds the one sub-tool whose rule covers the item. When none does,
// or more than one does, it returns E_NEEDS_SUBTOOL: Trawl never picks. The
// fingerprint depends only on the source and format, so every item of the
// same unknown kind reports the same fingerprint.
func (s *Set) Match(source, format string) (Entry, error) {
	var found []Entry
	for _, e := range s.entries {
		if e.Rule.Matches(source, format) {
			found = append(found, e)
		}
	}
	material := []byte(source + "\n" + format)
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return Entry{}, errs.New(errs.NeedsSubtool, "no_matching_rule", material)
	default:
		return Entry{}, errs.New(errs.NeedsSubtool, "ambiguous_rule", material)
	}
}
