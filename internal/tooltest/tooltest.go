// Package tooltest runs a sub-tool against its saved fixtures. Every
// sub-tool's test is one call to Run.
//
// A sub-tool's testdata folder holds:
//
//	match.json          envelopes with the matching result expected
//	<case>/item.json    the envelope: source, format, and for a fetched
//	                    document its address and content type
//	<case>/body.*       the document, byte for byte
//	<case>/want.json    the exact outcome expected
package tooltest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ClearedStack/trawl/internal/golden"
	"github.com/ClearedStack/trawl/internal/intake"
	"github.com/ClearedStack/trawl/internal/tool"
)

type matchCase struct {
	Source string `json:"source"`
	Format string `json:"format"`
	Match  bool   `json:"match"`
}

type itemFile struct {
	Source      string `json:"source"`
	Format      string `json:"format"`
	URL         string `json:"url,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// want is the saved outcome: a result, or the public error for the
// whole document.
type want struct {
	Result *intake.Result `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// Run checks the matching rule and every case folder under dir.
func Run(t *testing.T, tl tool.Tool, ruleYAML []byte, dir string) {
	t.Helper()
	rule, err := tool.ParseRule(ruleYAML)
	if err != nil {
		t.Fatalf("match.yaml: %v", err)
	}
	if rule.Tool != tl.Name() {
		t.Fatalf("match.yaml names %q, the logic names %q", rule.Tool, tl.Name())
	}

	t.Run("match", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(dir, "match.json"))
		if err != nil {
			t.Fatalf("no matching fixtures: %v", err)
		}
		var cases []matchCase
		if err := json.Unmarshal(data, &cases); err != nil {
			t.Fatal(err)
		}
		yes, no := 0, 0
		for _, c := range cases {
			if got := rule.Matches(c.Source, c.Format); got != c.Match {
				t.Errorf("Matches(%q, %q) = %v, want %v", c.Source, c.Format, got, c.Match)
			}
			if c.Match {
				yes++
			} else {
				no++
			}
		}
		if yes == 0 || no == 0 {
			t.Error("match.json needs at least one matching and one non-matching envelope")
		}
	})

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no fixture cases")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			caseDir := filepath.Join(dir, name)
			in := load(t, caseDir)
			if !rule.Matches(in.Source, in.Format) {
				t.Fatalf("the fixture's envelope does not match this sub-tool's rule")
			}
			first, second := outcome(tl, in), outcome(tl, in)
			a, _ := golden.Marshal(first)
			b, _ := golden.Marshal(second)
			if string(a) != string(b) {
				t.Fatal("two runs over the same input differ")
			}
			golden.Bytes(t, filepath.Join(caseDir, "want.json"), a)
		})
	}
}

func load(t *testing.T, caseDir string) tool.Input {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(caseDir, "item.json"))
	if err != nil {
		t.Fatal(err)
	}
	var it itemFile
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&it); err != nil {
		t.Fatalf("item.json: %v", err)
	}
	bodies, err := filepath.Glob(filepath.Join(caseDir, "body.*"))
	if err != nil || len(bodies) != 1 {
		t.Fatalf("want exactly one body file, found %d", len(bodies))
	}
	body, err := os.ReadFile(bodies[0])
	if err != nil {
		t.Fatal(err)
	}
	return tool.Input{
		Source:      it.Source,
		Format:      it.Format,
		URL:         it.URL,
		ContentType: it.ContentType,
		Body:        body,
	}
}

func outcome(tl tool.Tool, in tool.Input) want {
	res, err := intake.Process(tl, in)
	if err != nil {
		return want{Error: err.Error()}
	}
	return want{Result: &res}
}
