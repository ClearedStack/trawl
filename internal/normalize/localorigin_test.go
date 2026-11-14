package normalize

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseLocalTestOrigin(t *testing.T) {
	good := []string{
		"http://127.0.0.1:41234",
		"http://127.0.0.1:1",
		"http://127.0.0.1:80",
		"http://127.0.0.1:65535",
		"http://[::1]:41234",
		"http://[::1]:65535",
	}
	for _, v := range good {
		got, ok := ParseLocalTestOrigin(v)
		if !ok || got != v {
			t.Errorf("ParseLocalTestOrigin(%q) = %q, %v; want it accepted unchanged", v, got, ok)
		}
	}
	bad := []string{
		"",
		"127.0.0.1:41234",
		"https://127.0.0.1:41234",
		"HTTP://127.0.0.1:41234",
		"http://127.0.0.1",
		"http://127.0.0.1:",
		"http://127.0.0.1:0",
		"http://127.0.0.1:65536",
		"http://127.0.0.1:099",
		"http://127.0.0.1:041234",
		"http://127.0.0.1:123456",
		"http://127.0.0.1:+80",
		"http://127.0.0.1:8a",
		"http://127.0.0.1:41234/",
		"http://127.0.0.1:41234/calculator",
		"http://127.0.0.1:41234?x=1",
		"http://127.0.0.1:41234#top",
		"http://user@127.0.0.1:41234",
		"http://user:pw@127.0.0.1:41234",
		"http://localhost:41234",
		"http://127.0.0.2:41234",
		"http://127.1:41234",
		"http://0.0.0.0:41234",
		"http://[::1]",
		"http://[::1]:0",
		"http://[::1]:41234/",
		"http://[::2]:41234",
		"http://[0:0:0:0:0:0:0:1]:41234",
		"http://::1:41234",
		"http://10.0.0.1:41234",
		"http://tools.agency-example.gov:41234",
		" http://127.0.0.1:41234",
		"http://127.0.0.1:41234 ",
		"http://127.0.0.1:41234\n",
	}
	for _, v := range bad {
		if got, ok := ParseLocalTestOrigin(v); ok {
			t.Errorf("ParseLocalTestOrigin(%q) = %q, true; want refused", v, got)
		}
	}
}

func TestNormalizeLocalOrigin(t *testing.T) {
	v4 := Config{LocalTestOrigin: "http://127.0.0.1:41234"}
	v6 := Config{LocalTestOrigin: "http://[::1]:41234"}
	cases := []struct {
		name string
		cfg  Config
		in   string
		want string // canonical address, or "" when refused
		err  string // expected reason when refused
	}{
		{"v4 origin passes", v4, "http://127.0.0.1:41234/calculator", "http://127.0.0.1:41234/calculator", ""},
		{"v4 root path", v4, "http://127.0.0.1:41234", "http://127.0.0.1:41234/", ""},
		{"v4 path rules apply", v4, "http://127.0.0.1:41234/a/index.html?utm_source=x&b=2&a=1#top", "http://127.0.0.1:41234/a?a=1&b=2", ""},
		{"v4 upper-case scheme", v4, "HTTP://127.0.0.1:41234/x", "http://127.0.0.1:41234/x", ""},
		{"v6 origin passes", v6, "http://[::1]:41234/calculator", "http://[::1]:41234/calculator", ""},
		{"v6 root path", v6, "http://[::1]:41234", "http://[::1]:41234/", ""},
		{"v4 other port", v4, "http://127.0.0.1:41235/calculator", "", "host_not_public"},
		{"v4 no port", v4, "http://127.0.0.1/calculator", "", "host_not_public"},
		{"v4 leading zero port", v4, "http://127.0.0.1:041234/calculator", "", "bad_port"},
		{"v4 https", v4, "https://127.0.0.1:41234/calculator", "", "host_not_public"},
		{"v4 localhost same port", v4, "http://localhost:41234/calculator", "", "host_not_public"},
		{"v4 other loopback address", v4, "http://127.0.0.2:41234/calculator", "", "host_not_public"},
		{"v4 v6 loopback", v4, "http://[::1]:41234/calculator", "", "host_not_public"},
		{"v6 v4 loopback", v6, "http://127.0.0.1:41234/calculator", "", "host_not_public"},
		{"v6 other port", v6, "http://[::1]:41235/calculator", "", "host_not_public"},
		{"v6 long form", v6, "http://[0:0:0:0:0:0:0:1]:41234/calculator", "", "host_not_public"},
		{"v4 userinfo", v4, "http://user@127.0.0.1:41234/calculator", "", "userinfo"},
		{"v4 trailing dot", v4, "http://127.0.0.1.:41234/calculator", "", "host_not_public"},
		{"v4 private address", v4, "http://10.0.0.5:41234/calculator", "", "host_not_public"},
		{"v4 public host", v4, "https://tools.agency-example.gov/wage", "https://tools.agency-example.gov/wage", ""},
		{"unset v4", Config{}, "http://127.0.0.1:41234/calculator", "", "host_not_public"},
		{"unset v6", Config{}, "http://[::1]:41234/calculator", "", "host_not_public"},
		{"invalid setting is ignored", Config{LocalTestOrigin: "http://127.0.0.1:41234/"}, "http://127.0.0.1:41234/calculator", "", "host_not_public"},
		{"invalid setting localhost", Config{LocalTestOrigin: "http://localhost:41234"}, "http://localhost:41234/calculator", "", "host_not_public"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := c.cfg.Normalize(c.in)
			if c.want == "" {
				if err == nil || !strings.Contains(err.Error(), "reason="+c.err) {
					t.Fatalf("Normalize(%q) = %+v, %v; want refusal reason=%s", c.in, res, err, c.err)
				}
				return
			}
			if err != nil || res.URL != c.want {
				t.Fatalf("Normalize(%q) = %+v, %v; want %s", c.in, res, err, c.want)
			}
			again, err := c.cfg.Normalize(res.URL)
			if err != nil || again.URL != res.URL {
				t.Errorf("not idempotent: %q gives %+v, %v", res.URL, again, err)
			}
		})
	}
}

