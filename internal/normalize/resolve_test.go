package normalize

import "testing"

func TestResolve(t *testing.T) {
	const base = "https://tools.agency-example.gov/a/b/page?x=1"
	cases := []struct{ ref, want string }{
		{"https://calc.depot-example.mil/rates", "https://calc.depot-example.mil/rates"},
		{"HTTP://Example.org/A", "HTTP://Example.org/A"},
		{"mailto:made-up@example.org", "mailto:made-up@example.org"},
		{"javascript:void(0)", "javascript:void(0)"},
		{"//calc.depot-example.mil/rates", "https://calc.depot-example.mil/rates"},
		{"/wage", "https://tools.agency-example.gov/wage"},
		{"/wage/../rates/./table", "https://tools.agency-example.gov/rates/table"},
		{"/../../wage", "https://tools.agency-example.gov/wage"},
		{"wage", "https://tools.agency-example.gov/a/b/wage"},
		{"./wage", "https://tools.agency-example.gov/a/b/wage"},
		{"../wage", "https://tools.agency-example.gov/a/wage"},
		{"../../wage", "https://tools.agency-example.gov/wage"},
		{"../../../wage", "https://tools.agency-example.gov/wage"},
		{"..", "https://tools.agency-example.gov/a/"},
		{".", "https://tools.agency-example.gov/a/b/"},
		{"wage/", "https://tools.agency-example.gov/a/b/wage/"},
		{"wage?y=2#top", "https://tools.agency-example.gov/a/b/wage?y=2#top"},
		{"?y=2", "https://tools.agency-example.gov/a/b/page?y=2"},
		{"#top", "https://tools.agency-example.gov/a/b/page?x=1"},
		{"", "https://tools.agency-example.gov/a/b/page?x=1"},
		{"  wage \n", "https://tools.agency-example.gov/a/b/wage"},
		{"wage?next=https://example.org/", "https://tools.agency-example.gov/a/b/wage?next=https://example.org/"},
		{"a:b", "a:b"},
		{"./a:b", "https://tools.agency-example.gov/a/b/a:b"},
	}
	for _, c := range cases {
		if got := Resolve(base, c.ref); got != c.want {
			t.Errorf("Resolve(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
	if got := Resolve("https://tools.agency-example.gov/", "wage"); got != "https://tools.agency-example.gov/wage" {
		t.Errorf("from the root: %q", got)
	}
}
