package api_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/api"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/routes"
)

func TestPrefix(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in, want string
		ok       bool
	}{
		"slash added": {"/api", "/api/", true},
		"kept":        {"/api/", "/api/", true},
		"root":        {"/", "/", true},
		"nested":      {"/a/b", "/a/b/", true},
		"relative":    {"api", "", false},
		"empty":       {"", "", false},
		"method":      {"GET /x", "", false},
		"tab":         {"/x\t", "", false},
		"wildcard":    {"/x/{id}", "", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := routes.Prefix(c.in)
			if c.ok != (err == nil) || got != c.want {
				t.Fatalf("Prefix(%q) = %q, %v", c.in, got, err)
			}

			if err != nil && !errors.Is(err, routes.ErrPrefix) {
				t.Fatalf("error does not wrap ErrPrefix: %v", err)
			}
		})
	}
}

func TestManagedOptions(t *testing.T) {
	t.Parallel()

	g := link.APIs.GRPC([]api.GRPCOption{api.Listen("grpc", ":9000")})
	if g.Listener != "grpc" || g.Listen != ":9000" || g.Host != "" {
		t.Fatalf("grpc: %+v", g)
	}

	if d := link.APIs.GRPC([]api.GRPCOption(nil)); d.Listen != "" || d.Host != "" {
		t.Fatalf("grpc defaults: %+v", d)
	}

	h := link.APIs.HTTP([]api.HTTPOption{api.Listen("admin", ":8081"), api.Host("h")})
	if h.Listener != "admin" || h.Listen != ":8081" || h.Host != "h" {
		t.Fatalf("http: %+v", h)
	}
}

func TestOrigins(t *testing.T) {
	t.Parallel()

	var zero api.Origins
	if zero.IsAny() || len(zero.Patterns()) != 0 {
		t.Fatalf("zero origins: %v %v", zero.IsAny(), zero.Patterns())
	}

	if !api.AnyOrigin().IsAny() {
		t.Fatal("AnyOrigin")
	}

	o := api.AllowOrigins("app.example.com", "*.example.com")
	if o.IsAny() || !slices.Equal(o.Patterns(), []string{"app.example.com", "*.example.com"}) {
		t.Fatalf("patterns: %v", o.Patterns())
	}

	o.Patterns()[0] = "changed"
	if o.Patterns()[0] != "app.example.com" {
		t.Fatal("Patterns exposes the internal slice")
	}
}
