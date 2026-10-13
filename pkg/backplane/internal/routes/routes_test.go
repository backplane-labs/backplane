package routes_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"google.golang.org/grpc"

	"github.com/gopherex/backplane/pkg/backplane/internal/routes"
)

func TestServiceOf(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"/pkg.v1.Svc/Method": "pkg.v1.Svc",
		"pkg.Svc/M":          "pkg.Svc",
		"/nomethod":          "nomethod",
	} {
		if got := routes.ServiceOf(in); got != want {
			t.Fatalf("ServiceOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func recorder(order *[]string, name string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		*order = append(*order, name)

		return next(ctx, req)
	}
}

func streamRecorder(order *[]string, name string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		*order = append(*order, name)

		return next(srv, ss)
	}
}

// Interceptors run only for the services they were added for, in order.
func TestDispatch(t *testing.T) {
	t.Parallel()

	var order []string

	d := routes.NewDispatch()
	d.Add([]string{"a.A", "a.B"}, []grpc.UnaryServerInterceptor{recorder(&order, "1"), recorder(&order, "2")},
		[]grpc.StreamServerInterceptor{streamRecorder(&order, "s")})
	d.Add([]string{"c.C"}, nil, nil)

	if routes.ChainUnary(nil) != nil || routes.ChainStream(nil) != nil {
		t.Fatal("empty chain is not nil")
	}

	if len(d.ServerOptions()) != 2 {
		t.Fatal("server options")
	}

	call := func(method string) {
		order = append(order, "|")

		handler := func(context.Context, any) (any, error) {
			order = append(order, "h")

			return struct{}{}, nil
		}

		if _, err := d.Unary()(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: method}, handler); err != nil {
			t.Fatal(err)
		}
	}

	call("/a.A/X")
	call("/a.B/Y")
	call("/c.C/Z")
	call("/other.O/Z")

	want := []string{"|", "1", "2", "h", "|", "1", "2", "h", "|", "h", "|", "h"}
	if !slices.Equal(order, want) {
		t.Fatalf("order %v, want %v", order, want)
	}

	order = nil

	err := d.Stream()(nil, nil, &grpc.StreamServerInfo{FullMethod: "/a.A/S"},
		func(any, grpc.ServerStream) error { order = append(order, "h"); return nil })
	if err != nil || !slices.Equal(order, []string{"s", "h"}) {
		t.Fatalf("stream: %v %v", order, err)
	}
}

func TestChainStreamOrder(t *testing.T) {
	t.Parallel()

	var order []string

	chain := routes.ChainStream([]grpc.StreamServerInterceptor{
		streamRecorder(&order, "a"), streamRecorder(&order, "b"), streamRecorder(&order, "c"),
	})

	_ = chain(nil, nil, &grpc.StreamServerInfo{}, func(any, grpc.ServerStream) error {
		order = append(order, "h")

		return nil
	})

	if !slices.Equal(order, []string{"a", "b", "c", "h"}) {
		t.Fatalf("order %v", order)
	}
}

func serve(t *testing.T, h http.Handler, host string) (int, string) {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/x", http.NoBody)
	req.Host = host

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Result().Body)

	return rec.Code, string(body)
}

func text(s string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, s) })
}

func TestHosts(t *testing.T) {
	t.Parallel()

	h := routes.NewHosts()
	if !h.Add("API.example.com", text("exact")) || !h.Add("*.example.com", text("wild")) ||
		!h.Add("*.b.example.com", text("longer")) {
		t.Fatal("add")
	}

	if h.Add("api.example.com", text("dup")) || h.Add("*.example.com", text("dup")) {
		t.Fatal("duplicate host accepted")
	}

	cases := map[string]struct {
		code int
		body string
	}{
		"api.example.com:8080": {http.StatusOK, "exact"},
		"x.example.com":        {http.StatusOK, "wild"},
		"x.b.example.com":      {http.StatusOK, "longer"},
		"other.org":            {http.StatusNotFound, ""},
	}
	for host, want := range cases {
		code, body := serve(t, h, host)
		if code != want.code || (want.body != "" && body != want.body) {
			t.Fatalf("%s: %d %q", host, code, body)
		}
	}

	if !h.Add("", text("any")) || h.Add("", text("dup")) {
		t.Fatal("any host")
	}

	if _, body := serve(t, h, "other.org"); body != "any" {
		t.Fatalf("fallback: %q", body)
	}
}
