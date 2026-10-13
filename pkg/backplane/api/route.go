// Package api describes the APIs a service serves, independently of deployment.
// Managed APIs are served by the SDK; declarations passed to Service.API
// describe the author's own listener. Publication and protocol conversion
// belong to the application and deployment. Middleware and interceptors
// execute inside the service.
package api

import (
	"errors"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/routes"
)

//nolint:gochecknoinits // installs the private accessors for the SDK core
func init() {
	link.APIs.Decl = func(d any) (*backplanev1.API, error) {
		decl, _ := d.(Decl)
		if decl.r == nil && decl.err == nil {
			return nil, errZeroDecl
		}

		return decl.r, errors.Join(decl.err, filesError(decl.files))
	}
	link.APIs.Files = func(d any) *routes.SchemaFiles { decl, _ := d.(Decl); return decl.files }
	link.APIs.GRPC = func(opts any) routes.Managed {
		var m routes.Managed

		list, _ := opts.([]GRPCOption)
		for _, o := range list {
			o.applyGRPC(&m)
		}

		return m
	}
	link.APIs.HTTP = func(opts any) routes.Managed {
		var m routes.Managed

		list, _ := opts.([]HTTPOption)
		for _, o := range list {
			o.applyHTTP(&m)
		}

		return m
	}
	link.APIs.WS = func(opts any) routes.Managed {
		var m routes.Managed

		list, _ := opts.([]WSProtoOption)
		for _, o := range list {
			o.applyWS(&m)
		}

		return m
	}
}

var errZeroDecl = errors.New("api: zero Decl: use api.GRPC, HTTP, GraphQL or WSProto")

// Option interfaces, one per function.
type (
	// GRPCOption configures Service.GRPC.
	GRPCOption interface{ applyGRPC(m *routes.Managed) }
	// HTTPOption configures Service.HTTP and Service.GraphQL.
	HTTPOption interface{ applyHTTP(m *routes.Managed) }
	// WSProtoOption configures wsproto.Serve.
	WSProtoOption interface{ applyWS(m *routes.Managed) }
	// GRPCDeclOption configures api.GRPC.
	GRPCDeclOption interface{ applyGRPCDecl(r *backplanev1.API) }
	// HTTPDeclOption configures api.HTTP.
	HTTPDeclOption interface{ applyHTTPDecl(r *backplanev1.API) }
	// DeclOption configures api.GraphQL and api.WSProto.
	DeclOption interface{ applyDecl(r *backplanev1.API) }
)

type listen string

func (l listen) applyGRPC(m *routes.Managed) { m.Listen = string(l) }
func (l listen) applyHTTP(m *routes.Managed) { m.Listen = string(l) }
func (l listen) applyWS(m *routes.Managed)   { m.Listen = string(l) }

// ListenOption applies to managed routes.
type ListenOption interface {
	GRPCOption
	HTTPOption
	WSProtoOption
}

// Listen serves a managed route on its own listener instead of the shared
// public port; routes with the same address share one server.
func Listen(addr string) ListenOption { return listen(addr) }

type host string

func (h host) applyGRPC(m *routes.Managed)      { m.Host = string(h) }
func (h host) applyHTTP(m *routes.Managed)      { m.Host = string(h) }
func (h host) applyWS(m *routes.Managed)        { m.Host = string(h) }
func (h host) applyGRPCDecl(r *backplanev1.API) { h.applyDecl(r) }
func (h host) applyHTTPDecl(r *backplanev1.API) { h.applyDecl(r) }
func (h host) applyDecl(r *backplanev1.API)     { r.Host = string(h) }

// HostOption applies to managed HTTP/ws-proto APIs and API declarations.
type HostOption interface {
	HTTPOption
	WSProtoOption
	GRPCDeclOption
	HTTPDeclOption
	DeclOption
}

// Host matches the route by Host header in addition to its prefix: an exact
// host ("api.example.com") or a wildcard ("*.example.com"). Managed HTTP
// routes with one prefix and different hosts share a port.
func Host(h string) HostOption { return host(h) }

type port uint32

