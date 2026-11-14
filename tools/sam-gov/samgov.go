// Package samgov is the sam-gov sub-tool. It reads one page of public
// notices and pulls out the addresses a notice points to.
//
// The page is a JSON object with an opportunitiesData list. Of each notice
// these fields are read:
//
//	noticeId            required; names the notice in the "via" of a candidate
//	title               kept as the title of each candidate from the notice
//	additionalInfoLink  an address, recorded as a link
//	resourceLinks       a list of addresses, each recorded as an attachment
//	description         free text; every http or https address in it is
//	                    recorded as a link
//
// A link whose address contains the word "calculator" is recorded with the
// kind calculator instead of link.
//
// Two things are deliberately not recorded. The notice's own page (uiLink)
// is the notice, not a tool. And when the description is nothing but one
// address, it is the feed's pointer to the notice text, which needs a key
// to read; Trawl holds no key and does not follow it.
package samgov

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/screen"
	"github.com/ClearedStack/trawl/internal/tool"
)

// Name is this sub-tool's folder and registry name.
const Name = "sam-gov"

const listKey = "opportunitiesData"

type samGov struct{}

// New returns the sam-gov logic.
func New() tool.Tool { return samGov{} }

func (samGov) Name() string { return Name }

type notice struct {
	NoticeID           *string   `json:"noticeId"`
	Title              *string   `json:"title"`
	AdditionalInfoLink *string   `json:"additionalInfoLink"`
	ResourceLinks      []*string `json:"resourceLinks"`
	Description        *string   `json:"description"`
}

func (samGov) Extract(in tool.Input) (tool.Output, error) {
	broken := func(reason string) (tool.Output, error) {
		return tool.Output{}, errs.New(errs.Parse, reason, in.Body)
	}
	unfamiliar := func() (tool.Output, error) {
		return tool.Output{}, errs.New(errs.NeedsSubtool, "unfamiliar_document", []byte(Name+"\nunfamiliar_document"))
	}

	if !json.Valid(in.Body) {
		return broken("bad_json")
	}
	if bytes.TrimLeft(in.Body, " \t\r\n")[0] != '{' {
		return unfamiliar()
	}

	// Walk the top-level object and decode the notices one at a time, so
	// that a long page never has to be held twice.
	dec := json.NewDecoder(bytes.NewReader(in.Body))
	dec.Token() // the opening brace; the document is known to be valid
	var out tool.Output
	found := false
	for dec.More() {
		keyToken, _ := dec.Token()
		key, _ := keyToken.(string)
		if key != listKey {
			var skip json.RawMessage
			dec.Decode(&skip)
			continue
		}
		if found {
			return broken("bad_feed")
		}
		found = true

		var list json.RawMessage
		dec.Decode(&list)
		trimmed := bytes.TrimSpace(list)
		if string(trimmed) == "null" {
			continue
		}
		if trimmed[0] != '[' {
			return broken("bad_feed")
		}
		items := json.NewDecoder(bytes.NewReader(trimmed))
		items.Token()
		for items.More() {
			var raw json.RawMessage
			items.Decode(&raw)
			readNotice(raw, &out)
		}
	}
	if !found {
		return unfamiliar()
	}
	return out, nil
}

// readNotice adds the candidates of one notice to out, or one reject if
// the notice is broken.
func readNotice(raw json.RawMessage, out *tool.Output) {
	refuse := func(reason string) {
		out.Rejects = append(out.Rejects, errs.New(errs.Parse, reason, raw))
	}
	var n notice
	if err := json.Unmarshal(raw, &n); err != nil || n.NoticeID == nil || !validID(*n.NoticeID) {
		refuse("bad_notice")
		return
	}
	for _, link := range n.ResourceLinks {
		if link == nil {
			refuse("bad_notice")
			return
		}
	}
	title, description := "", ""
	if n.Title != nil {
		title = *n.Title
	}
	if n.Description != nil {
		description = *n.Description
	}
	if screen.Marked(title) || screen.Marked(description) {
		refuse("controlled_marking")
		return
	}

	via := Name + ":" + *n.NoticeID
	add := func(address, kind string) {
		out.Candidates = append(out.Candidates, tool.Candidate{URL: address, Kind: kind, Title: title, Via: via})
	}
	if n.AdditionalInfoLink != nil && strings.TrimSpace(*n.AdditionalInfoLink) != "" {
		add(*n.AdditionalInfoLink, linkKind(*n.AdditionalInfoLink))
	}
	for _, link := range n.ResourceLinks {
		add(*link, "attachment")
	}
	if found := addresses(description); !(len(found) == 1 && found[0] == strings.TrimSpace(description)) {
		for _, address := range found {
			add(address, linkKind(address))
		}
	}
}

// validID accepts 1 to 64 letters, digits, hyphens and underscores.
func validID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

func linkKind(address string) string {
	if strings.Contains(strings.ToLower(address), "calculator") {
		return "calculator"
	}
	return "link"
}

// addresses finds every http or https address in free text. An address
// starts at "http://" or "https://" in any letter case, and runs to the
// next white space, quote, angle bracket or the end of the text.
// Punctuation that ends a sentence or closes a bracket is dropped from
// its end.
func addresses(text string) []string {
	var found []string
	lower := strings.ToLower(text)
	for i := 0; i < len(text); {
		at := strings.Index(lower[i:], "http")
		if at < 0 {
			break
		}
		start := i + at
		rest := lower[start:]
		if !strings.HasPrefix(rest, "http://") && !strings.HasPrefix(rest, "https://") {
			i = start + len("http")
			continue
		}
		// Part of a longer word, such as "xhttp://", is not an address.
		if start > 0 && isWordByte(text[start-1]) {
			i = start + len("http")
			continue
		}
		end := start
		for end < len(text) && !strings.ContainsRune(" \t\r\n\"'<>", rune(text[end])) {
			end++
		}
		address := strings.TrimRight(text[start:end], ".,;:!?)]}")
		found = append(found, address)
		i = end
	}
	return found
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
