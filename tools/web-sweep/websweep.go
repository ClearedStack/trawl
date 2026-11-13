// Package websweep is the web-sweep sub-tool. It reads one HTML page that
// was fetched from a public site and records the links on it that point to
// tools and paperwork.
//
// A link is recorded when a whole word of its text, or of the path of its
// address, is one of the words in Words. Everything else on the page is
// left alone. The page is never followed further: each page of a sweep is
// its own item, queued by whoever schedules the sweep.
//
// Links that are not addresses of a document are passed over: a link to a
// place on the same page, and mailto, tel, javascript and other schemes
// that are not http or https.
package websweep

import (
	"html"
	"strings"

	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/normalize"
	"github.com/ClearedStack/trawl/internal/screen"
	"github.com/ClearedStack/trawl/internal/tool"
)

// Name is this sub-tool's folder and registry name.
const Name = "web-sweep"

// Words are the words that mark a link as a candidate.
var Words = []string{
	"builder", "calculator", "calculators", "checklist", "checklists",
	"estimator", "estimators", "finder", "forms", "generator", "lookup",
	"template", "templates", "tool", "toolkit", "toolkits", "tools",
	"wizard", "worksheet", "worksheets",
}

var wordSet = func() map[string]bool {
	set := map[string]bool{}
	for _, w := range Words {
		set[w] = true
	}
	return set
}()

type webSweep struct{}

// New returns the web-sweep logic.
func New() tool.Tool { return webSweep{} }

func (webSweep) Name() string { return Name }

func (webSweep) Extract(in tool.Input) (tool.Output, error) {
	if in.URL == "" {
		return tool.Output{}, errs.New(errs.Parse, "no_page_address", in.Body)
	}
	if in.ContentType != "text/html" && in.ContentType != "application/xhtml+xml" {
		// A real document of a kind this sub-tool cannot read, such
		// as a PDF.
		return tool.Output{}, errs.New(errs.NeedsSubtool, "not_html", []byte(Name+"\nnot_html"))
	}

	base, anchors := scan(string(in.Body))
	docBase := in.URL
	if base != "" {
		if norm, err := normalize.Normalize(normalize.Resolve(in.URL, base)); err == nil {
			docBase = norm.URL
		}
	}

	var out tool.Output
	seen := map[string]bool{}
	for _, a := range anchors {
		href := strings.Trim(a.href, " \t\r\n")
		if href == "" || href[0] == '#' {
			continue
		}
		address := normalize.Resolve(docBase, href)
		lower := strings.ToLower(address)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			continue
		}
		if !hasWord(a.text) && !hasWord(pathOf(address)) {
			continue
		}
		if seen[address] {
			continue
		}
		seen[address] = true
		out.Candidates = append(out.Candidates, tool.Candidate{URL: address, Kind: "link", Title: a.text, Via: in.URL})
	}
	return out, nil
}

// pathOf returns the path of an absolute address, without host or query.
func pathOf(address string) string {
	rest := address[strings.Index(address, "://")+3:]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return ""
	}
	path := rest[slash:]
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	return path
}

// hasWord reports whether text contains one of Words as a whole word.
// Words are runs of ASCII letters; everything else separates them.
func hasWord(text string) bool {
	start := -1
	for i := 0; i <= len(text); i++ {
		letter := i < len(text) && (text[i] >= 'a' && text[i] <= 'z' || text[i] >= 'A' && text[i] <= 'Z')
		switch {
		case letter && start < 0:
			start = i
		case !letter && start >= 0:
			if wordSet[strings.ToLower(text[start:i])] {
				return true
			}
			start = -1
		}
	}
	return false
}

type anchor struct {
	href string
	text string
}

