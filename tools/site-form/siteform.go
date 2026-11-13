// Package siteform is the site-form sub-tool. It validates a submission
// from the form on clearedstack.com and turns it into either a candidate
// address or a proposed edit.
//
// A submission is a JSON object of at most 4 KB:
//
//	kind      "candidate" or "edit"
//	url       candidate: the address being suggested
//	title     candidate: an optional name, at most 120 characters
//	target    edit: the identifier of the record, such as PA-0007
//	field     edit: the field to change; it must be in ownership_map.json
//	value     edit: the new value
//	website   the honeypot; a person leaves it empty
//	pow       the proof of work: {"challenge": "...", "nonce": "..."}
//	ip_hash   the form's daily-salted hash of the sender's address
//
// Of the five layers that shield the form, this sub-tool checks four: the
// proof of work, the honeypot, the rate limit (it names the counter; the
// worker counts through the hub) and the schema with markup stripped. The
// fifth, owner approval, happens after Trawl.
//
// Trawl never sees a raw network address. A submission that carries any
// key not listed above, such as "ip", is refused.
package siteform

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"io"
	"math/bits"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/ClearedStack/trawl/internal/dedupe"
	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/normalize"
	"github.com/ClearedStack/trawl/internal/screen"
	"github.com/ClearedStack/trawl/internal/tool"
)

const (
	// Name is this sub-tool's folder and registry name.
	Name = "site-form"
	// MaxBytes is the largest submission accepted.
	MaxBytes = 4096
	// MaxTitle is the longest candidate title, in characters.
	MaxTitle = 120
	// WorkBits is how many leading zero bits the proof of work must
	// have: about a second of hashing in a browser.
	WorkBits = 18
	// PerDay is how many submissions one sender hash may make in a day.
	PerDay = 5
)

//go:embed ownership_map.json
var ownershipJSON []byte

type ownedField struct {
	Owner string `json:"owner"`
	Max   int    `json:"max"`
	Type  string `json:"type"`
}

type ownershipMap struct {
	Version int                   `json:"version"`
	Fields  map[string]ownedField `json:"fields"`
}

var ownership = func() ownershipMap {
	var m ownershipMap
	dec := json.NewDecoder(bytes.NewReader(ownershipJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil || len(m.Fields) == 0 {
		panic("site-form: ownership_map.json is not valid")
	}
	return m
}()

type proof struct {
	Challenge string `json:"challenge"`
	Nonce     string `json:"nonce"`
}

type submission struct {
	Kind    string `json:"kind"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Target  string `json:"target"`
	Field   string `json:"field"`
	Value   string `json:"value"`
	Website string `json:"website"`
	Proof   *proof `json:"pow"`
	IPHash  string `json:"ip_hash"`
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

	if s.Website != "" {
		return refuse("honeypot")
	}
	if !isHex64(s.IPHash) {
		return refuse("schema")
	}
	observed, err := time.Parse(time.RFC3339, in.ObservedAt)
	if err != nil {
		return refuse("schema")
	}
	if screen.Marked(string(in.Body)) {
		return refuse("controlled_marking")
	}

	out := tool.Output{
		RateKey:   Name + "/" + observed.UTC().Format("2006-01-02") + "/" + s.IPHash,
		RateLimit: PerDay,
	}
	var subject string

	switch s.Kind {
	case "candidate":
		if s.URL == "" || len(s.URL) > normalize.MaxLength || s.Target != "" || s.Field != "" || s.Value != "" {
			return refuse("schema")
		}
		if utf8.RuneCountInString(s.Title) > MaxTitle {
			return refuse("schema")
		}
		subject = s.URL
		out.Candidates = []tool.Candidate{{URL: s.URL, Kind: "submission", Title: s.Title, Via: Name}}

	case "edit":
		if s.URL != "" || s.Title != "" || s.Value == "" {
			return refuse("schema")
		}
		if _, ok := dedupe.ParseID(s.Target); !ok {
			return refuse("schema")
		}
		owned, ok := ownership.Fields[s.Field]
		if !ok {
			return refuse("field_not_owned")
		}
		if utf8.RuneCountInString(s.Value) > owned.Max {
			return refuse("schema")
		}
		value := screen.Text(s.Value, owned.Max)
		if owned.Type == "url" {
			norm, err := normalize.Normalize(s.Value)
			if err != nil {
				return tool.Output{}, err
			}
			value = norm.URL
		}
		if value == "" {
			return refuse("schema")
		}
		subject = s.Target + "/" + s.Field
		out.Proposals = []tool.Proposal{{Target: s.Target, Field: s.Field, Value: value}}

	case "":
		return refuse("schema")

	default:
		// A well-formed submission of a kind no rule covers.
		return tool.Output{}, errs.New(errs.NeedsSubtool, "unknown_kind", []byte(Name+"\nunknown_kind"))
	}

	if s.Proof == nil || !proofToken(s.Proof.Challenge, 8, 64) || !proofToken(s.Proof.Nonce, 1, 32) {
		return refuse("schema")
	}
	if !Verify(s.Proof.Challenge, subject, s.Proof.Nonce) {
		return refuse("proof_of_work")
	}
	return out, nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// proofToken reports whether s is min to max characters drawn from
// letters, digits, hyphen and underscore.
func proofToken(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

// work returns the number of leading zero bits in the SHA-256 digest of
// the challenge, the subject and the nonce, each on its own line. The
// subject is the address of a candidate exactly as typed, or the target
// and field of an edit joined by a slash, so the work cannot be reused for
// a different submission.
func work(challenge, subject, nonce string) int {
	sum := sha256.Sum256([]byte(challenge + "\n" + subject + "\n" + nonce))
	zeros := 0
	for _, b := range sum {
		if b != 0 {
			return zeros + bits.LeadingZeros8(b)
		}
		zeros += 8
	}
	return zeros
}

// Verify reports whether the nonce is a valid proof of work.
func Verify(challenge, subject, nonce string) bool {
	return work(challenge, subject, nonce) >= WorkBits
}

// Solve finds a nonce by counting up from zero. It is what the form does
// in the browser, and it is how the fixtures were made.
func Solve(challenge, subject string) string {
	for n := 0; ; n++ {
		nonce := strconv.Itoa(n)
		if Verify(challenge, subject, nonce) {
			return nonce
		}
	}
}
