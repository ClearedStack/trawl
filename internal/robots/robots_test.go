package robots

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/", "/", true},
		{"/", "/tools", true},
		{"/tools", "/tools", true},
		{"/tools", "/tools/wage", true},
		{"/tools", "/toolshed", true},
		{"/tools/", "/tools", false},
		{"/tools", "/Tools", false},
		{"/tools$", "/tools", true},
		{"/tools$", "/tools/", false},
		{"/*.pdf", "/files/a.pdf", true},
		{"/*.pdf", "/files/a.pdf?x=1", true},
		{"/*.pdf$", "/files/a.pdf", true},
		{"/*.pdf$", "/files/a.pdf?x=1", false},
		{"/a/*/c", "/a/b/c", true},
		{"/a/*/c", "/a/c", false},
		{"/a/*/c", "/a/b/d/c/e", true},
		{"/a*b*c$", "/a-b-c", true},
		{"/a*b*c$", "/a-b-c-b", false},
		{"/a*b$", "/ab", true},
		{"/a*bb$", "/ab", false},
		{"*", "/anything", true},
		{"/*?", "/search?q=x", true},
		{"/*?", "/search", false},
		{"/private*", "/private", true},
	}
	for _, c := range cases {
		if got := match(c.pattern, c.path); got != c.want {
			t.Errorf("match(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

const sample = `
# A made-up robots.txt file.
User-agent: *
Disallow: /private/
Disallow: /search
Allow: /search/help
Disallow: /*.zip$

User-agent: trawl
User-agent: OtherBot
Disallow: /tools/drafts/
Allow: /tools/drafts/public/
Crawl-delay: 10
Sitemap: https://tools.agency-example.gov/sitemap.xml

User-agent: greedy
Disallow: /
`

func TestAllowed(t *testing.T) {
	r := Parse([]byte(sample))
	cases := []struct {
		agent, path string
		want        bool
	}{
		// trawl has its own group, so the "*" group does not apply.
		{"trawl", "/private/notes", true},
		{"trawl", "/tools/wage", true},
		{"trawl", "/tools/drafts/x", false},
		{"trawl", "/tools/drafts/public/x", true},
		{"TRAWL", "/tools/drafts/x", false},
		{"otherbot", "/tools/drafts/x", false},
		// An unnamed crawler falls back to "*".
		{"unnamed", "/private/notes", false},
		{"unnamed", "/search", false},
		{"unnamed", "/search?q=wage", false},
		{"unnamed", "/search/help", true},
		{"unnamed", "/files/forms.zip", false},
		{"unnamed", "/files/forms.zip?x=1", true},
		{"unnamed", "/tools/drafts/x", true},
		{"greedy", "/", false},
		{"greedy", "/anything", false},
		{"greedy", "/robots.txt", true},
		{"unnamed", "", true},
	}
	for _, c := range cases {
		if got := r.Allowed(c.agent, c.path); got != c.want {
			t.Errorf("Allowed(%q, %q) = %v, want %v", c.agent, c.path, got, c.want)
		}
	}
}

func TestEdges(t *testing.T) {
	// No file content: everything is allowed.
	if !Parse(nil).Allowed("trawl", "/x") {
		t.Error("empty file should allow")
	}
	// An empty disallow allows everything.
	if !Parse([]byte("User-agent: *\nDisallow:\n")).Allowed("trawl", "/x") {
		t.Error("empty disallow should allow")
	}
	// Rules before any user-agent line apply to no one.
	if !Parse([]byte("Disallow: /\n")).Allowed("trawl", "/x") {
		t.Error("orphan rule should be ignored")
	}
	// Equal length: allow wins.
	tie := Parse([]byte("User-agent: *\nDisallow: /a\nAllow: /a\n"))
	if !tie.Allowed("trawl", "/a") {
		t.Error("allow should win a tie")
	}
	// Two groups naming the same crawler are read together.
	two := Parse([]byte("User-agent: trawl\nDisallow: /a\n\nUser-agent: trawl\nDisallow: /b\n"))
	if two.Allowed("trawl", "/a") || two.Allowed("trawl", "/b") || !two.Allowed("trawl", "/c") {
		t.Error("groups for the same crawler should combine")
	}
	// Carriage returns, odd spacing and letter case in keys.
	messy := Parse([]byte("USER-AGENT : *\r\n  disallow :   /x   # comment\r\n"))
	if messy.Allowed("trawl", "/x/y") {
		t.Error("messy file should still disallow")
	}
	// Junk does not break parsing.
	junk := Parse([]byte("<html>not a robots file</html>\nUser-agent: *\nDisallow: /x\n"))
	if junk.Allowed("trawl", "/x") {
		t.Error("rule after junk should apply")
	}
}