func (p port) applyGRPCDecl(r *backplanev1.API) { r.Port = uint32(p) }
func (p port) applyHTTPDecl(r *backplanev1.API) { r.Port = uint32(p) }
func (p port) applyDecl(r *backplanev1.API)     { r.Port = uint32(p) }

// PortOption applies to declarative routes.
type PortOption interface {
	GRPCDeclOption
	HTTPDeclOption
	DeclOption
}

// Port is where the author serves a declarative route; 0 means the port
// registered in Consul.
func Port(p uint16) PortOption { return port(p) }

type openAPI []byte

func (o openAPI) applyHTTP(m *routes.Managed) { m.OpenAPI, m.SchemaFiles = o, nil }
func (o openAPI) applyHTTPDecl(r *backplanev1.API) {
	r.Schema = &backplanev1.API_Openapi{Openapi: o}
}

// OpenAPIOption applies to HTTP routes.
type OpenAPIOption interface {
	HTTPOption
	HTTPDeclOption
}

// OpenAPI attaches a self-contained OpenAPI document (JSON or YAML) to an
// HTTP API. spec can be a []byte populated by go:embed. Bundle multi-file
// specifications with internal $ref links before embedding, or use OpenAPIFS
// to carry several files without bundling. Each HTTP route may carry its own
// document; a service can announce multiple routes.
func OpenAPI(spec []byte) OpenAPIOption { return openAPI(spec) }

type descriptors []byte

func (d descriptors) applyGRPCDecl(r *backplanev1.API) {
	r.Schema = &backplanev1.API_Descriptors{Descriptors: d}
}

// Descriptors attaches a serialized FileDescriptorSet to a declarative gRPC
// route; managed routes derive descriptors from registration.
func Descriptors(fds []byte) GRPCDeclOption { return descriptors(fds) }

// Decl describes an API served on the author's own listener.
// Build one with GRPC, HTTP, GraphQL or WSProto.
type Decl struct {
	r     *backplanev1.API
	err   error
	files *routes.SchemaFiles
}

// GRPC declares a gRPC service (full name) the author serves.
func GRPC(service string, opts ...GRPCDeclOption) Decl {
	r := &backplanev1.API{
		Kind:     backplanev1.ApiKind_API_KIND_GRPC,
		Prefix:   "/" + service + "/",
		Services: []string{service},
	}
	for _, o := range opts {
		o.applyGRPCDecl(r)
	}

	return Decl{r: r}
}

// HTTP declares an HTTP prefix the author serves.
func HTTP(prefix string, opts ...HTTPDeclOption) Decl {
	r, err := prefixed(backplanev1.ApiKind_API_KIND_HTTP, prefix)
	for _, o := range opts {
		o.applyHTTPDecl(r)
	}

	return Decl{r: r, err: err, files: declaredFiles(r, opts)}
}

// GraphQL declares a GraphQL endpoint the author serves, with its
// introspection result (JSON); nil when there is none.
func GraphQL(prefix string, introspection []byte, opts ...DeclOption) Decl {
	r, err := prefixed(backplanev1.ApiKind_API_KIND_GRAPHQL, prefix)
	if introspection != nil {
		r.Schema = &backplanev1.API_Graphql{Graphql: introspection}
	}

	for _, o := range opts {
		o.applyDecl(r)
	}

	return Decl{r: r, err: err, files: declaredFiles(r, opts)}
}

// WSProto declares a ws-proto endpoint the author serves, with its
// serialized FileDescriptorSet; nil when there is none.
func WSProto(prefix string, descriptors []byte, opts ...DeclOption) Decl {
	r, err := prefixed(backplanev1.ApiKind_API_KIND_WS_PROTO, prefix)
	if descriptors != nil {
		r.Schema = &backplanev1.API_Descriptors{Descriptors: descriptors}
	}

	for _, o := range opts {
		o.applyDecl(r)
	}

	return Decl{r: r, err: err, files: declaredFiles(r, opts)}
}

func prefixed(kind backplanev1.ApiKind, prefix string) (*backplanev1.API, error) {
	p, err := routes.Prefix(prefix)

	//nolint:wrapcheck // routes.Prefix speaks for this package ("route: bad path prefix")
	return &backplanev1.API{Kind: kind, Prefix: p}, err
}
