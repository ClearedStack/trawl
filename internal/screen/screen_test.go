package screen

import (
	"strings"
	"testing"
)

func TestMarked(t *testing.T) {
	marked := []string{
		"CUI//SP-PRVCY",
		"(CUI) The made-up paragraph.",
		"(U//FOUO) made-up",
		"cui//basic",
		"SECRET//NOFORN",
		"UNCLASSIFIED//FOR OFFICIAL USE ONLY",
		"Source Selection Information - see made-up clause",
		"CONTROLLED//made-up",
	}
	for _, text := range marked {
		if !Marked(text) {
			t.Errorf("%q should be refused", text)
		}
	}
	plain := []string{
		"",
		"A guide to marking CUI",
		"CUI decision helper",
		"Top secret recipe for wage tables",
		"Explains FOUO and what replaced it",
		"Proprietary data checklist",
	}
	for _, text := range plain {
		if Marked(text) {
			t.Errorf("%q should be accepted", text)
		}
	}
}

func TestStripHTML(t *testing.T) {
	cases := map[string]string{
		"plain text":                         "plain text",
		"<b>bold</b> text":                   " bold  text",
		"a<script>alert(1)</script>b":        "a b",
		"a<SCRIPT src=x>alert(1)</SCRIPT >b": "a b",
		"a<style>p{color:red}</style>b":      "a b",
		"a<!-- hidden -->b":                  "a b",
		"a<script>never closed":              "a ",
		"a<!-- never closed":                 "a",
		"a <img src=x onerror=alert(1)":      "a ",
		"2 < 3 and 4 > 1":                    "2   1",
		"a<scripted>b</scripted>c":           "a b c",
		"&lt;b&gt; stays as written":         "&lt;b&gt; stays as written",
	}
	for in, want := range cases {
		if got := StripHTML(in); got != want {
			t.Errorf("StripHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripHTMLLeavesNoTagStart(t *testing.T) {
	inputs := []string{
		"<<script>script>alert(1)<</script>/script>",
		"<scr<script>ipt>alert(1)",
		"<a href='x'>link</a><",
		"<<b>>",
	}
	for _, in := range inputs {
		if got := StripHTML(in); strings.Contains(got, "<") {
			t.Errorf("StripHTML(%q) = %q still has a tag start", in, got)
		}
	}
}

func TestText(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"  Wage   calculator \n", 120, "Wage calculator"},
		{"<b>Wage</b>\tcalculator", 120, "Wage calculator"},
		{"Wage\x00 calc\x07ulator", 120, "Wage calculator"},
		{"abcdefghij", 4, "abcd"},
		{"abc defghij", 4, "abc"},
		{"ab cd ef", 5, "ab cd"},
		{"café menu", 4, "café"},
		{"", 10, ""},
		{"<script>alert(1)</script>", 10, ""},
		{"bad \xff bytes", 20, "bad bytes"},
	}
	for _, c := range cases {
		if got := Text(c.in, c.max); got != c.want {
			t.Errorf("Text(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
