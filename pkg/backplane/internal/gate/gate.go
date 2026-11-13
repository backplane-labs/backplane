// Package gate admits internal API calls only while the author's tree is
// up: before it started they get Unavailable, and stopping waits for the
// calls in flight before the tree (and its dependencies) stops. When the
// stop budget runs out first, the contexts of the calls still running are
// cancelled, so a long stream ends instead of outliving the tree. Health is
// never gated.
package gate

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const healthPrefix = "/grpc.health.v1.Health/"

// Gate tracks admitted calls.
type Gate struct {
	mu    sync.Mutex
	open  bool
	calls map[*call]struct{}
	idle  chan struct{} // closed when the last call leaves after Close
}

type call struct{ cancel context.CancelFunc }

// New creates a closed gate.
func New() *Gate { return &Gate{calls: map[*call]struct{}{}} }

// Open admits calls.
func (g *Gate) Open() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.open = true
}

// Close stops admitting and waits for admitted calls within ctx. When ctx
// ends first, it cancels the contexts of the calls still running and
// reports them.
func (g *Gate) Close(ctx context.Context) error {
	g.mu.Lock()
	g.open = false

	if len(g.calls) == 0 {
		g.mu.Unlock()

		return nil
	}

	// Concurrent closes share one channel: replacing it would strand the
	// first waiter until its ctx ends.
	if g.idle == nil {
		g.idle = make(chan struct{})
	}

	idle := g.idle
	g.mu.Unlock()

	select {
	case <-idle:
		return nil
	case <-ctx.Done():
	}

	g.mu.Lock()
	n := len(g.calls)

	for c := range g.calls {
		c.cancel()
	}
	g.mu.Unlock()

	return fmt.Errorf("gate: %d internal calls cancelled: %w", n, ctx.Err())
}

// enter admits a call; ctx is the call's context, cancelled by a Close
// that ran out of budget.
func (g *Gate) enter(ctx context.Context, method string) (context.Context, func(), bool) {
	if strings.HasPrefix(method, healthPrefix) {
		return ctx, func() {}, true
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.open {
		return ctx, nil, false
	}

	ctx, cancel := context.WithCancel(ctx) // leave (or a Close out of budget) cancels it
	c := &call{cancel: cancel}
	g.calls[c] = struct{}{}

	return ctx, func() { g.leave(c) }, true
}

func (g *Gate) leave(c *call) {
	c.cancel()

	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.calls, c)

	if len(g.calls) == 0 && g.idle != nil {
		close(g.idle)
		g.idle = nil
	}
}

func unavailable() error {
	return status.Error(codes.Unavailable, "service not serving internal API") //nolint:wrapcheck // gRPC status
}

// ServerOptions installs the gate on a gRPC server.
func (g *Gate) ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(func(
			ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler,
		) (any, error) {
			ctx, leave, ok := g.enter(ctx, info.FullMethod)
			if !ok {
				return nil, unavailable()
			}
			defer leave()

			return next(ctx, req)
		}),
		grpc.ChainStreamInterceptor(func(
			srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler,
		) error {
			ctx, leave, ok := g.enter(stream.Context(), info.FullMethod)
			if !ok {
				return unavailable()
			}
			defer leave()

			return next(srv, &gated{ServerStream: stream, ctx: ctx})
		}),
	}
}

// gated is a stream whose context the gate can cancel.
type gated struct {
	grpc.ServerStream

	ctx context.Context //nolint:containedctx // a ServerStream carries its context by contract
}

func (s *gated) Context() context.Context { return s.ctx }
