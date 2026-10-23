package mtls

import (
	"context"
	"net"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// optionalCredentials hand gRPC the connections of a listener serving TLS
// and plain alike (listener.TLSOptional): a verified one with its peer, a
// plain one as it is.
type optionalCredentials struct{ serverCredentials }

//nolint:ireturn // credentials.TransportCredentials
func (optionalCredentials) ServerHandshake(conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	if p, ok := PeerOf(conn); ok {
		info := AuthInfo{CommonAuthInfo: credentials.CommonAuthInfo{SecurityLevel: credentials.PrivacyAndIntegrity}, Peer: p}

		return conn, info, nil
	}

	return conn, nil, nil
}

//nolint:ireturn // credentials.TransportCredentials
func (s optionalCredentials) Clone() credentials.TransportCredentials { return s }

// Platform authorizes the calls of the platform port when it serves mutual
// TLS: a caller with a certificate is authorized by src (its peer put in
// the context); a plain caller passes when required is false, else only to
// what open accepts (the probes).
type Platform struct {
	Src      Source
	Required bool
	// OpenHTTP and OpenGRPC: the paths and methods a plain caller reaches
	// when Required.
	OpenHTTP func(path string) bool
	OpenGRPC func(method string) bool
}

// GRPCServerOptions are the options of the platform port's gRPC server.
func (p Platform) GRPCServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.Creds(optionalCredentials{}),
		grpc.ChainUnaryInterceptor(func(
			ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler,
		) (any, error) {
			ctx, err := p.authorize(ctx, info.FullMethod)
			if err != nil {
				return nil, err
			}

			return h(ctx, req)
		}),
		grpc.ChainStreamInterceptor(func(
			srv any, server grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler,
		) error {
			ctx, err := p.authorize(server.Context(), info.FullMethod)
			if err != nil {
				return err
			}

			return h(srv, &stream{ServerStream: server, ctx: ctx})
		}),
	}
}

func (p Platform) authorize(ctx context.Context, method string) (context.Context, error) {
	if pr, ok := peer.FromContext(ctx); ok {
		if _, verified := pr.AuthInfo.(AuthInfo); verified {
			return authorizeGRPC(ctx, p.Src)
		}
	}

	if p.Required && (p.OpenGRPC == nil || !p.OpenGRPC(method)) {
		return ctx, status.Error(codes.Unauthenticated, //nolint:wrapcheck // a status
			"mtls: the platform port needs a service certificate (backplane.mesh.platform=mtls)")
	}

	return ctx, nil
}

// Middleware authorizes the platform port's HTTP side the same way.
func (p Platform) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, ok := PeerFrom(r.Context())

		switch {
		case ok:
			if allowed, reason := p.Src.Authorize(caller.Service); !allowed {
				http.Error(w, "mtls: "+caller.Service+" may not call this service: "+reason, http.StatusForbidden)

				return
			}
		case p.Required && (p.OpenHTTP == nil || !p.OpenHTTP(r.URL.Path)):
			http.Error(w, "mtls: the platform port needs a service certificate", http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// HealthMethod reports whether method is of grpc.health.v1.Health: what a
// Consul gRPC check calls.
func HealthMethod(method string) bool { return strings.HasPrefix(method, "/grpc.health.v1.Health/") }
