package fetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClearedStack/trawl/internal/errs"
)

var ctx = context.Background()

// web is a made-up internet: handlers by host name, served without a
// network. It records every request made.
type web struct {
	mu       sync.Mutex
	sites    map[string]http.Handler
	requests []*http.Request
}

func (w *web) RoundTrip(req *http.Request) (*http.Response, error) {
	w.mu.Lock()
	w.requests = append(w.requests, req)
	handler, ok := w.sites[req.URL.Host]
	w.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("lookup %s on 10.0.0.53:53: no such host", req.URL.Host)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func (w *web) asked() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, r := range w.requests {
		out = append(out, r.Method+" "+r.URL.String())
	}
	return out
}

func site(robots string, pages map[string]http.HandlerFunc) http.Handler {
	mux := http.NewServeMux()
	if robots != "" {
		mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, robots) })
	}
	for path, h := range pages {
		mux.HandleFunc(path, h)
	}
	return mux
}

func page(contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		fmt.Fprint(w, body)
	}
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func redirectTo(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusFound) }
}

func wantCode(t *testing.T, err error, code errs.Code, reason string) {
	t.Helper()
	e, ok := errs.As(err)
	if !ok || e.Code != code || e.Reason != reason {
		t.Fatalf("error = %v, want %s reason=%s", err, code, reason)
	}
}

func TestFetch(t *testing.T) {
	w := &web{sites: map[string]http.Handler{
		"tools.agency-example.gov": site("User-agent: *\nDisallow: /private/\n", map[string]http.HandlerFunc{
			"/resources": page("text/html; charset=utf-8", "<html>made-up</html>"),
		}),
	}}
	f := NewWithTransport(w, time.Second)
	doc, err := f.Fetch(ctx, "HTTPS://Tools.Agency-Example.gov/resources/?utm_source=x#top")
	if err != nil {
		t.Fatal(err)
	}
	if doc.URL != "https://tools.agency-example.gov/resources" || doc.ContentType != "text/html" || string(doc.Body) != "<html>made-up</html>" {
		t.Errorf("doc = %+v", doc)
	}
	want := []string{
		"GET https://tools.agency-example.gov/robots.txt",
		"GET https://tools.agency-example.gov/resources",
	}
	if got := w.asked(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests = %v", got)
	}
	for _, r := range w.requests {
		if r.Header.Get("User-Agent") != UserAgent {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("request carries a cookie or a credential")
		}
	}
}

func TestRobots(t *testing.T) {
	pages := map[string]http.HandlerFunc{
		"/private/list": page("text/html", "secret"),
		"/open":         page("text/html", "open"),
	}
	w := &web{sites: map[string]http.Handler{
		"rules.agency-example.gov":  site("User-agent: *\nDisallow: /private/\n", pages),
		"named.agency-example.gov":  site("User-agent: trawl\nDisallow: /\n\nUser-agent: *\nAllow: /\n", pages),
		"absent.agency-example.gov": site("", pages),
		"closed.agency-example.gov": site("", map[string]http.HandlerFunc{"/robots.txt": status(http.StatusForbidden), "/open": pages["/open"]}),
		"broken.agency-example.gov": site("", map[string]http.HandlerFunc{"/robots.txt": status(http.StatusServiceUnavailable), "/open": pages["/open"]}),
		"moved.agency-example.gov": site("", map[string]http.HandlerFunc{
			"/robots.txt": redirectTo("https://rules.agency-example.gov/robots.txt"),
			"/private/x":  page("text/html", "x"),
			"/open":       pages["/open"],
		}),
		"astray.agency-example.gov": site("", map[string]http.HandlerFunc{"/robots.txt": redirectTo("http://localhost/robots.txt"), "/open": pages["/open"]}),
	}}
	f := NewWithTransport(w, time.Second)

	cases := []struct {
		address string
		allowed bool
	}{
		{"https://rules.agency-example.gov/open", true},
		{"https://rules.agency-example.gov/private/list", false},
		{"https://named.agency-example.gov/open", false},
		{"https://absent.agency-example.gov/private/list", true},
		{"https://closed.agency-example.gov/open", false},
		{"https://moved.agency-example.gov/open", true},
		{"https://moved.agency-example.gov/private/x", false},
		{"https://astray.agency-example.gov/open", false},
	}
	for _, c := range cases {
		before := len(w.asked())
		_, err := f.Fetch(ctx, c.address)
		if c.allowed && err != nil {
			t.Errorf("%s: %v", c.address, err)
		}
		if !c.allowed {
			if !errors.Is(err, ErrRobotsDisallowed) {
				t.Errorf("%s: err = %v, want robots refusal", c.address, err)
			}
			// The page itself must not have been asked for.
			for _, req := range w.asked()[before:] {
				if !strings.HasSuffix(req, "/robots.txt") {
					t.Errorf("%s: made request %q despite robots.txt", c.address, req)
				}
			}
		}
	}

	// When robots.txt cannot be read, the page is not read either.
	before := len(w.asked())
	_, err := f.Fetch(ctx, "https://broken.agency-example.gov/open")
	wantCode(t, err, errs.Timeout, "upstream")
	if got := w.asked()[before:]; len(got) != 1 {
		t.Errorf("requests after a failing robots.txt: %v", got)
	}
}

