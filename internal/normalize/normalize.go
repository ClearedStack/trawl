// Package normalize turns a candidate address into its canonical form.
//
// The rules here are the ones SPEC.md lists under "Deterministic URL
// Normalization" and no others. The address is split by hand rather than with
// net/url so that the result does not depend on the Go release used to build
// Trawl. Anything the specification does not mention (ports, dot segments,
// percent-encoding, parameter order, "www.") is passed through byte for byte.
package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// MaxAddressBytes is the longest address accepted, after trimming.
const MaxAddressBytes = 4096

// Public error codes. Trawl has three; this package never produces E_TIMEOUT
// because it does not touch the network.
const (
	CodeParse        = "E_PARSE"
	CodeNeedsSubtool = "E_NEEDS_SUBTOOL"
)

// Reasons are fixed words. They never contain any part of the input, so an
// error cannot name a machine, an address or a credential.
const (
	ReasonEmpty              = "empty"
	ReasonTooLong            = "too_long"
	ReasonBadCharacter       = "bad_character"
	ReasonMissingScheme      = "missing_scheme"
	ReasonSchemeNotPublic    = "scheme_not_public"
	ReasonMissingHost        = "missing_host"
	ReasonCredentialsPresent = "credentials_present"
	ReasonBadPort            = "bad_port"
	ReasonBadHost            = "bad_host"
	ReasonHostNotPublic      = "host_not_public"
	ReasonNonASCIIHost       = "non_ascii_host"
	ReasonTrailingDotHost    = "trailing_dot_host"
)

// Reasons lists every reason with its code. Tests require a saved example
// for each one.
var Reasons = map[string]string{
	ReasonEmpty:              CodeParse,
	ReasonTooLong:            CodeParse,
	ReasonBadCharacter:       CodeParse,
	ReasonMissingScheme:      CodeParse,
	ReasonSchemeNotPublic:    CodeParse,
	ReasonMissingHost:        CodeParse,
	ReasonCredentialsPresent: CodeParse,
	ReasonBadPort:            CodeParse,
	ReasonBadHost:            CodeParse,
	ReasonHostNotPublic:      CodeParse,
	ReasonNonASCIIHost:       CodeNeedsSubtool,
	ReasonTrailingDotHost:    CodeNeedsSubtool,
}

// Rules names every normalization rule. Each has a fixture file of the same
// name under testdata/rules; tests fail if one is missing.
var Rules = []string{
	"well-formed-input",
	"lowercase-scheme-host",
	"strip-tracking-params",
	"strip-fragment",
	"trailing-slash",
	"default-index",
	"public-scheme",
	"public-host",
	"leave-unlisted-alone",
}

// Error is the only error type Normalize returns.
type Error struct {
	Code   string
	Reason string
}

func (e *Error) Error() string { return e.Code + " " + e.Reason }

func fail(reason string) (string, error) {
	return "", &Error{Code: Reasons[reason], Reason: reason}
}

// Query parameters removed whatever their value. Names are compared after
// percent-decoding and lowercasing. Anything starting with "utm_" goes too.
var strippedParams = map[string]bool{
	"ref":        true,
	"fbclid":     true,
	"gclid":      true,
	"phpsessid":  true,
	"jsessionid": true,
	"sessionid":  true,
	"session_id": true,
	"sid":        true,
}

// Final path segments treated as a default index page. Exact, lowercase.
var defaultIndex = map[string]bool{
	"index.html":   true,
	"index.htm":    true,
	"index.php":    true,
	"index.asp":    true,
	"index.aspx":   true,
	"index.jsp":    true,
	"default.html": true,
	"default.htm":  true,
	"default.asp":  true,
	"default.aspx": true,
}

// Top-level names that are never on the public internet.
var nonPublicSuffix = map[string]bool{
	"alt":       true,
	"arpa":      true,
	"corp":      true,
	"example":   true,
	"home":      true,
	"internal":  true,
	"invalid":   true,
	"lan":       true,
	"local":     true,
	"localhost": true,
	"onion":     true,
	"svc":       true,
	"test":      true,
}

