package backplane

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc/filters"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/listener"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/recovery"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/routes"
)

// serverOptions of every SDK gRPC server: telemetry without health-check
// noise, the limits of config.Server, panic recovery (first in the chain,
// so it covers every interceptor after it), then extra.
func (c *core) serverOptions(where string, extra ...grpc.ServerOption) []grpc.ServerOption {
	s := c.cfg.Server
	opts := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler(otelgrpc.WithFilter(filters.Not(filters.HealthCheck())))),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime: s.GRPCKeepaliveMinTime, PermitWithoutStream: s.GRPCPermitWithoutStream,
		}),
	}

	if s.GRPCMaxRecvMsgSize > 0 {
		opts = append(opts, grpc.MaxRecvMsgSize(int(s.GRPCMaxRecvMsgSize)))
	}

	opts = append(opts, recovery.ServerOptions(c.log, where)...)

	return append(opts, extra...)
}

// limits of the HTTP side of every listener.
func (c *core) limits() listener.Limits {
	s := c.cfg.Server

	return listener.Limits{
		ReadHeaderTimeout: s.ReadHeaderTimeout,
		IdleTimeout:       s.IdleTimeout,
		MaxHeaderBytes:    int(s.MaxHeaderBytes),
	}
}

// grpcServer of a public port, created on first use: the SDK's options,
// the author's GRPCServerOptions, then the per-registration interceptors;
// grpc.health.v1 follows readiness like on the platform port.
func (c *core) grpcServer(p *publicPort) *grpc.Server {
	if p.grpc == nil {
		p.dispatch = routes.NewDispatch()

		extra := append(append([]grpc.ServerOption(nil), c.opts.grpc...), p.dispatch.ServerOptions()...)
		p.grpc = grpc.NewServer(c.serverOptions(recovery.GRPCPublic, extra...)...)
		hv1.RegisterHealthServer(p.grpc, c.health.GRPC())
	}

	return p.grpc
}

// publicHandler is the traced, panic-safe HTTP side of a public port, or
// nil without HTTP routes.
func (c *core) publicHandler(p *publicPort) http.Handler {
	if p.http == nil {
		return nil
	}

	return otelhttp.NewHandler(recovery.HTTP(c.log, recovery.HTTPPublic, p.http), "public"+p.addr)
}
