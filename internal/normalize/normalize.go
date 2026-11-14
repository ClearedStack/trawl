// Package normalize turns an address as it was found into the one canonical
// form Trawl records. The rules are fixed, ordered and free of any outside
// lookup, so the same input gives the same output on any machine.
//
// The package parses addresses itself instead of leaning on a general URL
// library, so that a change in a library can never change a recorded hash.
package normalize

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ClearedStack/trawl/internal/errs"
)

// Rule names one normalization or validation rule. Every rule has a fixture
// file named after it under testdata.
type Rule string

const (
	Whitespace      Rule = "surrounding-whitespace"
	Syntax          Rule = "syntax"
	SchemeLowercase Rule = "scheme-lowercase"
	PublicScheme    Rule = "public-scheme"
	HostLowercase   Rule = "host-lowercase"
	HostTrailingDot Rule = "host-trailing-dot"
	PublicHost      Rule = "public-host"
	DefaultPort     Rule = "default-port"
	Fragment        Rule = "fragment"
	PercentEncoding Rule = "percent-encoding"
	SessionParams   Rule = "session-params"
	TrackingParams  Rule = "tracking-params"
	QueryOrder      Rule = "query-order"
	DefaultIndex    Rule = "default-index"
	TrailingSlash   Rule = "trailing-slash"
	RootPath        Rule = "root-path"
)

// Info describes one rule.
type Info struct {
	Rule Rule
	// Rejects is true for a rule that refuses input instead of rewriting it.
	Rejects bool
	Summary string
}

// Rules lists every rule in the order it is reported.
var Rules = []Info{
	{Whitespace, false, "Drops spaces, tabs and line ends around the address."},
	{Syntax, true, "Refuses an address that is empty, too long, has control characters, a backslash, no scheme, no host, a bad port or a bad percent escape."},
	{SchemeLowercase, false, "Lowercases the scheme."},
	{PublicScheme, true, "Accepts only http and https."},
	{HostLowercase, false, "Lowercases the host name."},
	{HostTrailingDot, false, "Drops one trailing dot from the host name."},
	{PublicHost, true, "Refuses user names, IP addresses, single-label names and reserved or private suffixes. A host name outside ASCII is paused as needing a sub-tool."},
	{DefaultPort, false, "Drops port 80 for http and 443 for https, an empty port, and leading zeros in a port."},
	{Fragment, false, "Drops the # fragment."},
	{PercentEncoding, false, "Writes percent escapes in upper case and escapes bytes outside printable ASCII and the characters \" < > ^ ` { | }."},
	{SessionParams, false, "Drops session identifiers from the query and ;jsessionid from the path."},
	{TrackingParams, false, "Drops utm_*, ref, fbclid and gclid from the query."},
	{QueryOrder, false, "Drops empty query pairs and sorts the rest by key, keeping the given order among equal keys."},
	{DefaultIndex, false, "Drops a default index filename at the end of the path."},
	{TrailingSlash, false, "Drops slashes at the end of the path."},
	{RootPath, false, "Writes an empty path as /."},
}

// MaxLength is the longest address accepted, in bytes, matching Muster
// and Logbook.
const MaxLength = 2000

// Result is a canonical address and the rules that changed the input to
// reach it, in the order of Rules.
type Result struct {
	URL     string
	Applied []Rule
}

var trackingKeys = map[string]bool{"ref": true, "fbclid": true, "gclid": true}

var sessionKeys = map[string]bool{
	"jsessionid": true,
	"phpsessid":  true,
	"sessionid":  true,
	"session_id": true,
	"sessid":     true,
}

var indexNames = map[string]bool{
	"index.html":   true,
	"index.htm":    true,
	"index.shtml":  true,
	"index.php":    true,
	"index.asp":    true,
	"index.aspx":   true,
	"index.jsp":    true,
	"default.html": true,
	"default.htm":  true,
	"default.asp":  true,
	"default.aspx": true,
}

// nonPublicSuffixes are final labels that never name a public host.
var nonPublicSuffixes = map[string]bool{
	"local":       true,
	"localhost":   true,
	"localdomain": true,
	"internal":    true,
	"intranet":    true,
	"private":     true,
	"lan":         true,
	"home":        true,
	"corp":        true,
	"svc":         true,
	"test":        true,
	"example":     true,
	"invalid":     true,
	"onion":       true,
	"arpa":        true,
}

type applied map[Rule]bool

func (a applied) list() []Rule {
	out := []Rule{}
	for _, info := range Rules {
		if a[info.Rule] {
			out = append(out, info.Rule)
		}
	}
	return out
}

