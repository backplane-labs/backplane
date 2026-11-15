package testhttp_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/backplane-labs/backplane/internal/testhttp"
)

// A request on the test's client survives another server's Close.
func TestClientOwnsItsTransport(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)

	c := testhttp.Client()
	if c.Transport == http.DefaultTransport || c.Transport == testhttp.Client().Transport {
		t.Fatal("each call is a client of its own transport")
	}

	httptest.NewServer(http.NotFoundHandler()).Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = res.Body.Close()
}
