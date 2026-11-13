package normalize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type fixture struct {
	Rule  string `json:"rule"`
	Spec  string `json:"spec"`
	Cases []struct {
		Name      string `json:"name"`
		Input     string `json:"input"`
		Canonical string `json:"canonical"`
		SHA256    string `json:"sha256"`
		Code      string `json:"code"`
		Reason    string `json:"reason"`
	} `json:"cases"`
}

func loadFixtures(t *testing.T) map[string]fixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "rules", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	all := make(map[string]fixture)
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f fixture
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if want := strings.TrimSuffix(filepath.Base(p), ".json"); f.Rule != want {
			t.Fatalf("%s: rule is %q, want %q", p, f.Rule, want)
		}
		all[f.Rule] = f
	}
	return all
}

// Every saved example must produce exactly its saved output.
func TestGolden(t *testing.T) {
	for rule, f := range loadFixtures(t) {
		for _, c := range f.Cases {
			t.Run(rule+"/"+c.Name, func(t *testing.T) {
				got, err := Normalize(c.Input)
				if c.Code != "" {
					if c.Canonical != "" || c.SHA256 != "" {
						t.Fatal("fixture has both an output and an error")
					}
					e, ok := err.(*Error)
					if !ok {
						t.Fatalf("Normalize = %q, %v; want %s %s", got, err, c.Code, c.Reason)
					}
					if e.Code != c.Code || e.Reason != c.Reason {
						t.Fatalf("error = %s %s; want %s %s", e.Code, e.Reason, c.Code, c.Reason)
					}
					if got != "" {
						t.Fatalf("output %q returned with an error", got)
					}
					return
				}
				if err != nil {
					t.Fatalf("Normalize: %v; want %q", err, c.Canonical)
				}
				if got != c.Canonical {
					t.Fatalf("canonical\n got %q\nwant %q", got, c.Canonical)
				}
				if h := Hash(got); h != c.SHA256 {
					t.Fatalf("sha256\n got %s\nwant %s", h, c.SHA256)
				}
				again, err := Normalize(got)
				if err != nil || again != got {
					t.Fatalf("not stable: Normalize(%q) = %q, %v", got, again, err)
				}
			})
		}
	}
}

// Every rule and every error reason must have a saved example, and every
// fixture file must belong to a rule.
func TestEveryRuleHasFixtures(t *testing.T) {
	all := loadFixtures(t)
	known := make(map[string]bool)
	for _, rule := range Rules {
		known[rule] = true
		if len(all[rule].Cases) == 0 {
			t.Errorf("rule %q has no saved example", rule)
		}
	}
	reasons := make(map[string]bool)
	for rule, f := range all {
		if !known[rule] {
			t.Errorf("fixture file %q is not a rule", rule)
		}
		for _, c := range f.Cases {
			if c.Reason != "" {
				reasons[c.Reason] = true
			}
		}
	}
	var missing []string
	for reason := range Reasons {
		if !reasons[reason] {
			missing = append(missing, reason)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("reasons with no saved example: %v", missing)
	}
}

// Every name in the three built-in lists must appear in a saved example, so
// that adding or removing one fails a test.
func TestEveryListedNameHasFixture(t *testing.T) {
	var inputs []string
	for _, f := range loadFixtures(t) {
		for _, c := range f.Cases {
			inputs = append(inputs, strings.ToLower(c.Input))
		}
	}
	has := func(part string) bool {
		for _, in := range inputs {
			if strings.Contains(in, part) {
				return true
			}
		}
		return false
	}
	for name := range strippedParams {
		if !has("?"+name) && !has("&"+name+"=") {
			t.Errorf("stripped parameter %q has no saved example", name)
		}
	}
	for name := range defaultIndex {
		if !has("/" + name) {
			t.Errorf("default index %q has no saved example", name)
		}
	}
	for name := range nonPublicSuffix {
		if !has("."+name+"/") && !has("//"+name+"/") {
			t.Errorf("non-public name %q has no saved example", name)
		}
	}
}

func TestErrorsNeverEchoInput(t *testing.T) {
	_, err := Normalize("https://user:hunter2@db-7.internal/x")
	if err == nil {
		t.Fatal("accepted")
	}
	for _, part := range []string{"user", "hunter2", "db-7", "internal/"} {
		if strings.Contains(err.Error(), part) {
			t.Fatalf("error %q contains input text %q", err, part)
		}
	}
}

func FuzzNormalize(f *testing.F) {
	for _, fx := range loadFixturesForFuzz() {
		f.Add(fx)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := Normalize(in)
		if err != nil {
			e, ok := err.(*Error)
			if !ok || Reasons[e.Reason] != e.Code || out != "" {
				t.Fatalf("bad error %v (output %q)", err, out)
			}
			return
		}
		again, err := Normalize(out)
		if err != nil || again != out {
			t.Fatalf("not stable: %q -> %q -> %q, %v", in, out, again, err)
		}
		if strings.ContainsAny(out, " \t\r\n#") {
			t.Fatalf("output %q has whitespace or a fragment", out)
		}
	})
}

func loadFixturesForFuzz() []string {
	paths, _ := filepath.Glob(filepath.Join("testdata", "rules", "*.json"))
	var inputs []string
	for _, p := range paths {
		data, _ := os.ReadFile(p)
		var f fixture
		if json.Unmarshal(data, &f) == nil {
			for _, c := range f.Cases {
				inputs = append(inputs, c.Input)
			}
		}
	}
	return inputs
}
