package console_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gopherex/ws-proto/wsrpc"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/console"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

// The console on its own listener, under a prefix; its stop closes the
// listener and the open /ws connections.
//
//nolint:tparallel // the subtest's end is the stop under test
func TestListener(t *testing.T) {
	t.Parallel()

	var (
		cc   *wsrpc.ClientConn
		base string
	)

	t.Run("serving", func(t *testing.T) {
		h := backplanetest.New(t, backplanetest.Name("backplane"))
		c := console.New(h.Root(), console.Settings{
			Listen: "127.0.0.1:0", Prefix: "/backplane", AdminToken: adminToken, InsecureCookie: true,
		}, newMemSessions(), newAttempts(), registry.NewHub())
		h.Start()

		base = "http://" + c.Addr().String()

		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/backplane/auth/login",
			strings.NewReader(`{"token":"`+adminToken+`"}`))

		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = res.Body.Close()

		if res.StatusCode != http.StatusOK || len(res.Cookies()) != 1 {
			t.Fatalf("login: %s", res.Status)
		}

		hdr := http.Header{"Cookie": {cookieHeader(res.Cookies()[0])}, "Origin": {base}}

		// Not bound to the subtest: its end must not be what closes it.
		cc, err = wsrpc.Dial(context.Background(), "ws"+strings.TrimPrefix(base, "http")+"/backplane/ws", //nolint:usetesting // see above
			wsrpc.WithHeader(hdr))
		if err != nil {
			t.Fatal(err)
		}

		err = call(t.Context(), cc, "/backplane.console.v1.CatalogService/ListServices",
			&consolev1.ListServicesRequest{}, &consolev1.ListServicesResponse{})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Cleanup(func() { _ = cc.Close() })

	closed(t, cc)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/backplane/auth/session", http.NoBody)
	if res, err := http.DefaultClient.Do(req); err == nil {
		_ = res.Body.Close()
		t.Fatalf("listener open after stop: %s", res.Status)
	}
}
