package normalize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ClearedStack/trawl/internal/dedupe"
	"github.com/ClearedStack/trawl/internal/golden"
)

// fixture is one saved example: an input and the exact output expected.
type fixture struct {
	Name    string `json:"name"`
	Input   string `json:"input"`
	URL     string `json:"url,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Applied []Rule `json:"applied,omitempty"`
	Error   string `json:"error,omitempty"`
}

func run(input string) fixture {
	got := fixture{Input: input}
	res, err := Normalize(input)
	if err != nil {
		got.Error = err.Error()
		return got
	}
	got.URL = res.URL
	got.SHA256 = dedupe.Hash(res.URL)
	got.Applied = res.Applied
	return got
}

func load(t *testing.T, rule Rule) (string, []fixture) {
	t.Helper()
	path := filepath.Join("testdata", string(rule)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("rule %s has no fixture file: %v", rule, err)
	}
	var cases []fixture
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return path, cases
}

// TestGolden checks every saved example of every rule, and that each rule
// has at least one example in which it is the rule at work.
func TestGolden(t *testing.T) {
	for _, info := range Rules {
		t.Run(string(info.Rule), func(t *testing.T) {
			path, cases := load(t, info.Rule)
			exercised := false
			for i, want := range cases {
				got := run(want.Input)
				got.Name = want.Name
				if golden.Update() {
					cases[i] = got
				} else if !reflect.DeepEqual(got, normalizeEmpty(want)) {
					t.Errorf("%s:\n input   %q\n got     %+v\n want    %+v", want.Name, want.Input, got, want)
				}
				if info.Rejects {
					exercised = exercised || got.Error != ""
				} else {
					for _, r := range got.Applied {
						exercised = exercised || r == info.Rule
					}
				}
			}
			if !exercised {
				t.Errorf("no fixture in %s exercises the rule", path)
			}
			if golden.Update() {
				golden.JSON(t, path, cases)
			}
		})
	}
}

func normalizeEmpty(f fixture) fixture {
	if f.Applied == nil && f.Error == "" {
		f.Applied = []Rule{}
	}
	return f
}

// TestNoStrayFixtures fails when a fixture file names no rule.
func TestNoStrayFixtures(t *testing.T) {
	known := map[string]bool{}
	for _, info := range Rules {
		known[string(info.Rule)+".json"] = true
	}
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !known[e.Name()] {
			t.Errorf("testdata/%s matches no rule", e.Name())
		}
	}
}

// TestIdempotent checks that a canonical address is its own canonical form.
func TestIdempotent(t *testing.T) {
	for _, info := range Rules {
		_, cases := load(t, info.Rule)
		for _, c := range cases {
			first, err := Normalize(c.Input)
			if err != nil {
				continue
			}
			second, err := Normalize(first.URL)
			if err != nil {
				t.Errorf("%s: canonical form %q was refused: %v", c.Name, first.URL, err)
				continue
			}
			if second.URL != first.URL || len(second.Applied) != 0 {
				t.Errorf("%s: %q normalized again to %q by %v", c.Name, first.URL, second.URL, second.Applied)
			}
		}
	}
}

// TestErrorsCarryNoInput checks that a refusal never repeats the address.
func TestErrorsCarryNoInput(t *testing.T) {
	inputs := []string{
		"ftp://files.agency-example.gov/secret-path",
		"https://someone:hunter2@agency-example.gov/",
		"https://10.20.30.40/admin",
		"https://bücher.example.com/",
	}
	for _, in := range inputs {
		_, err := Normalize(in)
		if err == nil {
			t.Fatalf("%q was accepted", in)
		}
		msg := err.Error()
		for _, leak := range []string{"agency-example", "hunter2", "10.20.30.40", "secret-path", "bücher"} {
			if strings.Contains(msg, leak) {
				t.Errorf("error %q repeats input", msg)
			}
		}
	}
}

func FuzzNormalize(f *testing.F) {
	for _, seed := range []string{
		"https://agency-example.gov/tools/?utm_source=x#top",
		"HTTP://Example.COM:80/a/b/index.html",
		"https://example.org/a;jsessionid=ABC?b=2&a=1",
		"not an address",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		first, err := Normalize(in)
		if err != nil {
			return
		}
		second, err := Normalize(first.URL)
		if err != nil || second.URL != first.URL {
			t.Fatalf("not idempotent: %q -> %q -> %q (%v)", in, first.URL, second.URL, err)
		}
	})
}
