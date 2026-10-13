// Package wsproto serves gRPC services over ws-proto (protobuf RPC over
// WebSocket) as a managed public api. It takes the same register function
// as Service.GRPC, so one implementation serves both, recovers a panicking
// call into codes.Internal and traces every call like the gRPC server does.
package wsproto

import (
	"context"
	"maps"
	"slices"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	"github.com/gopherex/ws-proto/wsrpc"

	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/api"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/recovery"
	"github.com/gopherex/backplane/pkg/backplane/internal/routes"
)

const instrumentation = "github.com/gopherex/backplane/pkg/backplane/wsproto"

// Serve mounts a ws-proto endpoint at prefix on a public port of svc and
// announces it with the registered services' descriptors. origins is the
// browser origin policy: the zero value admits same-origin browsers and
// non-browser clients only. api.Interceptors and
// api.StreamInterceptors wrap this endpoint's calls only;
// api.Middleware wraps its upgrade handler.
func Serve[St any](
	svc *backplane.Service[St], prefix string, origins api.Origins,
	register func(grpc.ServiceRegistrar), opts ...api.WSProtoOption,
) {
	spec := link.APIs.WS(opts)
	srv := wsrpc.NewServer(originOption(origins))

	u := append([]grpc.UnaryServerInterceptor{recovery.Unary(svc.Log(), recovery.WSProto), unary}, spec.Unary...)
	st := append([]grpc.StreamServerInterceptor{recovery.Stream(svc.Log(), recovery.WSProto), stream}, spec.Stream...)

	reg := wsrpc.GRPCRegistrar(srv,
		wsrpc.WithUnaryInterceptor(routes.ChainUnary(u)), wsrpc.WithStreamInterceptor(routes.ChainStream(st)))
	register(reg)

	var services []string
	if si, ok := reg.(interface {
		GetServiceInfo() map[string]grpc.ServiceInfo
	}); ok {
		services = slices.Sorted(maps.Keys(si.GetServiceInfo()))
	}

	link.MountWS(svc, prefix, srv, services, spec)
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

// traced runs fn in a server span named after the method.
func traced(ctx context.Context, method string, fn func(ctx context.Context) error) error {
	ctx, s := otel.Tracer(instrumentation).Start(ctx, method,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("rpc.system", "ws-proto"), attribute.String("rpc.method", method)))
	defer s.End()

	err := fn(ctx)
	if err != nil {
		s.RecordError(err)
		s.SetStatus(codes.Error, err.Error())
	}

	return err
}

func unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	var res any

	err := traced(ctx, info.FullMethod, func(ctx context.Context) error {
		var err error

		res, err = next(ctx, req)

		return err
	})

	return res, err
}

type tracedStream struct {
	grpc.ServerStream

	ctx context.Context //nolint:containedctx // a ServerStream carries its context by contract
}

func (t tracedStream) Context() context.Context { return t.ctx }

func stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	return traced(ss.Context(), info.FullMethod, func(ctx context.Context) error {
		return next(srv, tracedStream{ServerStream: ss, ctx: ctx})
	})
}