func TestRedirects(t *testing.T) {
	w := &web{sites: map[string]http.Handler{
		"old.agency-example.gov": site("", map[string]http.HandlerFunc{
			"/tool":       redirectTo("https://new.agency-example.gov/tool/"),
			"/relative":   redirectTo("moved/here"),
			"/inward":     redirectTo("http://localhost:8080/admin"),
			"/metadata":   redirectTo("http://169.254.169.254/latest/"),
			"/shy":        redirectTo("https://new.agency-example.gov/private/tool"),
			"/loop":       redirectTo("/loop"),
			"/moved/here": page("text/html", "relative"),
		}),
		"new.agency-example.gov": site("User-agent: *\nDisallow: /private/\n", map[string]http.HandlerFunc{
			"/tool": page("text/html", "moved"),
		}),
	}}
	f := NewWithTransport(w, time.Second)

	doc, err := f.Fetch(ctx, "https://old.agency-example.gov/tool")
	if err != nil || doc.URL != "https://new.agency-example.gov/tool" || string(doc.Body) != "moved" {
		t.Errorf("cross-site redirect: %+v, %v", doc, err)
	}
	want := []string{
		"GET https://old.agency-example.gov/robots.txt",
		"GET https://old.agency-example.gov/tool",
		"GET https://new.agency-example.gov/robots.txt",
		"GET https://new.agency-example.gov/tool",
	}
	if got := w.asked(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests = %v", got)
	}

	doc, err = f.Fetch(ctx, "https://old.agency-example.gov/relative")
	if err != nil || doc.URL != "https://old.agency-example.gov/moved/here" {
		t.Errorf("relative redirect: %+v, %v", doc, err)
	}

	_, err = f.Fetch(ctx, "https://old.agency-example.gov/inward")
	wantCode(t, err, errs.Parse, "host_not_public")
	_, err = f.Fetch(ctx, "https://old.agency-example.gov/metadata")
	wantCode(t, err, errs.Parse, "host_not_public")
	for _, req := range w.asked() {
		if strings.Contains(req, "localhost") || strings.Contains(req, "169.254") {
			t.Errorf("followed a redirect inward: %s", req)
		}
	}

	_, err = f.Fetch(ctx, "https://old.agency-example.gov/shy")
	if !errors.Is(err, ErrRobotsDisallowed) {
		t.Errorf("redirect to a disallowed path: %v", err)
	}

	_, err = f.Fetch(ctx, "https://old.agency-example.gov/loop")
	wantCode(t, err, errs.Parse, "too_many_redirects")
}

