package netguard_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/backplane-labs/backplane/pkg/backplane/netguard"
)

func TestForbidden(t *testing.T) {
	t.Parallel()

	for addr, want := range map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "172.16.0.1": true, "192.168.1.1": true, "169.254.169.254": true,
		"100.64.1.1": true, "0.0.0.0": true, "0.1.2.3": true, "224.0.0.1": true, "::1": true, "::": true,
		"fe80::1": true, "fc00::1": true, "fd12::1": true, "ff02::1": true, "::ffff:10.0.0.1": true,
		// IANA special-purpose IPv4: not globally reachable.
		"192.0.0.8": true, "192.0.2.1": true, "198.18.0.1": true, "198.19.255.254": true, "198.51.100.1": true,
		"203.0.113.1": true, "240.0.0.1": true, "255.255.255.255": true,
		// IPv6 embedding IPv4 (NAT64, 6to4, Teredo, compatible, SIIT); site-local; discard; documentation.
		"64:ff9b::a9fe:a9fe": true, "64:ff9b:1::1": true, "2002:a9fe:a9fe::1": true, "2001:0:4136:e378::1": true,
		"::a9fe:a9fe": true, "::ffff:0:a9fe:a9fe": true, "fec0::1": true, "100::1": true, "2001:db8::1": true,
		"2001:2::1": true,
		// Zoned: scoped to an interface, never public.
		"64:ff9b::a9fe:a9fe%eth0": true, "2a00:1450:4001::1%eth0": true,
		"8.8.8.8": false, "142.250.74.10": false, "198.20.0.1": false, "2001:4860:4860::8888": false,
		"2a00:1450:4001::1": false, "3fff::1": false,
	} {
		if got := netguard.Forbidden(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %v", addr, got)
		}
	}
}

func TestAllowed(t *testing.T) {
	t.Parallel()

	hosts := []string{"hooks.example.com", "*.bots.example.org"}
	for host, want := range map[string]bool{
		"hooks.example.com": true, "HOOKS.example.com.": true, "a.bots.example.org": true,
		"bots.example.org": false, "evil.example.com": false, "8.8.8.8": false, "fe80::1%eth0": false,
		"2a00:1450:4001::1": false,
	} {
		if got := netguard.Allowed(host, hosts); got != want {
			t.Errorf("%s: %v", host, got)
		}
	}
}

// A test server that redirects n times (to /hop?n=n-1), then answers 200;
// target: a fixed redirect target instead.
func server(t *testing.T, target string) *httptest.Server {
	t.Helper()

	var srv *httptest.Server

	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))

		switch {
		case target != "":
			http.Redirect(w, r, target, http.StatusFound)
		case n > 0:
			http.Redirect(w, r, srv.URL+"/hop?n="+strconv.Itoa(n-1), http.StatusFound)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestClient(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		to     string // the server's redirect target
		url    func(srv *httptest.Server) string
		client func(srv *httptest.Server) *http.Client
		status int
		err    error
	}{
		{
			name: "the real guard refuses loopback", url: func(s *httptest.Server) string { return s.URL },
			client: func(*httptest.Server) *http.Client { return netguard.Client() }, err: netguard.ErrForbiddenAddress,
		},
		{
			name: "http is refused", url: func(s *httptest.Server) string { return strings.Replace(s.URL, "https", "http", 1) },
			client: func(s *httptest.Server) *http.Client { return netguard.LoopbackClient(s) }, err: netguard.ErrForbiddenURL,
		},
		{
			name: "a host outside Hosts is refused", url: func(s *httptest.Server) string { return s.URL },
			client: func(s *httptest.Server) *http.Client {
				return netguard.LoopbackClient(s, netguard.Hosts("example.com"))
			},
			err: netguard.ErrForbiddenURL,
		},
		{
			name: "no redirect by default: the 3xx comes back", url: func(s *httptest.Server) string { return s.URL + "/?n=1" },
			client: func(s *httptest.Server) *http.Client { return netguard.LoopbackClient(s) }, status: http.StatusFound,
		},
		{
			name: "redirects within the limit are followed", url: func(s *httptest.Server) string { return s.URL + "/?n=2" },
			client: func(s *httptest.Server) *http.Client { return netguard.LoopbackClient(s, netguard.Redirects(2)) },
			status: http.StatusOK,
		},
		{
			name: "one redirect too many", url: func(s *httptest.Server) string { return s.URL + "/?n=3" },
			client: func(s *httptest.Server) *http.Client { return netguard.LoopbackClient(s, netguard.Redirects(2)) },
			err:    netguard.ErrRedirects,
		},
		{
			name: "a redirect to http is refused", to: "http://example.com/",
			url:    func(s *httptest.Server) string { return s.URL },
			client: func(s *httptest.Server) *http.Client { return netguard.LoopbackClient(s, netguard.Redirects(5)) },
			err:    netguard.ErrForbiddenURL,
		},
		{
			name: "a redirect to an internal address is refused at dial", to: "https://10.0.0.1:1/",
			url:    func(s *httptest.Server) string { return s.URL },
			client: func(s *httptest.Server) *http.Client { return netguard.LoopbackClient(s, netguard.Redirects(5)) },
			err:    netguard.ErrForbiddenAddress,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := server(t, tc.to)

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.url(srv), http.NoBody)
			if err != nil {
				t.Fatal(err)
			}

			res, err := tc.client(srv).Do(req)
			if res != nil {
				_ = res.Body.Close()
			}

			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err %v, want %v", err, tc.err)
				}

				return
			}

			if err != nil || res.StatusCode != tc.status {
				t.Fatalf("status %v %v, want %d", res, err, tc.status)
			}
		})
	}
}

func BenchmarkForbidden(b *testing.B) {
	for _, addr := range []string{"8.8.8.8", "10.1.2.3", "2a00:1450:4001::1", "64:ff9b::a9fe:a9fe"} {
		ip := netip.MustParseAddr(addr)

		b.Run(addr, func(b *testing.B) {
			for b.Loop() {
				netguard.Forbidden(ip)
			}
		})
	}
}

// TestMain installs the W3C propagators, as the SDK does at start.
func TestMain(m *testing.M) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	os.Exit(m.Run())
}

// A request to a guarded (untrusted) URL carries none of the caller's trace
// context: no traceparent, tracestate or baggage.
func TestClientSendsNoTrace(t *testing.T) {
	t.Parallel()

	got := make(chan string, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Traceparent") + r.Header.Get("Tracestate") + r.Header.Get("Baggage")
	}))
	t.Cleanup(srv.Close)

	member, err := baggage.NewMember("tenant", "acme")
	if err != nil {
		t.Fatal(err)
	}

	bag, err := baggage.New(member)
	if err != nil {
		t.Fatal(err)
	}

	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(baggage.ContextWithBaggage(t.Context(), bag), "caller")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	res, err := netguard.LoopbackClient(srv).Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = res.Body.Close()

	if sent := <-got; sent != "" {
		t.Fatalf("trace context sent to an untrusted URL: %q", sent)
	}
}
