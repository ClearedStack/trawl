// Package githubissues is the github-issues sub-tool. It reads suggestions
// that were opened as issues on the public repository.
//
// The document is one issue or a list of issues as JSON. Of each issue
// only number, labels, body and the presence of pull_request are read;
// nothing about the person who opened it is read or kept.
//
// An issue is a suggestion when it carries exactly one of two labels, and
// its body must then be the matching form, written as "### Heading"
// sections the way GitHub renders an issue form:
//
//	label tool-suggestion        label paperwork-suggestion
//	### Tool address             ### Document address
//	### Tool name                ### Document name
//	### What it does             ### What it is for
//
// The address section is required and must hold one address. The name is
// kept as the title. The last section is read past and not kept.
//
// What happens to an issue:
//
//	a pull request, or no suggestion label   skipped: not a suggestion
//	both labels, or headings outside the form  the item pauses, E_NEEDS_SUBTOOL
//	no number, no body, no single address    that issue is refused, E_PARSE
package githubissues

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/screen"
	"github.com/ClearedStack/trawl/internal/tool"
)

// Name is this sub-tool's folder and registry name.
const Name = "github-issues"

// form describes one issue form.
type form struct {
	label   string
	kind    string
	address string
	name    string
	about   string
}

var forms = []form{
	{label: "tool-suggestion", kind: "tool", address: "Tool address", name: "Tool name", about: "What it does"},
	{label: "paperwork-suggestion", kind: "paperwork", address: "Document address", name: "Document name", about: "What it is for"},
}

// noResponse is what GitHub writes for a form field left empty.
const noResponse = "_No response_"

type githubIssues struct{}

// New returns the github-issues logic.
func New() tool.Tool { return githubIssues{} }

func (githubIssues) Name() string { return Name }

func (githubIssues) Extract(in tool.Input) (tool.Output, error) {
	if !json.Valid(in.Body) {
		return tool.Output{}, errs.New(errs.Parse, "bad_json", in.Body)
	}
	unfamiliar := func(reason string) (tool.Output, error) {
		return tool.Output{}, errs.New(errs.NeedsSubtool, reason, []byte(Name+"\n"+reason))
	}

	var issues []json.RawMessage
	switch body := bytes.TrimLeft(in.Body, " \t\r\n"); body[0] {
	case '[':
		if err := json.Unmarshal(in.Body, &issues); err != nil {
			return tool.Output{}, errs.New(errs.Parse, "bad_json", in.Body)
		}
	case '{':
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(in.Body, &probe); err != nil {
			return tool.Output{}, errs.New(errs.Parse, "bad_json", in.Body)
		}
		if _, ok := probe["number"]; !ok {
			// A JSON object that is not an issue, such as a notice
			// from the service in place of the data.
			return unfamiliar("unfamiliar_document")
		}
		issues = []json.RawMessage{in.Body}
	default:
		return unfamiliar("unfamiliar_document")
	}

	var out tool.Output
	for _, raw := range issues {
		refuse := func(reason string) {
			out.Rejects = append(out.Rejects, errs.New(errs.Parse, reason, raw))
		}

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			refuse("bad_issue")
			continue
		}
		number, ok := positiveInt(fields["number"])
		if !ok {
			refuse("bad_issue")
			continue
		}
		if _, isPull := fields["pull_request"]; isPull {
			continue
		}
		labels, ok := labelNames(fields["labels"])
		if !ok {
			refuse("bad_issue")
			continue
		}
		var matched []form
		for _, f := range forms {
			if labels[f.label] {
				matched = append(matched, f)
			}
		}
		if len(matched) == 0 {
			continue
		}
		if len(matched) > 1 {
			return unfamiliar("ambiguous_labels")
		}
		f := matched[0]

		var body string
		if err := json.Unmarshal(fields["body"], &body); err != nil || strings.TrimSpace(body) == "" {
			refuse("no_body")
			continue
		}
		if screen.Marked(body) {
			refuse("controlled_marking")
			continue
		}
		sections, ok := parseForm(body, f)
		if !ok {
			return unfamiliar("unfamiliar_form")
		}
		address := sections[f.address]
		if address == "" {
			refuse("no_address")
			continue
		}
		if strings.ContainsAny(address, " \t\n") {
			refuse("address_not_single")
			continue
		}
		out.Candidates = append(out.Candidates, tool.Candidate{
			URL:   address,
			Kind:  f.kind,
			Title: sections[f.name],
			Via:   Name + "#" + strconv.Itoa(number),
		})
	}
	return out, nil
}

func positiveInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 || len(raw) > 9 {
		return 0, false
	}
	n, err := strconv.Atoi(string(raw))
	return n, err == nil && n > 0
}

// labelNames reads labels given as objects with a name, or as plain
// strings. A missing or null list is an empty one.
func labelNames(raw json.RawMessage) (map[string]bool, bool) {
	names := map[string]bool{}
	if len(raw) == 0 || string(raw) == "null" {
		return names, true
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, false
	}
	for _, item := range list {
		var name string
		if err := json.Unmarshal(item, &name); err == nil {
			names[name] = true
			continue
		}
		var obj struct {
			Name *string `json:"name"`
		}
		if err := json.Unmarshal(item, &obj); err != nil || obj.Name == nil {
			return nil, false
		}
		names[*obj.Name] = true
	}
	return names, true
}

// parseForm splits an issue body into its "### Heading" sections and
// checks them against the form: the address heading must be present, no
// heading may repeat, no heading may be outside the form, and nothing but
// blank lines may come before the first heading.
func parseForm(body string, f form) (map[string]string, bool) {
	allowed := map[string]bool{f.address: true, f.name: true, f.about: true}
	sections := map[string]string{}
	current := ""
	var text []string
	flush := func() {
		if current != "" {
			value := strings.TrimSpace(strings.Join(text, "\n"))
			if value == noResponse {
				value = ""
			}
			sections[current] = value
		}
		text = text[:0]
	}

	body = strings.ReplaceAll(body, "\r\n", "\n")
	for _, line := range strings.Split(body, "\n") {
		heading, isHeading := strings.CutPrefix(line, "### ")
		if !isHeading {
			if current == "" && strings.TrimSpace(line) != "" {
				return nil, false
			}
			text = append(text, line)
			continue
		}
		flush()
		heading = strings.TrimSpace(heading)
		if _, repeated := sections[heading]; repeated || !allowed[heading] {
			return nil, false
		}
		current = heading
		sections[current] = ""
	}
	flush()
	if _, ok := sections[f.address]; !ok {
		return nil, false
	}
	return sections, true
}