// Normalize returns the canonical form of raw. On failure the error is a
// public *errs.Error: E_PARSE for a broken or non-public address, and
// E_NEEDS_SUBTOOL for a well-formed address no rule covers.
func Normalize(raw string) (Result, error) { return normalize(raw, "") }

// normalize is Normalize with one optional local test origin, already
// checked by ParseLocalTestOrigin, whose authority skips the public-host
// rule. Everything else about the address is cleaned as usual.
func normalize(raw, local string) (Result, error) {
	fail := func(code errs.Code, reason string) (Result, error) {
		return Result{}, errs.New(code, reason, []byte(raw))
	}
	done := applied{}

	s := strings.Trim(raw, " \t\r\n")
	if s != raw {
		done[Whitespace] = true
	}
	if s == "" {
		return fail(errs.Parse, "empty")
	}
	if len(s) > MaxLength {
		return fail(errs.Parse, "too_long")
	}
	if !utf8.ValidString(s) {
		return fail(errs.Parse, "bad_utf8")
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c < 0x21 || c == 0x7f:
			return fail(errs.Parse, "control_char")
		case c == '\\':
			return fail(errs.Parse, "backslash")
		}
	}

	cut := strings.Index(s, "://")
	if cut <= 0 || !validScheme(s[:cut]) {
		return fail(errs.Parse, "no_scheme")
	}
	scheme := strings.ToLower(s[:cut])
	if scheme != s[:cut] {
		done[SchemeLowercase] = true
	}
	if scheme != "http" && scheme != "https" {
		return fail(errs.Parse, "scheme_not_public")
	}

	rest := s[cut+3:]
	authority, tail := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority, tail = rest[:i], rest[i:]
	}

	var host, port string
	if local != "" && scheme == "http" && authority == local[len("http://"):] {
		// The one test-only exception: keep host and port exactly as given.
		i := strings.LastIndexByte(authority, ':')
		host, port = authority[:i], authority[i+1:]
	} else {
		var code errs.Code
		var reason string
		host, port, code, reason = splitAuthority(authority, scheme, done)
		if reason != "" {
			return fail(code, reason)
		}
	}

	if i := strings.IndexByte(tail, '#'); i >= 0 {
		tail = tail[:i]
		done[Fragment] = true
	}
	path, query, hasQuery := tail, "", false
	if i := strings.IndexByte(tail, '?'); i >= 0 {
		path, query, hasQuery = tail[:i], tail[i+1:], true
	}

	path, ok := escape(path, done)
	if !ok {
		return fail(errs.Parse, "bad_escape")
	}
	path = cleanPath(path, done)

	if hasQuery {
		query, ok = escape(query, done)
		if !ok {
			return fail(errs.Parse, "bad_escape")
		}
		query = cleanQuery(query, done)
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
	b.WriteString(path)
	if query != "" {
		b.WriteByte('?')
		b.WriteString(query)
	}
	out := b.String()
	if len(out) > MaxLength {
		return fail(errs.Parse, "too_long")
	}
	return Result{URL: out, Applied: done.list()}, nil
}

func validScheme(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		alpha := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if i == 0 && !alpha {
			return false
		}
		if !alpha && !(c >= '0' && c <= '9') && c != '+' && c != '-' && c != '.' {
			return false
		}
	}
	return s != ""
}

// splitAuthority validates the host and port. A non-empty reason reports a
// refusal under the given code.
func splitAuthority(authority, scheme string, done applied) (host, port string, code errs.Code, reason string) {
	if authority == "" {
		return "", "", errs.Parse, "no_host"
	}
	if strings.IndexByte(authority, '@') >= 0 {
		return "", "", errs.Parse, "userinfo"
	}
	if strings.ContainsAny(authority, "[]") {
		return "", "", errs.Parse, "host_not_public"
	}
	host = authority
	if i := strings.IndexByte(authority, ':'); i >= 0 {
		host = authority[:i]
		rawPort := authority[i+1:]
		if rawPort == "" {
			done[DefaultPort] = true
		} else {
			if len(rawPort) > 5 {
				return "", "", errs.Parse, "bad_port"
			}
			n := 0
			for j := 0; j < len(rawPort); j++ {
				if rawPort[j] < '0' || rawPort[j] > '9' {
					return "", "", errs.Parse, "bad_port"
				}
				n = n*10 + int(rawPort[j]-'0')
			}
			if n < 1 || n > 65535 {
				return "", "", errs.Parse, "bad_port"
			}
			port = strconv.Itoa(n)
			if port != rawPort {
				done[DefaultPort] = true
			}
			if scheme == "http" && n == 80 || scheme == "https" && n == 443 {
				port = ""
				done[DefaultPort] = true
			}
		}
	}

	for i := 0; i < len(host); i++ {
		if host[i] >= 0x80 {
			return "", "", errs.NeedsSubtool, "idn_host"
		}
	}
	lower := strings.ToLower(host)
	if lower != host {
		done[HostLowercase] = true
	}
	host = lower
	if strings.HasSuffix(host, ".") {
		host = host[:len(host)-1]
		done[HostTrailingDot] = true
	}
	if host == "" {
		return "", "", errs.Parse, "no_host"
	}
	if len(host) > 253 {
		return "", "", errs.Parse, "bad_host"
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if !validLabel(label) {
			return "", "", errs.Parse, "bad_host"
		}
	}
	last := labels[len(labels)-1]
	if len(labels) < 2 || allDigits(last) || nonPublicSuffixes[last] {
		return "", "", errs.Parse, "host_not_public"
	}
	return host, port, "", ""
}

