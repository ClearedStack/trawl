// Package tool defines what a sub-tool is: a matching rule that says which
// items it handles, and logic that turns one item into candidates. The
// sub-tools themselves live under tools/, one folder each.
package tool

import (
	"errors"
	"fmt"

	"github.com/ClearedStack/trawl/internal/errs"
)

// Input is one document as a sub-tool sees it. The logic of a sub-tool is
// a pure function of this value. Sub-tools never read the clock.
type Input struct {
	Source string
	Format string
	// URL is the canonical address Body was read from. It is empty when
	// the body arrived inline with a leased item.
	URL         string
	ContentType string
	Body        []byte
}

// Candidate is an address a sub-tool found, exactly as it found it.
type Candidate struct {
	URL   string
	Kind  string
	Title string
	// Via says where the address was seen, for example an issue number.
	Via string
}

// Output is what a sub-tool extracted from one document.
type Output struct {
	Candidates []Candidate
	// Rejects are entries inside the document that were broken. The rest
	// of the document still counts.
	Rejects []*errs.Error
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
	Tool  string
	Match struct {
		Source  string
		Formats []string
	}
	// Input says whether the sub-tool's documents arrive inline with a
	// leased item, are read from an address, or may do either.
	Input string
}

// ParseRule reads a match.yaml. Unknown keys are an error.
func ParseRule(data []byte) (Rule, error) {
	doc, err := parseYAML(data)
	if err != nil {
		return Rule{}, err
	}
	var r Rule
	top := newFields(doc)
	if r.Tool, err = top.text("tool"); err != nil {
		return Rule{}, err
	}
	if r.Input, err = top.text("input"); err != nil {
		return Rule{}, err
	}
	match, err := top.mapping("match")
	if err != nil {
		return Rule{}, err
	}
	if r.Match.Source, err = match.text("source"); err != nil {
		return Rule{}, err
	}
	if r.Match.Formats, err = match.list("formats"); err != nil {
		return Rule{}, err
	}
	if err := match.done(); err != nil {
		return Rule{}, err
	}
	if err := top.done(); err != nil {
		return Rule{}, err
	}
	switch r.Input {
	case Inline, Fetch, Either:
	default:
		return Rule{}, errors.New("match rule input must be inline, fetch or either")
	}
	return r, nil
}

// Matches reports whether the rule covers a document with this source and
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
	Contract string
	Tools    []string
}

// ParseRegistry reads a registry.yaml. Unknown keys are an error.
func ParseRegistry(data []byte) (Registry, error) {
	doc, err := parseYAML(data)
	if err != nil {
		return Registry{}, err
	}
	var r Registry
	top := newFields(doc)
	if r.Contract, err = top.text("contract"); err != nil {
		return Registry{}, err
	}
	if r.Tools, err = top.list("tools"); err != nil {
		return Registry{}, err
	}
	if err := top.done(); err != nil {
		return Registry{}, err
	}
	return r, nil
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

// Source finds the one sub-tool whose rule names the source, whatever the
// format. It is used for a leased item, which carries its source and no
// format. When no rule names the source, or more than one does, it
// returns E_NEEDS_SUBTOOL.
func (s *Set) Source(source string) (Entry, error) {
	var found []Entry
	for _, e := range s.entries {
		if e.Rule.Match.Source == source {
			found = append(found, e)
		}
	}
	material := []byte(source)
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return Entry{}, errs.New(errs.NeedsSubtool, "no_matching_rule", material)
	default:
		return Entry{}, errs.New(errs.NeedsSubtool, "ambiguous_rule", material)
	}
}

// Match finds the one sub-tool whose rule covers the document. When none does,
// or more than one does, it returns E_NEEDS_SUBTOOL: Trawl never picks. The
// fingerprint depends only on the source and format, so every document of the
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
