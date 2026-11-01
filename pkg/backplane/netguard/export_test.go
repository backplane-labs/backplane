package netguard

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
)

// LoopbackClient is Client over a transport that allows loopback (the test
// servers) and trusts srv's certificate; every other check is Client's.
func LoopbackClient(srv *httptest.Server, opts ...Option) *http.Client {
	t := transport(func(addr netip.Addr) bool { return !addr.IsLoopback() && Forbidden(addr) })
	t.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()

	return client(t, opts)
}