func validLabel(label string) bool {
	if len(label) < 1 || len(label) > 63 {
		return false
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

const upperHex = "0123456789ABCDEF"

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func upper(c byte) byte {
	if c >= 'a' && c <= 'f' {
		return c - 'a' + 'A'
	}
	return c
}

// escape rewrites percent escapes in upper case and escapes bytes that must
// not appear bare. It reports false on a malformed escape.
func escape(s string, done applied) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	changed := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%':
			if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
				return "", false
			}
			hi, lo := upper(s[i+1]), upper(s[i+2])
			if hi != s[i+1] || lo != s[i+2] {
				changed = true
			}
			b.WriteByte('%')
			b.WriteByte(hi)
			b.WriteByte(lo)
			i += 2
		case c >= 0x80 || strings.IndexByte("\"<>^`{|}", c) >= 0:
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0x0f])
			changed = true
		default:
			b.WriteByte(c)
		}
	}
	if changed {
		done[PercentEncoding] = true
	}
	return b.String(), true
}

// unescapeLower decodes percent escapes and lowercases the result. It is
// used only to compare query keys against the lists of keys to drop.
func unescapeLower(s string) string {
	if strings.IndexByte(s, '%') < 0 {
		return strings.ToLower(s)
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			out = append(out, unhex(s[i+1])<<4|unhex(s[i+2]))
			i += 2
			continue
		}
		out = append(out, s[i])
	}
	return strings.ToLower(string(out))
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

func cleanPath(path string, done applied) string {
	if path == "" {
		done[RootPath] = true
		return "/"
	}

	// Drop ;jsessionid=... path parameters.
	if strings.IndexByte(path, ';') >= 0 {
		segments := strings.Split(path, "/")
		for i, segment := range segments {
			parts := strings.Split(segment, ";")
			kept := parts[:1]
			for _, param := range parts[1:] {
				name := param
				if j := strings.IndexByte(param, '='); j >= 0 {
					name = param[:j]
				}
				if unescapeLower(name) == "jsessionid" {
					done[SessionParams] = true
					continue
				}
				kept = append(kept, param)
			}
			segments[i] = strings.Join(kept, ";")
		}
		path = strings.Join(segments, "/")
	}

	for {
		before := path
		for len(path) > 1 && strings.HasSuffix(path, "/") {
			path = path[:len(path)-1]
			done[TrailingSlash] = true
		}
		slash := strings.LastIndexByte(path, '/')
		if indexNames[strings.ToLower(path[slash+1:])] {
			path = path[:slash+1]
			done[DefaultIndex] = true
		}
		if path == before {
			break
		}
	}
	return path
}

func cleanQuery(query string, done applied) string {
	type pair struct{ key, raw string }
	var pairs []pair
	for _, raw := range strings.Split(query, "&") {
		if raw == "" {
			done[QueryOrder] = true
			continue
		}
		key := raw
		if i := strings.IndexByte(raw, '='); i >= 0 {
			key = raw[:i]
		}
		name := unescapeLower(key)
		if strings.HasPrefix(name, "utm_") || trackingKeys[name] {
			done[TrackingParams] = true
			continue
		}
		if sessionKeys[name] {
			done[SessionParams] = true
			continue
		}
		pairs = append(pairs, pair{key, raw})
	}
	sorted := sort.SliceIsSorted(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })
	if !sorted {
		sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })
		done[QueryOrder] = true
	}
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.raw
	}
	return strings.Join(parts, "&")
}
