// Package fetch reads documents from public addresses, and only reads.
//
// It sends GET requests and nothing else. It carries no cookies and no
// credentials, so it cannot sign in, create an account or get past a
// paywall. It asks robots.txt before every address, including each step of
// a redirect, and refuses any host that is not public, both by name and by
// the network address the name resolves to.
package fetch

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"github.com/ClearedStack/trawl/internal/errs"
	"github.com/ClearedStack/trawl/internal/normalize"
	"github.com/ClearedStack/trawl/internal/robots"
)

const (
	// Agent is the product token matched against robots.txt.
	Agent = "trawl"
	// UserAgent is what Trawl calls itself to the sites it reads.
	UserAgent = "trawl/0.1 (+https://clearedstack.com/platform/trawl/)"
	// MaxBody is the largest document read, in bytes.
	MaxBody = 1 << 20
	// MaxRedirects is how many redirects are followed for one address.
	MaxRedirects = 5
	// DefaultTimeout bounds one whole fetch, robots.txt and redirects
	// included.
	DefaultTimeout = 20 * time.Second
)

// Document is something read from a public address.
type Document struct {
	// URL is the canonical address the document was finally read from.
	URL         string
	ContentType string
	Body        []byte
}

// ErrRobotsDisallowed means the site's robots.txt does not allow the
// address to be read.
var ErrRobotsDisallowed = errors.New("robots.txt does not allow this address")

var errNotPublic = errors.New("address is not public")

// Fetcher reads public addresses.
type Fetcher struct {
	client  *http.Client
	timeout time.Duration
}

// New returns a Fetcher that connects directly, never through a proxy, and
// refuses to connect to any network address that is not public.
func New(timeout time.Duration) *Fetcher {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: guardDial}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           2,
		IdleConnTimeout:        30 * time.Second,
		ForceAttemptHTTP2:      false,
	}
	return NewWithTransport(transport, timeout)
}

