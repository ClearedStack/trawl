package githubissues

import (
	_ "embed"
	"testing"

	"github.com/ClearedStack/trawl/internal/tooltest"
)

//go:embed match.yaml
var matchYAML []byte

func TestFixtures(t *testing.T) {
	tooltest.Run(t, New(), matchYAML, "testdata")
}

func TestParseForm(t *testing.T) {
	f := forms[0]
	good := "### Tool address\n\nhttps://tools.agency-example.gov/wage\n\n### Tool name\n\n_No response_\n\n### What it does\n\nMade-up text.\n"
	sections, ok := parseForm(good, f)
	if !ok || sections["Tool address"] != "https://tools.agency-example.gov/wage" || sections["Tool name"] != "" {
		t.Fatalf("sections = %v, %v", sections, ok)
	}
	bad := map[string]string{
		"text before the first heading": "Hello\n\n### Tool address\n\nhttps://example.org/\n",
		"heading outside the form":      "### Tool address\n\nhttps://example.org/\n\n### Price\n\n1\n",
		"repeated heading":              "### Tool address\n\nhttps://example.org/\n\n### Tool address\n\nhttps://example.com/\n",
		"address heading missing":       "### Tool name\n\nMade-up\n",
		"the other form":                "### Document address\n\nhttps://example.org/\n",
		"no headings":                   "https://example.org/",
	}
	for name, body := range bad {
		if _, ok := parseForm(body, f); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}
