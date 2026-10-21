package api

import (
	"net/http"

	"google.golang.org/grpc"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/routes"
)

type unary []grpc.UnaryServerInterceptor

func (u unary) applyGRPC(m *routes.Managed) { m.Unary = append(m.Unary, u...) }
func (u unary) applyWS(m *routes.Managed)   { m.Unary = append(m.Unary, u...) }

type stream []grpc.StreamServerInterceptor

func (s stream) applyGRPC(m *routes.Managed) { m.Stream = append(m.Stream, s...) }
func (s stream) applyWS(m *routes.Managed)   { m.Stream = append(m.Stream, s...) }

// InterceptorOption applies to Service.GRPC, Service.Internal and
// wsproto.Serve.
type InterceptorOption interface {
	GRPCOption
	WSProtoOption
}

// Interceptors wrap the unary calls of this registration's services only,
// the first outermost, inside the SDK's recovery and tracing. The same
// register function passed to Service.GRPC and wsproto.Serve takes them
// separately.
func Interceptors(i ...grpc.UnaryServerInterceptor) InterceptorOption {
	return unary(append([]grpc.UnaryServerInterceptor(nil), i...))
}

// StreamInterceptors wrap the streaming calls of this registration's
// services only, the first outermost.
func StreamInterceptors(i ...grpc.StreamServerInterceptor) InterceptorOption {
	return stream(append([]grpc.StreamServerInterceptor(nil), i...))
}

type middleware func(http.Handler) http.Handler

func (mw middleware) applyHTTP(m *routes.Managed) { m.Middleware = append(m.Middleware, mw) }
func (mw middleware) applyWS(m *routes.Managed)   { m.Middleware = append(m.Middleware, mw) }

// MiddlewareOption applies to Service.HTTP, Service.GraphQL and
// wsproto.Serve.
type MiddlewareOption interface {
	HTTPOption
	WSProtoOption
}

// Middleware wraps this route's handler; repeated, the first is outermost.
// On ws-proto it sees the upgrade request.
func Middleware(mw func(http.Handler) http.Handler) MiddlewareOption { return middleware(mw) }

type reflection struct{}

func (reflection) applyGRPC(m *routes.Managed) { m.Reflection = true }

// Reflection serves grpc.reflection (v1 and v1alpha) on this route's port,
// once per port, for grpcurl and similar tools. It is not announced to
// external routing: it answers on the port directly.
func Reflection() GRPCOption { return reflection{} }

type mtlsOption struct{}

func (mtlsOption) applyGRPC(m *routes.Managed) { m.MTLS = true }
func (mtlsOption) applyHTTP(m *routes.Managed) { m.MTLS = true }
func (mtlsOption) applyWS(m *routes.Managed)   { m.MTLS = true }

// MTLSOption applies to Service.GRPC, Service.HTTP, Service.GraphQL and
// wsproto.Serve.
type MTLSOption interface {
	GRPCOption
	HTTPOption
	WSProtoOption
}

// MTLS serves the route's port to services of the mesh only: mutual TLS
// with the certificates of the installed driver (Consul's CA), every call
// authorized by the mesh's rules (Consul intentions) and its caller in
// backplane.CallerOf. A port is mTLS or not as a whole: every route on it
// says the same, or Run fails. Without the driver or Consul, Run fails.
// Clients call it with Root.ServiceCredentials (gRPC) or Root.ServiceTLS.
func MTLS() MTLSOption { return mtlsOption{} }
