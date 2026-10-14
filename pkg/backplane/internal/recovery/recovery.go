// Package recovery turns a panic in a request handler into an error for
// that request instead of a crashed process: gRPC and ws-proto calls get
// codes.Internal, HTTP requests 500. Every recovered panic is logged as an
// OpenTelemetry exception record (with its stack and the request's trace)
// and counted in backplane.panics{where}.
package recovery

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/exception"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
)

// Where a panic was recovered: the value of the metric's where attribute.
const (
	GRPCPublic   = "grpc.public"
	GRPCInternal = "grpc.internal"
	WSProto      = "wsproto"
	HTTPPublic   = "http.public"
	HTTPPlatform = "http.platform"
)

// report logs and counts a recovered panic.
func report(ctx context.Context, log *xlog.Logger, where, target string, v any) {
	metrics.Panic(ctx, where)
	log.Ctx().Error(ctx, "panic recovered", append(exception.Panic(v, debug.Stack()),
		xlog.String("where", where), xlog.String("target", target))...)
}

func internal() error {
	return status.Error(codes.Internal, "internal error") //nolint:wrapcheck // gRPC status
}

// Unary recovers a panicking unary handler (and the interceptors after
// this one) into codes.Internal.
func Unary(log *xlog.Logger, where string) grpc.UnaryServerInterceptor {
	//nolint:nonamedreturns // recover sets the result
	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler,
	) (res any, err error) {
		defer func() {
			if v := recover(); v != nil {
				report(ctx, log, where, info.FullMethod, v)

				res, err = nil, internal()
			}
		}()

		return next(ctx, req)
	}
}

// Stream recovers a panicking stream handler into codes.Internal.
//
// The directive below is read by contextcheck itself (callers are
// checked through this function), so nolintlint cannot see it used.
//
//nolint:contextcheck,nolintlint // reports with the stream's own context
func Stream(log *xlog.Logger, where string) grpc.StreamServerInterceptor {
	return func(
		srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler,
	) (err error) {
		ctx := stream.Context()

		defer func() {
			if v := recover(); v != nil {
				report(ctx, log, where, info.FullMethod, v)

				err = internal()
			}
		}()

		return next(srv, stream)
	}
}

// ServerOptions installs Unary and Stream first in the server's chain, so
// they also cover the interceptors that follow.
func ServerOptions(log *xlog.Logger, where string) []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(Unary(log, where)),
		grpc.ChainStreamInterceptor(Stream(log, where)),
	}
}

// HTTP recovers a panicking handler: the client gets 500 when nothing was
// written yet. http.ErrAbortHandler keeps its meaning (abort the response
// silently) and is re-raised for net/http.
func HTTP(log *xlog.Logger, where string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		defer func() {
			v := recover()
			if v == nil {
				return
			}

			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}

			report(ctx, log, where, r.Method+" "+r.URL.Path, v)
			// A hijacked or already answered response ignores this.
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}()

		next.ServeHTTP(w, r)
	})
}
