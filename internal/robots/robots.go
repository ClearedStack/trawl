// Package robots reads a robots.txt file and answers whether a path may be
// fetched, following the Robots Exclusion Protocol (RFC 9309): groups of
// user-agent lines followed by allow and disallow rules, with "*" matching
// any run of characters and "$" anchoring the end, the longest matching
// rule winning and allow winning a tie.
package robots

import (
	"strings"
)

// MaxBytes is how much of a robots.txt file is read. The protocol asks for
// at least 500 kibibytes.
const MaxBytes = 512 << 10

type rule struct {
	allow   bool
	pattern string
}

type group struct {
	agents []string
	rules  []rule
}

// Rules is a parsed robots.txt file.
type Rules struct {
	groups []group
}

// Parse reads a robots.txt file. Lines it does not understand are skipped,
// as the protocol requires.
func Parse(data []byte) *Rules {
	if len(data) > MaxBytes {
		data = data[:MaxBytes]
	}
	r := &Rules{}
	var current *group
	inAgents := false

	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:colon]))
		value := strings.TrimSpace(line[colon+1:])

		switch key {
		case "user-agent":
			if !inAgents {
				r.groups = append(r.groups, group{})
				current = &r.groups[len(r.groups)-1]
				inAgents = true
			}
			current.agents = append(current.agents, strings.ToLower(value))
		case "allow", "disallow":
			inAgents = false
			if current == nil || value == "" {
				// A rule before any user-agent line belongs to no
				// one; an empty pattern matches nothing.
				continue
			}
			current.rules = append(current.rules, rule{allow: key == "allow", pattern: value})
		}
	}
	return r
}

// Allowed reports whether the crawler named agent may fetch path, which is
// the path of an address together with its query if it has one.
func (r *Rules) Allowed(agent, path string) bool {
	if path == "" {
		path = "/"
	}
	if path == "/robots.txt" {
		return true
	}
	agent = strings.ToLower(agent)

	rules, named := []rule{}, false
	for _, g := range r.groups {
		for _, a := range g.agents {
			if a == agent {
				rules = append(rules, g.rules...)
				named = true
				break
			}
		}
	}
	if !named {
		for _, g := range r.groups {
			for _, a := range g.agents {
				if a == "*" {
					rules = append(rules, g.rules...)
					break
				}
			}
		}
	}

	best, allowed := -1, true
	for _, rl := range rules {
		if !match(rl.pattern, path) {
			continue
		}
		n := len(rl.pattern)
		if n > best || n == best && rl.allow {
			best, allowed = n, rl.allow
		}
	}
	return allowed
}

// match reports whether pattern matches a prefix of path, or all of path
// when the pattern ends in "$".
func match(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = pattern[:len(pattern)-1]
	}
	parts := strings.Split(pattern, "*")

	// The first part must sit at the start of the path.
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	pos := len(parts[0])
	if len(parts) == 1 {
		return !anchored || pos == len(path)
	}
	// Each middle part is taken at its first occurrence, which leaves
	// the most room for the parts that follow.
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(path[pos:], part)
		if i < 0 {
			return false
		}
		pos += i + len(part)
	}
	last := parts[len(parts)-1]
	if !anchored {
		return strings.Contains(path[pos:], last)
	}
	return len(path)-pos >= len(last) && strings.HasSuffix(path, last)
}
