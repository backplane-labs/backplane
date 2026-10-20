package mtls

import (
	"context"
	"net"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// AuthInfo is the gRPC auth info of a connection the listener's TLS
// verified.
type AuthInfo struct {
	credentials.CommonAuthInfo

	Peer Peer
}

// AuthType implements credentials.AuthInfo.
func (AuthInfo) AuthType() string { return "mtls" }

// serverCredentials hands gRPC a connection the listener already
// terminated TLS on, with its peer: the handshake happened under cmux, so
// it is only read here.
type serverCredentials struct{}

//nolint:ireturn // credentials.TransportCredentials
func (serverCredentials) ServerHandshake(conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	p, ok := PeerOf(conn)
	if !ok {
		return nil, nil, ErrPeer
	}

	info := AuthInfo{CommonAuthInfo: credentials.CommonAuthInfo{SecurityLevel: credentials.PrivacyAndIntegrity}, Peer: p}

	return conn, info, nil
}

//nolint:ireturn // credentials.TransportCredentials
func (serverCredentials) ClientHandshake(context.Context, string, net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, ErrPeer
}

func (serverCredentials) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{SecurityProtocol: "tls"}
}

//nolint:ireturn // credentials.TransportCredentials
func (s serverCredentials) Clone() credentials.TransportCredentials { return s }

func (serverCredentials) OverrideServerName(string) error { return nil }

// GRPCServerOptions are the options of a gRPC server behind an mTLS
// listener: the peer as auth info, and every call authorized by src, the
// caller put in its context (PeerFrom).
func GRPCServerOptions(src Source) []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.Creds(serverCredentials{}),
		grpc.ChainUnaryInterceptor(func(
			ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler,
		) (any, error) {
			ctx, err := authorizeGRPC(ctx, src)
			if err != nil {
				return nil, err
			}

			return h(ctx, req)
		}),
		grpc.ChainStreamInterceptor(func(
			srv any, server grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler,
		) error {
			ctx, err := authorizeGRPC(server.Context(), src)
			if err != nil {
				return err
			}

			return h(srv, &stream{ServerStream: server, ctx: ctx})
		}),
	}
}

type stream struct {
	grpc.ServerStream

	ctx context.Context //nolint:containedctx // the stream's context with its caller
}

func (s *stream) Context() context.Context { return s.ctx }

func authorizeGRPC(ctx context.Context, src Source) (context.Context, error) {
	pr, _ := peer.FromContext(ctx)

	info, ok := pr.AuthInfo.(AuthInfo)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "mtls: no service certificate") //nolint:wrapcheck // a status
	}

	if allowed, reason := src.Authorize(info.Peer.Service); !allowed {
		return ctx, status.Errorf(codes.PermissionDenied, "mtls: %s may not call this service: %s", info.Peer.Service, reason)
	}

	return WithPeer(ctx, info.Peer), nil
}

// ConnContext puts the peer of an HTTP connection into its requests'
// context (http.Server.ConnContext).
func ConnContext(ctx context.Context, conn net.Conn) context.Context {
	if p, ok := PeerOf(conn); ok {
		return WithPeer(ctx, p)
	}

	return ctx
}

// Middleware authorizes every request of an mTLS port: 403 for a caller
// src refuses, 401 without a service certificate.
func Middleware(src Source) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PeerFrom(r.Context())
			if !ok {
				http.Error(w, "mtls: no service certificate", http.StatusUnauthorized)

				return
			}

			if allowed, reason := src.Authorize(p.Service); !allowed {
				http.Error(w, "mtls: "+p.Service+" may not call this service: "+reason, http.StatusForbidden)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
