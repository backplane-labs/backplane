package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/backplane-labs/backplane/examples/hello/internal/hellotest"
	"github.com/backplane-labs/backplane/examples/hello/internal/web"
)

func get(t *testing.T, h http.Handler, target string, header ...string) string {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
	if len(header) == 2 {
		req.Header.Set(header[0], header[1])
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return strings.TrimSpace(rec.Body.String())
}

func TestHello(t *testing.T) {
	t.Parallel()

	tree := hellotest.New(t, nil)
	tree.H.Start()

	h := web.Hello(tree.Greeter)

	if body := get(t, h, "/hello/?name=ann"); body != "Hello, ann!" {
		t.Fatalf("fallback: %q", body)
	}

	if body := get(t, h, "/hello/"); body != "Hello, world!" {
		t.Fatal(body)
	}
}

func TestGraphQL(t *testing.T) {
	t.Parallel()

	tree := hellotest.New(t, nil)
	tree.H.Start()

	h := web.ServedBy("hello")(web.GraphQL(tree.Greeter))

	for query, want := range map[string]string{
		`{"query":"{ greeting(name: \"gql\") }"}`: `{"data":{"greeting":"Hello, gql!"}}`,
		`{"query":"{ users { id } }"}`:            `{"errors":[{"message":"only { greeting(name: \"...\") } is served"}]}`,
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/graphql/", strings.NewReader(query))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if body := strings.TrimSpace(rec.Body.String()); body != want || rec.Header().Get("X-Served-By") != "hello" {
			t.Errorf("%s: %s", query, body)
		}
	}
}