// Hash returns the lowercase hex SHA-256 of a canonical address.
func Hash(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// Normalize returns the canonical form of raw, or an *Error.
// Normalize(Normalize(x)) == Normalize(x) for every accepted x.
func Normalize(raw string) (string, error) {
	s := strings.Trim(raw, " \t\r\n")
	if s == "" {
		return fail(ReasonEmpty)
	}
	if len(s) > MaxAddressBytes {
		return fail(ReasonTooLong)
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= 0x20 || s[i] == 0x7f {
			return fail(ReasonBadCharacter)
		}
	}

	// Scheme.
	colon := strings.IndexByte(s, ':')
	if colon <= 0 || !isScheme(s[:colon]) {
		return fail(ReasonMissingScheme)
	}
	scheme := lowerASCII(s[:colon])
	if scheme != "http" && scheme != "https" {
		return fail(ReasonSchemeNotPublic)
	}
	rest := s[colon+1:]
	if !strings.HasPrefix(rest, "//") {
		return fail(ReasonMissingHost)
	}
	rest = rest[2:]

	// Authority, path, query. The fragment is dropped.
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest = rest[:i]
	}
	query := ""
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest, query = rest[:i], rest[i+1:]
	}
	authority, path := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		authority, path = rest[:i], rest[i:]
	}

	host, port, reason := splitAuthority(authority)
	if reason != "" {
		return fail(reason)
	}

	var b strings.Builder
	b.Grow(len(s) + 1)
	b.WriteString(scheme)
	b.WriteString("://")
	b.WriteString(host)
	if port != "" {
		b.WriteByte(':')
		b.WriteString(port)
	}
	b.WriteString(normalizePath(path))
	if q := normalizeQuery(query); q != "" {
		b.WriteByte('?')
		b.WriteString(q)
	}
	return b.String(), nil
}

func isScheme(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

func lowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// splitAuthority validates the host and returns it lowercased, with the port
// exactly as written. A non-empty third value is the reason for refusal.
func splitAuthority(authority string) (host, port, reason string) {
	if authority == "" {
		return "", "", ReasonMissingHost
	}
	if strings.IndexByte(authority, '@') >= 0 {
		return "", "", ReasonCredentialsPresent
	}
	if authority[0] == '[' {
		// IP literals are refused whether or not the address is routable.
		return "", "", ReasonHostNotPublic
	}
	host = authority
	if i := strings.LastIndexByte(authority, ':'); i >= 0 {
		host, port = authority[:i], authority[i+1:]
		if !validPort(port) {
			return "", "", ReasonBadPort
		}
	}
	if host == "" {
		return "", "", ReasonMissingHost
	}
	for i := 0; i < len(host); i++ {
		if host[i] >= 0x80 {
			return "", "", ReasonNonASCIIHost
		}
	}
	host = lowerASCII(host)
	if len(host) > 1 && host[len(host)-1] == '.' && host[len(host)-2] != '.' {
		if validLabels(host[:len(host)-1]) {
			return "", "", ReasonTrailingDotHost
		}
		return "", "", ReasonBadHost
	}
	if len(host) > 253 || !validLabels(host) {
		return "", "", ReasonBadHost
	}
	last := host
	dot := strings.LastIndexByte(host, '.')
	if dot >= 0 {
		last = host[dot+1:]
	}
	// One label, a numeric last label (every IPv4 spelling), or a name that
	// is reserved for private use: not public.
	if dot < 0 || last[0] < 'a' || last[0] > 'z' || nonPublicSuffix[last] {
		return "", "", ReasonHostNotPublic
	}
	return host, port, ""
}

func validPort(p string) bool {
	if len(p) == 0 || len(p) > 5 {
		return false
	}
	n := 0
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return false
		}
		n = n*10 + int(p[i]-'0')
	}
	return n >= 1 && n <= 65535
}

// validLabels reports whether a lowercased host is dot-separated labels of
// 1 to 63 letters, digits and hyphens, with no hyphen at either end.
func validLabels(host string) bool {
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// normalizePath removes trailing slashes and default index filenames until
// neither is left. The root path is always written as "/".
func normalizePath(p string) string {
	for {
		p = strings.TrimRight(p, "/")
		i := strings.LastIndexByte(p, '/')
		if i < 0 || !defaultIndex[p[i+1:]] {
			break
		}
		p = p[:i]
	}
	if p == "" {
		return "/"
	}
	return p
}

// normalizeQuery drops tracking and session parameters and empty pieces.
// What remains keeps its order and its exact bytes.
func normalizeQuery(q string) string {
	if q == "" {
		return ""
	}
	kept := make([]string, 0, 4)
	for _, piece := range strings.Split(q, "&") {
		if piece == "" {
			continue
		}
		name := piece
		if i := strings.IndexByte(piece, '='); i >= 0 {
			name = piece[:i]
		}
		name = lowerASCII(percentDecode(name))
		if strippedParams[name] || strings.HasPrefix(name, "utm_") {
			continue
		}
		kept = append(kept, piece)
	}
	return strings.Join(kept, "&")
}

// percentDecode decodes %XX escapes. Broken escapes are left as written.
func percentDecode(s string) string {
	if strings.IndexByte(s, '%') < 0 {
		return s
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b = append(b, unhex(s[i+1])<<4|unhex(s[i+2]))
			i += 2
			continue
		}
		b = append(b, s[i])
	}
	return string(b)
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}
