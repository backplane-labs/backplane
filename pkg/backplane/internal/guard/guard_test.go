package guard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
)

func TestHTTP(t *testing.T) {
	t.Parallel()

	h := guard.New("s3cret").HTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for presented, want := range map[string]int{"": 403, "wrong": 403, "s3cret": 200} {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		if presented != "" {
			req.Header.Set(guard.HTTPHeader, presented)
		}

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != want {
			t.Errorf("secret %q: got %d want %d", presented, rec.Code, want)
		}
	}
}

func TestEmptySecretDisables(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	guard.New("").HTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestGRPC(t *testing.T) {
	t.Parallel()

	g := guard.New("s3cret")

	with := func(secret string) context.Context {
		return metadata.NewIncomingContext(context.Background(), metadata.Pairs(guard.Header, secret))
	}
	if err := g.Authorize(with("s3cret"), "/hello.console.v1.AdminService/GetStats"); err != nil {
		t.Fatalf("valid secret rejected: %v", err)
	}

	if err := g.Authorize(with("wrong"), "/hello.console.v1.AdminService/GetStats"); err == nil {
		t.Fatal("wrong secret accepted")
	}

	if err := g.Authorize(context.Background(), "/hello.console.v1.AdminService/GetStats"); err == nil {
		t.Fatal("missing secret accepted")
	}

	if err := g.Authorize(context.Background(), "/grpc.health.v1.Health/Check"); err != nil {
		t.Fatalf("health must be open: %v", err)
	}

	if len(g.ServerOptions()) != 2 {
		t.Fatal("want unary and stream interceptors")
	}
}

func TestPreviousSecretAccepted(t *testing.T) {
	t.Parallel()

	g := guard.New("new", "old", "")
	h := g.HTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for presented, want := range map[string]int{"new": 200, "old": 200, "": 403, "other": 403} {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		if presented != "" {
			req.Header.Set(guard.HTTPHeader, presented)
		}

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != want {
			t.Errorf("http secret %q: got %d want %d", presented, rec.Code, want)
		}

		md := metadata.NewIncomingContext(context.Background(), metadata.Pairs(guard.Header, presented))
		if err := g.Authorize(md, "/svc.v1.S/M"); (err == nil) != (want == 200) {
			t.Errorf("grpc secret %q: %v", presented, err)
		}
	}
}

func TestPreviousAloneDoesNotEnable(t *testing.T) {
	t.Parallel()

	if err := guard.New("", "old").Authorize(context.Background(), "/svc.v1.S/M"); err != nil {
		t.Fatalf("empty secret must disable the guard: %v", err)
	}
}
