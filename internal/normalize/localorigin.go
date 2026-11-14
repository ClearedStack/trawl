package normalize

import "strings"

// Config holds the settings that change what Normalize accepts. The zero
// value is the default and gives the same output as the package function.
type Config struct {
	// LocalTestOrigin is the one origin, written http://127.0.0.1:<port>
	// or http://[::1]:<port>, whose addresses pass the public-host rule.
	// It is for tests only; a value ParseLocalTestOrigin refuses is
	// ignored, so a bad value can never widen what is accepted.
	LocalTestOrigin string
}

// ParseLocalTestOrigin accepts exactly http://127.0.0.1:<port> or
// http://[::1]:<port>, with a port from 1 to 65535 written without
// leading zeros, and nothing before, after or between. It returns the
// value unchanged.
func ParseLocalTestOrigin(v string) (string, bool) {
	var port string
	switch {
	case strings.HasPrefix(v, "http://127.0.0.1:"):
		port = v[len("http://127.0.0.1:"):]
	case strings.HasPrefix(v, "http://[::1]:"):
		port = v[len("http://[::1]:"):]
	default:
		return "", false
	}
	if len(port) == 0 || len(port) > 5 || port[0] == '0' {
		return "", false
	}
	n := 0
	for i := 0; i < len(port); i++ {
		if port[i] < '0' || port[i] > '9' {
			return "", false
		}
		n = n*10 + int(port[i]-'0')
	}
	if n > 65535 {
		return "", false
	}
	return v, true
}

// Normalize is the package function Normalize with the settings in c.
func (c Config) Normalize(raw string) (Result, error) {
	local, _ := ParseLocalTestOrigin(c.LocalTestOrigin)
	return normalize(raw, local)
}
