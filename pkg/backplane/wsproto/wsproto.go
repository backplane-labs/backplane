// Package wsproto serves services over ws-proto (protobuf RPC over
// WebSocket) as a managed public api: gRPC services, with the same register
// function as Service.GRPC so one implementation serves both, or handlers
// generated for ws-proto itself. Every call is traced and a panicking one
// ends with codes.Internal, like on the gRPC server.
package wsproto

import (
	"context"
	"slices"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	"github.com/gopherex/ws-proto/wsrpc"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/api"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/recovery"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/routes"
)

const instrumentation = "github.com/backplane-labs/backplane/pkg/backplane/wsproto"

// Register is what Serve registers the endpoint's services with: the
// register function of Service.GRPC, or one that registers ws-proto
// generated handlers (Register<Service>Handler) on the endpoint's server.
type Register interface {
	func(grpc.ServiceRegistrar) | func(*wsrpc.Server)
}

// Serve mounts a ws-proto endpoint at prefix on a public port of svc and
// announces it with the registered services' descriptors. origins is the
// browser origin policy: the zero value admits same-origin browsers and
// non-browser clients only.
//
// api.Middleware wraps the endpoint once per connection: the connection's
// context is the one it passes on (see api.Middleware). api.CallMiddleware
// wraps every call; api.Interceptors and api.StreamInterceptors wrap the
// calls of gRPC-registered services only. api.Keepalive tunes pings.
func Serve[St any, R Register](
	svc *backplane.Service[St], prefix string, origins api.Origins, register R, opts ...api.WSProtoOption,
) {
	spec := link.APIs.WS(opts)
	srv := wsrpc.NewServer(append([]wsrpc.ServerOption{
		originOption(origins),
		wsrpc.WithMiddleware(recovery.Calls(svc.Log(), recovery.WSProto), traced),
	}, spec.WS...)...)

	switch register := any(register).(type) {
	case func(grpc.ServiceRegistrar):
		register(wsrpc.GRPCRegistrar(srv,
			wsrpc.WithUnaryInterceptor(routes.ChainUnary(spec.Unary)),
			wsrpc.WithStreamInterceptor(routes.ChainStream(spec.Stream))))
	case func(*wsrpc.Server):
		register(srv)
	}

	link.MountWS(svc, prefix, srv, services(srv.Methods()), spec)
}

// services are the full names of the services the methods
// ("/pkg.Service/Method") belong to, sorted.
func services(methods []string) []string {
	var out []string

	for _, m := range methods {
		if name, _, ok := strings.Cut(strings.TrimPrefix(m, "/"), "/"); ok {
			out = append(out, name)
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}

// originOption maps the policy onto wsrpc.
func originOption(o api.Origins) wsrpc.ServerOption {
	switch {
	case o.IsAny():
		return wsrpc.WithInsecureSkipOriginCheck()
	case len(o.Patterns()) == 0:
		return wsrpc.WithSameOriginOnly()
	default:
		return wsrpc.WithOriginPatterns(o.Patterns()...)
	}
}

// traced runs every call in a server span named after its method.
func traced(next wsrpc.Handler) wsrpc.Handler {
	return func(ctx context.Context, s *wsrpc.Stream) error {
		ctx, span := otel.Tracer(instrumentation).Start(ctx, s.Method(),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("rpc.system", "ws-proto"), attribute.String("rpc.method", s.Method())))
		defer span.End()

		err := next(ctx, s)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}

		return err
	}
}
