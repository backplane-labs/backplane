// Package netguard keeps a service's requests to URLs it was handed — a
// webhook receiver, a push endpoint, a link to preview — off the
// installation's own network (SSRF).
//
//	client := netguard.Client(netguard.Redirects(5))
//	res, err := client.Do(req) // errors.Is(err, netguard.ErrForbiddenAddress), ErrForbiddenURL
//
// Every request and every redirect hop is https (Hosts when set), and its
// connection goes only to a public address: checked on the address dialed,
// after DNS, so a name that resolves inside — or rebinds to it — is
// refused. Requests go direct: through a proxy the address dialed would be
// the proxy's. Redirects are not followed unless Redirects allows them.
// The client's *url.Error names the whole URL: scrub it before logging when
// the URL may carry a token.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Errors.
var (
	// ErrForbiddenAddress: the destination resolved to an address that is
	// not public (Forbidden).
	ErrForbiddenAddress = errors.New("netguard: destination is not a public address")
	// ErrForbiddenURL: a request or redirect URL that is not https, or whose
	// host is not among Hosts.
	ErrForbiddenURL = errors.New("netguard: url is not allowed")
	// ErrRedirects: more redirects than Redirects allows.
	ErrRedirects = errors.New("netguard: too many redirects")
)

// The guarded transport: net/http's DefaultTransport settings.
const (
	dialTimeout           = 30 * time.Second
	keepAlive             = 30 * time.Second
	maxIdleConns          = 100
	idleConnTimeout       = 90 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	expectContinueTimeout = time.Second
)

// globalUnicast6 is the only IPv6 range that is public: everything outside
// it is not global unicast — IPv4-compatible and SIIT forms, NAT64,
// discard, unique-local, link-local, site-local, multicast.
//
//nolint:gochecknoglobals // a constant prefix
var globalUnicast6 = netip.MustParsePrefix("2000::/3")

// special are the IANA special-purpose ranges inside IPv4 and 2000::/3 that
// netip does not classify: not globally reachable, or embedding an IPv4
// address a relay would reach (Teredo, 6to4), so an internal one,
// 169.254.169.254 included, would pass under them.
//
//nolint:gochecknoglobals // constant prefixes
var special = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // this network
	netip.MustParsePrefix("100.64.0.0/10"),   // shared address space (carrier-grade NAT)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking, often used inside networks
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, broadcast included
	netip.MustParsePrefix("2001::/23"),       // IETF protocol assignments, Teredo included
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4
}

// Forbidden reports whether addr is not public: loopback, private,
// link-local (169.254.169.254 included), shared, multicast, unspecified,
// another special-purpose range, IPv6 outside global unicast or embedding
// IPv4, or zoned (scoped to an interface).
func Forbidden(addr netip.Addr) bool {
	if addr.Zone() != "" {
		return true
	}

	addr = addr.Unmap()

	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() ||
		(addr.Is6() && !globalUnicast6.Contains(addr)) {
		return true
	}

	for _, p := range special {
		if p.Contains(addr) {
			return true
		}
	}

	return false
}

// Allowed reports whether host matches hosts: an exact name or *.suffix;
// never an IP address, zoned ones included.
func Allowed(host string, hosts []string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}

	host = strings.ToLower(strings.TrimSuffix(host, "."))

	for _, h := range hosts {
		h = strings.ToLower(h)
		if suffix, ok := strings.CutPrefix(h, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
		} else if host == h {
			return true
		}
	}

	return false
}

// Transport dials only public addresses (ErrForbiddenAddress) and goes
// direct, never through a proxy. It checks no URL: Client does.
func Transport() *http.Transport { return transport(Forbidden) }

func transport(forbidden func(netip.Addr) bool) *http.Transport {
	refuse := func(_, address string, _ syscall.RawConn) error {
		addr, err := netip.ParseAddrPort(address)
		if err != nil || forbidden(addr.Addr()) {
			return fmt.Errorf("%w: %s", ErrForbiddenAddress, address)
		}

		return nil
	}

	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: keepAlive, Control: refuse}

	// net/http's DefaultTransport settings, without its proxy.
	return &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxIdleConns,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: expectContinueTimeout,
	}
}

// Option configures Client.
type Option func(*settings)

type settings struct {
	hosts     []string
	redirects int
}

// Hosts limits every request and redirect to these hosts: exact names or
// *.suffix (Allowed). Without it any public host is allowed.
func Hosts(hosts ...string) Option {
	return func(s *settings) { s.hosts = append(s.hosts, hosts...) }
}

// Redirects follows at most n redirects, each checked as the first request
// (https, Hosts, a public address); one more is ErrRedirects. Without it
// (or n <= 0) no redirect is followed: the 3xx response is returned, as a
// delivery (a webhook, a push) wants.
func Redirects(n int) Option { return func(s *settings) { s.redirects = n } }

// Client is an *http.Client over Transport whose every request and
// redirect is https to an allowed host, traced (OpenTelemetry). Set its Timeout, or use request
// contexts, to bound a fetch.
func Client(opts ...Option) *http.Client { return client(Transport(), opts) }

func client(base http.RoundTripper, opts []Option) *http.Client {
	var s settings
	for _, o := range opts {
		o(&s)
	}

	return &http.Client{
		// Every allowed request is a client span carrying the caller's
		// trace context (traceparent); a refused one never leaves.
		Transport: checked{base: otelhttp.NewTransport(base), hosts: s.hosts},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if s.redirects <= 0 {
				return http.ErrUseLastResponse
			}

			if len(via) > s.redirects {
				return fmt.Errorf("%w: more than %d", ErrRedirects, s.redirects)
			}

			return nil
		},
	}
}

// checked refuses a request that is not https or not to an allowed host;
// it sees the first request and every redirect.
type checked struct {
	base  http.RoundTripper
	hosts []string
}

func (c checked) RoundTrip(req *http.Request) (*http.Response, error) {
	target := req.URL

	// Only the scheme and host are named: a path or query may carry a token.
	switch {
	case target.Scheme != "https" || target.Host == "":
		return nil, fmt.Errorf("%w: %s://%s is not https", ErrForbiddenURL, target.Scheme, target.Host)
	case len(c.hosts) > 0 && !Allowed(target.Hostname(), c.hosts):
		return nil, fmt.Errorf("%w: host %s", ErrForbiddenURL, target.Hostname())
	}

	return c.base.RoundTrip(req) //nolint:wrapcheck // the transport's own error, ErrForbiddenAddress included
}
