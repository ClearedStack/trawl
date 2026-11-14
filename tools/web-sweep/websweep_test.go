package websweep

import (
	_ "embed"
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/ClearedStack/trawl/internal/fetch"
	"github.com/ClearedStack/trawl/internal/intake"
	"github.com/ClearedStack/trawl/internal/tool"
	"github.com/ClearedStack/trawl/internal/tooltest"
)

//go:embed match.yaml
var matchYAML []byte

func TestFixtures(t *testing.T) {
	tooltest.Run(t, New(), matchYAML, "testdata")
}

func TestHasWord(t *testing.T) {
	yes := []string{"Wage calculator", "TOOLS", "/resources/tools/", "/wage-calculator.html", "small-business_toolkit", "Forms", "a tool."}
	for _, text := range yes {
		if !hasWord(text) {
			t.Errorf("hasWord(%q) = false", text)
		}
	}
	no := []string{"", "About us", "toolbox", "retool", "stools", "format", "performs", "/contact-form", "calculate"}
	for _, text := range no {
		if hasWord(text) {
			t.Errorf("hasWord(%q) = true", text)
		}
	}
}

func TestWordsAreSortedAndLowercase(t *testing.T) {
	if !sort.StringsAreSorted(Words) {
		t.Error("Words must stay sorted so the list reads the same everywhere")
	}
	for _, w := range Words {
		if !hasWord(w) {
			t.Errorf("%q is not matched by its own rule", w)
		}
	}
}

func TestAttribute(t *testing.T) {
	cases := []struct {
		tag, want string
		ok        bool
	}{
		{`<a href="/tools">`, "/tools", true},
		{`<a href='/tools'>`, "/tools", true},
		{`<a href=/tools>`, "/tools", true},
		{`<A HREF = "/tools" class=x>`, "/tools", true},
		{`<a class="x" href="/a?b=1&amp;c=2">`, "/a?b=1&c=2", true},
		{`<a title="href=/wrong" href="/right">`, "/right", true},
		{`<a title="a > b" href="/tools">`, "/tools", true},
		{`<a data-href="/wrong">`, "", false},
		{`<a>`, "", false},
		{`<a href>`, "", true},
		{`<a href="/first" href="/second">`, "/first", true},
		{`<a/href="/tools"/>`, "/tools", true},
	}
	for _, c := range cases {
		got, ok := attribute(c.tag, "href")
		if got != c.want || ok != c.ok {
			t.Errorf("attribute(%s) = %q, %v; want %q, %v", c.tag, got, ok, c.want, c.ok)
		}
	}
}

func TestScan(t *testing.T) {
	doc := `<html><head><base href="/resources/"><base href="/ignored/">
<script>var a = '<a href="/in-script">x</a>';</script>
<style>a[href="/in-style"] { color: red }</style></head>
<body><!-- <a href="/in-comment">x</a> -->
<a href="one">First <b>tool</b></a> text <a name="top">no href</a>
<A HREF='two'>Second &amp; last
<a href=three>unclosed`
	base, anchors := scan(doc)
	if base != "/resources/" {
		t.Errorf("base = %q", base)
	}
	want := []anchor{{"one", "First  tool"}, {"two", "Second & last"}, {"three", "unclosed"}}
	if !reflect.DeepEqual(anchors, want) {
		t.Errorf("anchors = %q", anchors)
	}
}

func TestScanSurvivesBrokenMarkup(t *testing.T) {
	for _, doc := range []string{"", "<", "<a", "<a href=\"", "<!--", "<script>", "</a>", "<a href=x><", "<<<>>>", "<a href='x'>t</a", "<base>"} {
		scan(doc) // must not panic or loop
	}
}

// TestLargestPageStaysSmall reads a page of the largest size the fetcher
// accepts and checks how much memory the whole pass allocates. The worker
// has a 30 MiB budget; one page must use only a small part of it.
func TestLargestPageStaysSmall(t *testing.T) {
	var b strings.Builder
	b.WriteString("<html><body>")
	for i := 0; b.Len() < fetch.MaxBody-200; i++ {
		fmt.Fprintf(&b, "<p>Made-up paragraph %d with filler text to pad the page out.</p>", i)
		if i%8 == 0 {
			fmt.Fprintf(&b, `<a href="/tools/%d">Tool %d</a>`, i, i)
		}
	}
	b.WriteString("</body></html>")
	in := tool.Input{URL: "https://www.agency-example.gov/big", ContentType: "text/html", Body: []byte(b.String())}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res, err := intake.Process(New(), in)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) < 1000 {
		t.Fatalf("only %d links found", len(res.Candidates))
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d bytes in, %d links out, %d KiB allocated", len(in.Body), len(res.Candidates), allocated>>10)
	if allocated > 8<<20 {
		t.Errorf("allocated %d KiB for one page; the limit is 8 MiB", allocated>>10)
	}
}
