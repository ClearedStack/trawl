package samgov

import (
	_ "embed"
	"reflect"
	"testing"

	"github.com/ClearedStack/trawl/internal/tooltest"
)

//go:embed match.yaml
var matchYAML []byte

func TestFixtures(t *testing.T) {
	tooltest.Run(t, New(), matchYAML, "testdata")
}

func TestAddresses(t *testing.T) {
	cases := map[string][]string{
		"":                           nil,
		"no address here":            nil,
		"http is a word, not a link": nil,
		"See https://tools.agency-example.gov/wage.":                         {"https://tools.agency-example.gov/wage"},
		"(see https://tools.agency-example.gov/wage)":                        {"https://tools.agency-example.gov/wage"},
		"Use HTTPS://Tools.Agency-Example.gov/a, then http://example.org/b!": {"HTTPS://Tools.Agency-Example.gov/a", "http://example.org/b"},
		"<a href=\"https://example.org/c\">c</a>":                            {"https://example.org/c"},
		"https://example.org/d\nhttps://example.org/e":                       {"https://example.org/d", "https://example.org/e"},
		"xhttps://example.org/not-a-link":                                    nil,
		"query kept: https://example.org/f?a=1&b=2;":                         {"https://example.org/f?a=1&b=2"},
	}
	for text, want := range cases {
		if got := addresses(text); !reflect.DeepEqual(got, want) {
			t.Errorf("addresses(%q) = %q, want %q", text, got, want)
		}
	}
}
