package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/backplane-labs/backplane/examples/hello/internal/flows"
	"github.com/backplane-labs/backplane/examples/hello/internal/hellotest"
	"github.com/backplane-labs/backplane/examples/hello/internal/web"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
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
	f := flows.New(tree.H.Root(), tree.Greeter, tree.Store)
	tree.H.Start()

	h := web.Hello(f.Greet, tree.Greeter)

	// No binding: the local greeter answers.
	if body := get(t, h, "/hello/?name=ann"); body != "Hello, ann!" {
		t.Fatalf("fallback: %q", body)
	}

	backplanetest.Answer(tree.H, f.Greet, func(ctx context.Context, in flows.GreetIn) (flows.GreetOut, error) {
		if _, has := ctx.Deadline(); !has {
			return flows.GreetOut{}, context.DeadlineExceeded
		}

		return flows.GreetOut{Text: "hook " + in.Name + " " + backplanetest.CallKey(ctx)}, nil
	})

	if body := get(t, h, "/hello/", web.IdempotencyKey, "k-1"); body != "hook world k-1" {
		t.Fatalf("hook with key: %q", body)
	}

	events := backplanetest.Events(tree.H, tree.Greeter.Greeted())
	if len(events) != 2 || events[1].Name != "world" || events[1].Text != "hook world k-1" {
		t.Fatalf("bound greeting event: %+v", events)
	}

	// Temporal down: the fallback again.
	backplanetest.Unavailable(tree.H)

	if body := get(t, h, "/hello/?name=bob"); body != "Hello, bob!" {
		t.Fatalf("unavailable: %q", body)
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