// NewWithTransport returns a Fetcher over the given transport. Tests use it
// to serve made-up sites without a network.
func NewWithTransport(rt http.RoundTripper, timeout time.Duration) *Fetcher {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Fetcher{
		timeout: timeout,
		client: &http.Client{
			Transport: rt,
			// Redirects are followed by hand so that every step is
			// checked like a first request.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// guardDial runs just before a connection is made, with the network
// address the host name resolved to.
func guardDial(_, address string, _ syscall.RawConn) error {
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil || !PublicIP(addrPort.Addr()) {
		return errNotPublic
	}
	return nil
}

var reserved = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b::/96", "100::/64", "2001:db8::/32", "2002::/16",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// PublicIP reports whether ip is an address on the public internet.
func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range reserved {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// Fetch reads the document at address. It returns ErrRobotsDisallowed when
// robots.txt forbids it; every other error is a public error:
//
//	E_TIMEOUT  the site was slow, unreachable, busy or failing
//	E_PARSE    the address is broken, not public, missing, behind a
//	           sign-in, too large or redirects too often
func (f *Fetcher) Fetch(ctx context.Context, address string) (Document, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	norm, err := normalize.Normalize(address)
	if err != nil {
		return Document{}, err
	}
	current := norm.URL
	material := []byte(current)

	for hop := 0; hop <= MaxRedirects; hop++ {
		allowed, err := f.robotsAllow(ctx, current, material)
		if err != nil {
			return Document{}, err
		}
		if !allowed {
			return Document{}, ErrRobotsDisallowed
		}

		resp, err := f.get(ctx, current, material)
		if err != nil {
			return Document{}, err
		}
		status := resp.StatusCode
		if next, ok := redirect(resp); ok {
			resp.Body.Close()
			target, err := normalize.Normalize(normalize.Resolve(current, next))
			if err != nil {
				e, _ := errs.As(err)
				return Document{}, errs.New(e.Code, e.Reason, material)
			}
			current = target.URL
			continue
		}
		if err := statusError(status, material); err != nil {
			resp.Body.Close()
			return Document{}, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
		resp.Body.Close()
		if err != nil {
			return Document{}, errs.New(errs.Timeout, "upstream", material)
		}
		if len(body) > MaxBody {
			return Document{}, errs.New(errs.Parse, "too_large", material)
		}
		return Document{URL: current, ContentType: mediaType(resp.Header.Get("Content-Type")), Body: body}, nil
	}
	return Document{}, errs.New(errs.Parse, "too_many_redirects", material)
}

// get sends one GET request. It is the only place a request is made.
func (f *Fetcher) get(ctx context.Context, address string, material []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errs.New(errs.Parse, "bad_url", material)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, transportError(err, material)
	}
	return resp, nil
}

// transportError turns a failed request into a public error. The original
// error is dropped: it names hosts and network addresses.
func transportError(err error, material []byte) error {
	var certErr *tls.CertificateVerificationError
	switch {
	case errors.Is(err, errNotPublic):
		return errs.New(errs.Parse, "host_not_public", material)
	case errors.As(err, &certErr):
		return errs.New(errs.Parse, "certificate", material)
	default:
		return errs.New(errs.Timeout, "upstream", material)
	}
}

func redirect(resp *http.Response) (string, bool) {
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		if next := resp.Header.Get("Location"); next != "" {
			return next, true
		}
	}
	return "", false
}

func statusError(status int, material []byte) error {
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusUnauthorized, status == http.StatusForbidden,
		status == http.StatusPaymentRequired, status == http.StatusProxyAuthRequired:
		// Behind a sign-in or a paywall. Trawl goes no further.
		return errs.New(errs.Parse, "forbidden", material)
	case status == http.StatusNotFound, status == http.StatusGone:
		return errs.New(errs.Parse, "not_found", material)
	case status == http.StatusTooManyRequests, status == http.StatusRequestTimeout, status >= 500:
		return errs.New(errs.Timeout, "upstream", material)
	default:
		return errs.New(errs.Parse, "http_status", material)
	}
}

func mediaType(header string) string {
	media, _, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return media
}

// robotsAllow fetches the robots.txt of the address's site and applies it.
//
//	200            the file decides
//	401, 403       the site is closed to crawlers: not allowed
//	other 4xx      there is no robots.txt: allowed
//	5xx, 429, slow the answer is unknown, so the address is not read now:
//	               E_TIMEOUT, and the item is tried again later
func (f *Fetcher) robotsAllow(ctx context.Context, address string, material []byte) (bool, error) {
	cut := strings.Index(address, "://")
	slash := strings.IndexByte(address[cut+3:], '/')
	origin, path := address[:cut+3+slash], address[cut+3+slash:]

	current := origin + "/robots.txt"
	for hop := 0; hop <= MaxRedirects; hop++ {
		resp, err := f.get(ctx, current, material)
		if err != nil {
			return false, err
		}
		status := resp.StatusCode
		if next, ok := redirect(resp); ok {
			resp.Body.Close()
			target, err := normalize.Normalize(normalize.Resolve(current, next))
			if err != nil {
				// robots.txt points somewhere Trawl will not go, so
				// the site's wishes cannot be learned.
				return false, nil
			}
			current = target.URL
			continue
		}
		switch {
		case status == http.StatusOK:
			data, err := io.ReadAll(io.LimitReader(resp.Body, robots.MaxBytes))
			resp.Body.Close()
			if err != nil {
				return false, errs.New(errs.Timeout, "upstream", material)
			}
			return robots.Parse(data).Allowed(Agent, path), nil
		case status == http.StatusUnauthorized, status == http.StatusForbidden:
			resp.Body.Close()
			return false, nil
		case status >= 400 && status < 500 && status != http.StatusTooManyRequests && status != http.StatusRequestTimeout:
			resp.Body.Close()
			return true, nil
		default:
			resp.Body.Close()
			return false, errs.New(errs.Timeout, "upstream", material)
		}
	}
	return false, nil
}