// scan walks an HTML document and returns the href of its first base
// element and every anchor that has an href, with the anchor's text. It
// does not build a tree: it reads tags in order, skips comments and the
// content of script and style elements, and forgives markup that is not
// closed. The document is never run and nothing in it is fetched.
func scan(doc string) (base string, anchors []anchor) {
	lower := strings.ToLower(doc)
	open := -1 // index into anchors of the anchor being read
	var text strings.Builder
	closeAnchor := func() {
		if open >= 0 {
			anchors[open].text = strings.TrimSpace(html.UnescapeString(screen.StripHTML(text.String())))
			open = -1
		}
		text.Reset()
	}

	for i := 0; i < len(doc); {
		lt := strings.IndexByte(doc[i:], '<')
		if lt < 0 {
			text.WriteString(doc[i:])
			break
		}
		text.WriteString(doc[i : i+lt])
		i += lt
		rest := lower[i:]

		switch {
		case strings.HasPrefix(rest, "<!--"):
			end := strings.Index(rest, "-->")
			if end < 0 {
				i = len(doc)
			} else {
				i += end + len("-->")
			}
			continue
		case tagNamed(rest, "script"), tagNamed(rest, "style"):
			name := "script"
			if tagNamed(rest, "style") {
				name = "style"
			}
			end := strings.Index(rest, "</"+name)
			if end < 0 {
				i = len(doc)
				continue
			}
			gt := strings.IndexByte(rest[end:], '>')
			if gt < 0 {
				i = len(doc)
			} else {
				i += end + gt + 1
			}
			text.WriteByte(' ')
			continue
		}

		end := tagEnd(doc, i)
		if end < 0 {
			break
		}
		tag := doc[i : end+1]
		switch {
		case tagNamed(rest, "a"):
			closeAnchor()
			if href, ok := attribute(tag, "href"); ok {
				anchors = append(anchors, anchor{href: href})
				open = len(anchors) - 1
			}
		case strings.HasPrefix(rest, "</a") && (len(rest) == 3 || !isNameByte(rest[3])):
			closeAnchor()
		case tagNamed(rest, "base"):
			if href, ok := attribute(tag, "href"); ok && base == "" {
				base = href
			}
			text.WriteByte(' ')
		default:
			text.WriteByte(' ')
		}
		i = end + 1
	}
	closeAnchor()
	return base, anchors
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == ':' || c == '_'
}

// tagNamed reports whether lowercase text starts with an opening tag of
// the given name.
func tagNamed(rest, name string) bool {
	if !strings.HasPrefix(rest, "<"+name) {
		return false
	}
	after := rest[1+len(name):]
	return after == "" || !isNameByte(after[0])
}

// tagEnd returns the index of the ">" that closes the tag starting at i,
// passing over any ">" inside a quoted attribute value. It returns -1 when
// the tag is never closed.
func tagEnd(doc string, i int) int {
	quote := byte(0)
	for j := i + 1; j < len(doc); j++ {
		c := doc[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return j
		}
	}
	return -1
}

// attribute returns the value of the named attribute of a tag, with
// character references decoded. Names are compared without regard to
// letter case. The first occurrence wins.
func attribute(tag, name string) (string, bool) {
	// Skip "<" and the tag name.
	i := 1
	for i < len(tag) && !isSpace(tag[i]) && tag[i] != '>' && tag[i] != '/' {
		i++
	}
	for i < len(tag) {
		for i < len(tag) && (isSpace(tag[i]) || tag[i] == '/') {
			i++
		}
		if i >= len(tag) || tag[i] == '>' {
			break
		}
		start := i
		for i < len(tag) && !isSpace(tag[i]) && tag[i] != '=' && tag[i] != '>' && tag[i] != '/' {
			i++
		}
		attr := strings.ToLower(tag[start:i])
		for i < len(tag) && isSpace(tag[i]) {
			i++
		}
		value := ""
		if i < len(tag) && tag[i] == '=' {
			i++
			for i < len(tag) && isSpace(tag[i]) {
				i++
			}
			if i < len(tag) && (tag[i] == '"' || tag[i] == '\'') {
				quote := tag[i]
				i++
				start = i
				for i < len(tag) && tag[i] != quote {
					i++
				}
				value = tag[start:i]
				i++
			} else {
				start = i
				for i < len(tag) && !isSpace(tag[i]) && tag[i] != '>' {
					i++
				}
				value = tag[start:i]
			}
		}
		if attr == name {
			return html.UnescapeString(value), true
		}
	}
	return "", false
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}
