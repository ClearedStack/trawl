// Package siteform is the site-form sub-tool. It validates a candidate
// that a visitor sent through the form on clearedstack.com.
//
// Conn files the submission with the hub, and the hub checks the
// visitor's proof of work and daily limit before it records anything.
// Trawl then leases the item and is given what was recorded, which is
// this sub-tool's document: a JSON object of at most 4 KB with exactly
// three keys.
//
//	source    "site-form"
//	title     the name the visitor gave
//	address   the address the visitor gave, as typed
//
// Of the five layers that shield the form, the first three (proof of
// work, honeypot, rate limit) are kept before Trawl, and the fifth (owner
// approval) after it. This sub-tool is the fourth: the shape and size
// check. Markup is stripped from the title and the address is cleaned by
// the rules every candidate passes.
//
// A document with any other key is refused, so a visitor's network
// address has nowhere to travel. A proposed edit to an existing record is
// not a candidate: it goes from Conn to the hub and on to the owner, and
// never reaches Trawl.
package siteform

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/normalize"
	"github.com/ClearedStack/trawl/internal/screen"
	"github.com/ClearedStack/trawl/internal/tool"
)

const (
	// Name is this sub-tool's folder and registry name, and the source
	// it reads.
	Name = "site-form"
	// MaxBytes is the largest document accepted.
	MaxBytes = 4096
)

type submission struct {
	Source  string `json:"source"`
	Title   string `json:"title"`
	Address string `json:"address"`
}

type siteForm struct{}

// New returns the site-form logic.
func New() tool.Tool { return siteForm{} }

func (siteForm) Name() string { return Name }

func (siteForm) Extract(in tool.Input) (tool.Output, error) {
	refuse := func(reason string) (tool.Output, error) {
		return tool.Output{}, errs.New(errs.Parse, reason, in.Body)
	}

	if len(in.Body) > MaxBytes {
		return refuse("too_large")
	}
	var s submission
	dec := json.NewDecoder(bytes.NewReader(in.Body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		if _, ok := err.(*json.SyntaxError); ok || err == io.EOF || err == io.ErrUnexpectedEOF {
			return refuse("bad_json")
		}
		return refuse("schema")
	}
	if _, err := dec.Token(); err != io.EOF {
		return refuse("bad_json")
	}
	if s.Source != Name || s.Address == "" || len(s.Address) > normalize.MaxLength {
		return refuse("schema")
	}
	if screen.Marked(string(in.Body)) {
		return refuse("controlled_marking")
	}
	return tool.Output{Candidates: []tool.Candidate{{URL: s.Address, Kind: "submission", Title: s.Title, Via: Name}}}, nil
}
