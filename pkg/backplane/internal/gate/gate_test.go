package gate_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/gate"
)

const (
	unaryMethod  = "/test.Gated/Unary"
	streamMethod = "/test.Gated/Stream"
	settle       = 50 * time.Millisecond
)

// blocker is a gated service whose calls report entry and wait for release.
type blocker struct {
	entered chan struct{}
	release chan struct{}
}

// wait blocks until release or until the call's context ends.
func (b *blocker) wait(ctx context.Context) error {
	b.entered <- struct{}{}

	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	}
}

func desc() *grpc.ServiceDesc {
	return &grpc.ServiceDesc{
		ServiceName: "test.Gated",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Unary",
			Handler: func(
				srv any, ctx context.Context, dec func(any) error, icpt grpc.UnaryServerInterceptor,
			) (any, error) {
				in := &emptypb.Empty{}
				if err := dec(in); err != nil {
					return nil, err
				}

				h := func(ctx context.Context, _ any) (any, error) {
					if err := srv.(*blocker).wait(ctx); err != nil {
						return nil, err
					}

					return &emptypb.Empty{}, nil
				}

				return icpt(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: unaryMethod}, h)
			},
		}},
		Streams: []grpc.StreamDesc{{
			StreamName:    "Stream",
			ServerStreams: true,
			Handler: func(srv any, ss grpc.ServerStream) error {
				return srv.(*blocker).wait(ss.Context())
			},
		}},
	}
}

type fixture struct {
	gate *gate.Gate
	svc  *blocker
	conn *grpc.ClientConn
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	g := gate.New()
	svc := &blocker{entered: make(chan struct{}, 4), release: make(chan struct{})}

	srv := grpc.NewServer(g.ServerOptions()...)
	srv.RegisterService(desc(), svc)
	hv1.RegisterHealthServer(srv, health.NewServer())

	ln := bufconn.Listen(1 << 20)

	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return ln.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	return &fixture{gate: g, svc: svc, conn: conn}
}

func (f *fixture) unary(ctx context.Context) error {
	return f.conn.Invoke(ctx, unaryMethod, &emptypb.Empty{}, &emptypb.Empty{})
}

func (f *fixture) stream(ctx context.Context) error {
	s, err := f.conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, streamMethod)
	if err != nil {
		return err
	}

	if err := s.SendMsg(&emptypb.Empty{}); err != nil {
		return err
	}

	if err := s.CloseSend(); err != nil {
		return err
	}

	if err := s.RecvMsg(&emptypb.Empty{}); !errors.Is(err, io.EOF) {
		return err
	}

	return nil
}

func TestClosedGateRejects(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	if err := f.unary(t.Context()); status.Code(err) != codes.Unavailable {
		t.Fatalf("unary before open: %v", err)
	}

	if err := f.stream(t.Context()); status.Code(err) != codes.Unavailable {
		t.Fatalf("stream before open: %v", err)
	}

	out, err := hv1.NewHealthClient(f.conn).Check(t.Context(), &hv1.HealthCheckRequest{})
	if err != nil || out.GetStatus() != hv1.HealthCheckResponse_SERVING {
		t.Fatalf("health must not be gated: %v %v", out, err)
	}
}

func TestCloseWaitsInFlight(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]func(*fixture, context.Context) error{
		"unary":  (*fixture).unary,
		"stream": (*fixture).stream,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			f.gate.Open()

			callErr := make(chan error, 1)

			go func() { callErr <- call(f, t.Context()) }()

			<-f.svc.entered

			closed := make(chan error, 1)

			go func() { closed <- f.gate.Close(t.Context()) }()

			select {
			case err := <-closed:
				t.Fatalf("close returned with a call in flight: %v", err)
			case <-time.After(settle):
			}

			if err := f.unary(t.Context()); status.Code(err) != codes.Unavailable {
				t.Fatalf("new call while closing: %v", err)
			}

			close(f.svc.release)

			if err := <-closed; err != nil {
				t.Fatalf("close: %v", err)
			}

			if err := <-callErr; err != nil {
				t.Fatalf("in-flight call: %v", err)
			}
		})
	}
}

// A Close that runs out of budget cancels the calls still in flight: a
// long stream ends instead of outliving the tree.
func TestCloseTimesOutCancelsInFlight(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]func(*fixture, context.Context) error{
		"unary":  (*fixture).unary,
		"stream": (*fixture).stream,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			f.gate.Open()

			callErr := make(chan error, 1)

			go func() { callErr <- call(f, t.Context()) }()

			<-f.svc.entered

			ctx, cancel := context.WithTimeout(t.Context(), settle)
			defer cancel()

			if err := f.gate.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("want deadline exceeded, got %v", err)
			}

			select {
			case err := <-callErr:
				if status.Code(err) != codes.Canceled {
					t.Fatalf("in-flight call: want Canceled, got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("in-flight call not cancelled")
			}
		})
	}
}

func TestConcurrentCloseBothReturn(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.gate.Open()

	callErr := make(chan error, 1)

	go func() { callErr <- f.unary(t.Context()) }()

	<-f.svc.entered

	closed := make(chan error, 2)

	for range 2 {
		go func() { closed <- f.gate.Close(t.Context()) }()
	}

	time.Sleep(settle)
	close(f.svc.release)

	for range 2 {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatalf("close: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("a concurrent Close never returned")
		}
	}

	if err := <-callErr; err != nil {
		t.Fatalf("in-flight call: %v", err)
	}
}

func TestCloseIdleReturnsAtOnce(t *testing.T) {
	t.Parallel()

	g := gate.New()
	g.Open()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := g.Close(ctx); err != nil {
		t.Fatalf("idle close: %v", err)
	}
}
