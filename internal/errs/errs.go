// Package errs holds the three public error codes and the only error type
// Trawl is allowed to show outside the process.
//
// A public error carries a code, the sub-tool it came from, a reason drawn
// from a fixed vocabulary, and a short fingerprint. It never carries the
// offending data, an address, a machine name or a credential.
package errs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// Code is one of the three public error codes.
type Code string

const (
	// Timeout means something outside Trawl took too long or could not be
	// reached. The item is safe to retry.
	Timeout Code = "E_TIMEOUT"
	// Parse means the data was broken or failed validation.
	Parse Code = "E_PARSE"
	// NeedsSubtool means the data was valid and no rule covers it.
	NeedsSubtool Code = "E_NEEDS_SUBTOOL"
)

// Valid reports whether c is one of the three public codes.
func (c Code) Valid() bool {
	return c == Timeout || c == Parse || c == NeedsSubtool
}

// Error is the public error. All fields are safe to print.
type Error struct {
	Code        Code
	Tool        string
	Reason      string
	Fingerprint string
}

// Error renders the error on one line, for example
// "E_PARSE tool=site-form reason=honeypot fp=1a2b3c4d5e6f".
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	if e.Tool != "" {
		b.WriteString(" tool=")
		b.WriteString(e.Tool)
	}
	if e.Reason != "" {
		b.WriteString(" reason=")
		b.WriteString(e.Reason)
	}
	if e.Fingerprint != "" {
		b.WriteString(" fp=")
		b.WriteString(e.Fingerprint)
	}
	return b.String()
}

// New builds a public error. The reason must be a token of lowercase
// letters, digits and underscores; anything else is replaced so that data
// can never leak through it. The fingerprint is derived from material and
// reveals nothing about it beyond equality.
func New(code Code, reason string, material []byte) *Error {
	return &Error{Code: code, Reason: token(reason), Fingerprint: Fingerprint(material)}
}

// WithTool returns a copy of e attributed to the named sub-tool.
func (e *Error) WithTool(tool string) *Error {
	c := *e
	c.Tool = token(tool)
	return &c
}

// As extracts a public error from err.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Fingerprint returns the first twelve hex characters of the SHA-256 digest
// of material.
func Fingerprint(material []byte) string {
	sum := sha256.Sum256(material)
	return hex.EncodeToString(sum[:])[:12]
}

// token keeps s only if it is a short identifier made of lowercase letters,
// digits, underscores and hyphens.
func token(s string) string {
	if s == "" || len(s) > 40 {
		return "unspecified"
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if !ok {
			return "unspecified"
		}
	}
	return s
}
