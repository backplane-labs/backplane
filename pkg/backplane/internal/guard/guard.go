// Package guard protects the platform port with the installation's internal
// secret: the console relay presents it, the SDK checks it. Health stays
// open for Consul checks and probes. An empty secret disables the guard.
// While the secret rotates the previous one is accepted too.
package guard

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Header carries the secret: gRPC metadata key and, canonicalized,
// HTTPHeader.
const (
	Header     = "bp-internal-secret"
	HTTPHeader = "Bp-Internal-Secret"
	// SessionHeader names the console session of a call the console relays:
	// the console drops the browser's bp-* keys and sets it itself.
	SessionHeader = "bp-console-session"
)

const healthPrefix = "/grpc.health.v1.Health/"

// Guard checks the secret.
type Guard struct{ secrets [][]byte }

// New creates a guard accepting secret and, during a rotation, the previous
// ones; an empty secret allows everything whatever the previous ones are.
func New(secret string, previous ...string) Guard {
	if secret == "" {
		return Guard{}
	}

	g := Guard{secrets: [][]byte{[]byte(secret)}}

	for _, p := range previous {
		if p != "" {
			g.secrets = append(g.secrets, []byte(p))
		}
	}

	return g
}

func (g Guard) disabled() bool { return len(g.secrets) == 0 }

// ok compares the presented secret with every accepted one in constant time,
// without stopping at a match.
func (g Guard) ok(presented string) bool {
	if g.disabled() {
		return true
	}

	match := 0
	for _, s := range g.secrets {
		match |= subtle.ConstantTimeCompare(s, []byte(presented))
	}

	return match == 1
}

// Authorize checks the secret in incoming gRPC metadata; health is exempt.
// The error is a gRPC status and is returned as is.
func (g Guard) Authorize(ctx context.Context, method string) error {
	if g.disabled() || strings.HasPrefix(method, healthPrefix) {
		return nil
	}

	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get(Header); len(values) == 1 && g.ok(values[0]) {
		return nil
	}

	// A gRPC status: returned as is so the code reaches the caller.
	return status.Error(codes.PermissionDenied, "internal secret required") //nolint:wrapcheck // see above
}

// ServerOptions installs the guard on a gRPC server.
func (g Guard) ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(g.unary), grpc.ChainStreamInterceptor(g.stream)}
}

func (g Guard) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	if err := g.Authorize(ctx, info.FullMethod); err != nil {
		return nil, err
	}

	return next(withSession(ctx), req)
}

func (g Guard) stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	if err := g.Authorize(ss.Context(), info.FullMethod); err != nil {
		return err
	}

	return next(srv, &stream{ServerStream: ss, ctx: withSession(ss.Context())})
}

type stream struct {
	grpc.ServerStream

	ctx context.Context //nolint:containedctx // the stream's context with its session
}

func (s *stream) Context() context.Context { return s.ctx }

type sessionKey struct{}

// withSession is ctx carrying the console session of an authorized call,
// when its metadata names one.
func withSession(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)

	values := md.Get(SessionHeader)
	if len(values) != 1 {
		return ctx
	}

	id, err := uuid.Parse(values[0])
	if err != nil || id == uuid.Nil {
		return ctx
	}

	return WithSession(ctx, id)
}

// WithSession is ctx carrying session as the console session of a call that
// passed the guard (backplanetest.Operator).
func WithSession(ctx context.Context, session uuid.UUID) context.Context {
	return context.WithValue(ctx, sessionKey{}, session)
}

// Session is the console session of a call that passed the guard.
func Session(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(sessionKey{}).(uuid.UUID)

	return id, ok
}

// HTTP wraps a handler.
func (g Guard) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.ok(r.Header.Get(HTTPHeader)) {
			http.Error(w, "internal secret required", http.StatusForbidden)

			return
		}

		next.ServeHTTP(w, r)
	})
}