func TestStatuses(t *testing.T) {
	w := &web{sites: map[string]http.Handler{
		"tools.agency-example.gov": site("", map[string]http.HandlerFunc{
			"/signin":  status(http.StatusUnauthorized),
			"/members": status(http.StatusForbidden),
			"/paid":    status(http.StatusPaymentRequired),
			"/missing": status(http.StatusNotFound),
			"/gone":    status(http.StatusGone),
			"/busy":    status(http.StatusTooManyRequests),
			"/down":    status(http.StatusBadGateway),
			"/odd":     status(http.StatusTeapot),
			"/empty":   status(http.StatusNoContent),
			"/big":     page("text/html", strings.Repeat("a", MaxBody+1)),
			"/fits":    page("text/html", strings.Repeat("a", MaxBody)),
		}),
	}}
	f := NewWithTransport(w, time.Second)
	cases := []struct {
		path   string
		code   errs.Code
		reason string
	}{
		{"/signin", errs.Parse, "forbidden"},
		{"/members", errs.Parse, "forbidden"},
		{"/paid", errs.Parse, "forbidden"},
		{"/missing", errs.Parse, "not_found"},
		{"/gone", errs.Parse, "not_found"},
		{"/busy", errs.Timeout, "upstream"},
		{"/down", errs.Timeout, "upstream"},
		{"/odd", errs.Parse, "http_status"},
		{"/empty", errs.Parse, "http_status"},
		{"/big", errs.Parse, "too_large"},
	}
	for _, c := range cases {
		_, err := f.Fetch(ctx, "https://tools.agency-example.gov"+c.path)
		e, ok := errs.As(err)
		if !ok || e.Code != c.code || e.Reason != c.reason {
			t.Errorf("%s: %v, want %s reason=%s", c.path, err, c.code, c.reason)
		}
	}
	if doc, err := f.Fetch(ctx, "https://tools.agency-example.gov/fits"); err != nil || len(doc.Body) != MaxBody {
		t.Errorf("a document of exactly the limit: %d bytes, %v", len(doc.Body), err)
	}
}

func TestNonPublicAddressesAreNeverRequested(t *testing.T) {
	w := &web{sites: map[string]http.Handler{}}
	f := NewWithTransport(w, time.Second)
	for _, address := range []string{
		"http://localhost/admin",
		"http://127.0.0.1/",
		"http://10.0.0.1/",
		"http://muster.cs-core.svc.cluster.local/v1/lease",
		"https://clerk:made-up@tools.agency-example.gov/",
		"ftp://files.agency-example.gov/a.zip",
		"file:///etc/hosts",
		"",
	} {
		_, err := f.Fetch(ctx, address)
		if e, ok := errs.As(err); !ok || e.Code != errs.Parse {
			t.Errorf("%q: %v", address, err)
		}
	}
	if got := w.asked(); len(got) != 0 {
		t.Errorf("requests were made: %v", got)
	}
}

// slow never answers; it waits until the request is given up.
type slow struct{}

func (slow) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestTimeout(t *testing.T) {
	f := NewWithTransport(slow{}, 20*time.Millisecond)
	start := time.Now()
	_, err := f.Fetch(ctx, "https://slow.agency-example.gov/feed")
	wantCode(t, err, errs.Timeout, "upstream")
	if time.Since(start) > 2*time.Second {
		t.Error("the timeout was not applied")
	}
}

// TestErrorsNameNothing checks that failures say nothing about the host,
// the network address or the resolver.
func TestErrorsNameNothing(t *testing.T) {
	w := &web{sites: map[string]http.Handler{}}
	f := NewWithTransport(w, time.Second)
	_, err := f.Fetch(ctx, "https://unknown.agency-example.gov/feed")
	wantCode(t, err, errs.Timeout, "upstream")
	for _, leak := range []string{"agency-example", "10.0.0.53", "no such host", "lookup"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q repeats %q", err, leak)
		}
	}
}

func TestPublicIP(t *testing.T) {
	private := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1",
		"0.0.0.0", "192.0.2.10", "198.51.100.7", "203.0.113.9", "198.18.0.1", "224.0.0.1", "255.255.255.255",
		"::1", "::", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1", "2001:db8::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "64:ff9b::a00:1",
	}
	for _, s := range private {
		if PublicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should not be public", s)
		}
	}
	public := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700::1111", "::ffff:8.8.8.8"}
	for _, s := range public {
		if !PublicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be public", s)
		}
	}
	if PublicIP(netip.Addr{}) {
		t.Error("the zero address should not be public")
	}
}

func TestGuardDial(t *testing.T) {
	for _, address := range []string{"127.0.0.1:80", "10.0.0.5:443", "[::1]:443", "169.254.169.254:80", "not-an-address"} {
		if err := guardDial("tcp", address, nil); !errors.Is(err, errNotPublic) {
			t.Errorf("guardDial(%s) = %v", address, err)
		}
	}
	if err := guardDial("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("guardDial(public) = %v", err)
	}
}

// TestRealTransportRefusesLoopback drives the real transport at a local
// test server. The dial guard must stop it before a connection is made.
func TestRealTransportRefusesLoopback(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	f := New(time.Second)
	_, err := f.get(ctx, srv.URL+"/", nil)
	wantCode(t, err, errs.Parse, "host_not_public")
	if hit {
		t.Error("a connection to a loopback address was made")
	}
}