// With the setting unset, the package function and a zero Config agree on
// every address in the saved examples, so output cannot change.
func TestZeroConfigMatchesNormalize(t *testing.T) {
	for _, info := range Rules {
		_, cases := load(t, info.Rule)
		for _, c := range cases {
			a, aerr := Normalize(c.Input)
			b, berr := Config{}.Normalize(c.Input)
			if !reflect.DeepEqual(a, b) || (aerr == nil) != (berr == nil) || (aerr != nil && aerr.Error() != berr.Error()) {
				t.Errorf("%q: Normalize and Config{}.Normalize differ", c.Input)
			}
		}
	}
}

func FuzzLocalTestOrigin(f *testing.F) {
	for _, s := range []string{
		"http://127.0.0.1:41234/calculator", "http://[::1]:41234/x", "http://127.0.0.1:80/", "http://127.0.0.1:41235/",
		"http://localhost:41234/", "http://127.0.0.1.:41234/", "HTTP://127.0.0.1:41234", "http://0x7f.1:41234/",
		"https://tools.agency-example.gov/wage", "http://[::ffff:127.0.0.1]:41234/", "http://127.0.0.1:041234/",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		// Unset: identical to the package function, and never a bare IP,
		// bracketed or single-label host in an accepted address.
		res, err := Config{}.Normalize(in)
		base, baseErr := Normalize(in)
		if (err == nil) != (baseErr == nil) || res.URL != base.URL {
			t.Fatalf("unset config differs from Normalize for %q", in)
		}
		if err == nil && !publicAuthority(res.URL) {
			t.Fatalf("unset config accepted a non-public address %q from %q", res.URL, in)
		}
		// Set: a non-public address is accepted only on the exact origin.
		for _, origin := range []string{"http://127.0.0.1:41234", "http://[::1]:41234"} {
			res, err := Config{LocalTestOrigin: origin}.Normalize(in)
			if err != nil || publicAuthority(res.URL) {
				continue
			}
			if !strings.HasPrefix(res.URL, origin+"/") && !strings.HasPrefix(res.URL, origin+"?") {
				t.Fatalf("origin %s accepted %q from %q", origin, res.URL, in)
			}
		}
	})
}

// publicAuthority reports whether the address's host is a dotted name whose
// last label is neither numeric nor reserved, as the public-host rule needs.
func publicAuthority(u string) bool {
	rest := u[strings.Index(u, "://")+3:]
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		rest = rest[:i]
	}
	labels := strings.Split(rest, ".")
	last := labels[len(labels)-1]
	return !strings.ContainsAny(u[:strings.Index(u, "://")+3+len(rest)], "[]") && len(labels) >= 2 && !allDigits(last) && !nonPublicSuffixes[last]
}
