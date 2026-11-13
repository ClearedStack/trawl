// Package screen cleans the free text Trawl keeps and refuses text that
// carries a control marking. Both are fixed rules over the bytes given.
package screen

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// markings are banner and portion markings of controlled information. A
// plain mention of a word such as "CUI" is not a marking; these patterns
// are the forms a marked document actually carries.
var markings = []string{
	"CUI//",
	"(CUI)",
	"CONTROLLED//",
	"//FOUO",
	"(FOUO)",
	"(U//FOUO)",
	"UNCLASSIFIED//FOR OFFICIAL USE ONLY",
	"CONFIDENTIAL//",
	"SECRET//",
	"//NOFORN",
	"//REL TO",
	"//SP-",
	"(C)//",
	"(S//",
	"(TS//",
	"SOURCE SELECTION INFORMATION",
}

// Marked reports whether text carries a control marking. Trawl accepts
// public material only, so marked text is refused and never stored.
func Marked(text string) bool {
	upper := strings.ToUpper(text)
	for _, m := range markings {
		if strings.Contains(upper, m) {
			return true
		}
	}
	return false
}

// StripHTML removes markup from text: script and style elements go with
// their content, comments go, and every other tag goes while its content
// stays. Text that only looks like the start of a tag is cut at that point,
// so nothing that could open a tag survives.
func StripHTML(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	lower := strings.ToLower(text)
	for i := 0; i < len(text); {
		if text[i] != '<' {
			b.WriteByte(text[i])
			i++
			continue
		}
		rest := lower[i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			end := strings.Index(rest, "-->")
			if end < 0 {
				return b.String()
			}
			i += end + len("-->")
		case elementStart(rest, "script"):
			i = skipElement(lower, i, "script")
		case elementStart(rest, "style"):
			i = skipElement(lower, i, "style")
		default:
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				return b.String()
			}
			i += end + 1
		}
		b.WriteByte(' ')
	}
	return b.String()
}

func elementStart(rest, name string) bool {
	if !strings.HasPrefix(rest, "<"+name) {
		return false
	}
	after := rest[1+len(name):]
	return after == "" || after[0] == '>' || after[0] == '/' || after[0] == ' ' || after[0] == '\t' || after[0] == '\n' || after[0] == '\r'
}

// skipElement returns the index just past the closing tag of the element
// that opens at i, or the end of the text when it is never closed.
func skipElement(lower string, i int, name string) int {
	closeTag := "</" + name
	at := strings.Index(lower[i:], closeTag)
	if at < 0 {
		return len(lower)
	}
	end := strings.IndexByte(lower[i+at:], '>')
	if end < 0 {
		return len(lower)
	}
	return i + at + end + 1
}

// Text returns text ready to keep: markup stripped, control characters
// dropped, runs of white space folded to one space, ends trimmed, and the
// result cut to at most max characters.
func Text(text string, max int) string {
	text = StripHTML(strings.ToValidUTF8(text, ""))
	var b strings.Builder
	b.Grow(len(text))
	space := false
	count := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if unicode.IsControl(r) || r == utf8.RuneError || !unicode.IsPrint(r) {
			continue
		}
		need := 1
		if space {
			need = 2
		}
		if count+need > max {
			break
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
		count += need
	}
	return b.String()
}
