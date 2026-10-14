package api_test

import (
	"errors"
	"slices"
	"testing"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/api"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/routes"
)

func TestDeclarative(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		decl      api.Decl
		kind      backplanev1.ApiKind
		prefix    string
		host      string
		port      uint32
		hasSchema bool
	}{
		"grpc": {
			api.GRPC("hello.v1.Hello", api.Port(9000)),
			backplanev1.ApiKind_API_KIND_GRPC, "/hello.v1.Hello/", "", 9000, false,
		},
		"grpc descriptors": {
			api.GRPC("hello.v1.Hello", api.Descriptors([]byte{1})),
			backplanev1.ApiKind_API_KIND_GRPC, "/hello.v1.Hello/", "", 0, true,
		},
		"http host": {
			api.HTTP("/x/", api.Host("api.example.com"), api.OpenAPI([]byte("{}"))),
			backplanev1.ApiKind_API_KIND_HTTP, "/x/", "api.example.com", 0, true,
		},
		"http normalized": {
			api.HTTP("/api", api.Port(8081)),
			backplanev1.ApiKind_API_KIND_HTTP, "/api/", "", 8081, false,
		},
		"graphql": {
			api.GraphQL("/graphql", []byte("{}")),
			backplanev1.ApiKind_API_KIND_GRAPHQL, "/graphql/", "", 0, true,
		},
		"graphql no introspection": {
			api.GraphQL("/graphql/", nil, api.Host("gql.example.com")),
			backplanev1.ApiKind_API_KIND_GRAPHQL, "/graphql/", "gql.example.com", 0, false,
		},
		"wsproto": {
			api.WSProto("/ws", nil),
			backplanev1.ApiKind_API_KIND_WS_PROTO, "/ws/", "", 0, false,
		},
		"wsproto descriptors": {
			api.WSProto("/ws/", []byte{1}, api.Port(7000)),
			backplanev1.ApiKind_API_KIND_WS_PROTO, "/ws/", "", 7000, true,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r, err := link.APIs.Decl(c.decl)
			if err != nil {
				t.Fatal(err)
			}

			if r.GetKind() != c.kind || r.GetPrefix() != c.prefix || r.GetHost() != c.host || r.GetPort() != c.port {
				t.Fatalf("got %v", r)
			}

			if (r.GetSchema() != nil) != c.hasSchema {
				t.Fatalf("schema presence: %v", r.GetSchema())
			}
		})
	}
}

func TestGRPCDeclNamesService(t *testing.T) {
	t.Parallel()

	r, err := link.APIs.Decl(api.GRPC("hello.v1.Hello"))
	if err != nil || !slices.Equal(r.GetServices(), []string{"hello.v1.Hello"}) {
		t.Fatalf("services %v err %v", r.GetServices(), err)
	}
}

func TestBadPrefixIsDeclError(t *testing.T) {
	t.Parallel()

	for name, d := range map[string]api.Decl{
		"http relative":     api.HTTP("api"),
		"http method":       api.HTTP("GET /x"),
		"http wildcard":     api.HTTP("/x/{id}"),
		"graphql relative":  api.GraphQL("graphql", nil),
		"wsproto wildcard":  api.WSProto("/ws/{x}", nil),
		"http with options": api.HTTP("api", api.Port(1), api.Host("h")),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := link.APIs.Decl(d); !errors.Is(err, routes.ErrPrefix) {
				t.Fatalf("want ErrPrefix, got %v", err)
			}
		})
	}
}

func TestZeroDeclIsError(t *testing.T) {
	t.Parallel()

	if _, err := link.APIs.Decl(api.Decl{}); err == nil {
		t.Fatal("zero Decl accepted")
	}
}

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

	g := link.APIs.GRPC([]api.GRPCOption{api.Listen(":9000")})
	if g.Listen != ":9000" || g.Host != "" || g.OpenAPI != nil {
		t.Fatalf("grpc: %+v", g)
	}

	if d := link.APIs.GRPC([]api.GRPCOption(nil)); d.Listen != "" || d.Host != "" {
		t.Fatalf("grpc defaults: %+v", d)
	}

	h := link.APIs.HTTP([]api.HTTPOption{api.OpenAPI([]byte("{}")), api.Listen(":8081"), api.Host("h")})
	if h.Listen != ":8081" || h.Host != "h" || string(h.OpenAPI) != "{}" {
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
