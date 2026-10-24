package backplane_test

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gopherex/ws-proto/wsrpc"

	hellov1 "github.com/backplane-labs/backplane/examples/hello/proto/hello/v1"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/api"
	"github.com/backplane-labs/backplane/pkg/backplane/wsproto"
)

type userKey struct{}

// gateway is a connection-oriented ws-proto endpoint: it admits a
// connection by its token, keeps its identity in the connection's context
// and can break it, and sees every call on it.
type gateway struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	calls  []string
	closed chan struct{}
}

func (g *gateway) connection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get("Authorization")
		if user == "" {
			http.Error(w, "no token", http.StatusUnauthorized)

			return
		}

		ctx, cancel := context.WithCancel(context.WithValue(r.Context(), userKey{}, user))
		defer cancel()

		g.mu.Lock()
		g.cancel = cancel
		g.mu.Unlock()

		next.ServeHTTP(w, r.WithContext(ctx))
		close(g.closed)
	})
}

func (g *gateway) call(next wsrpc.Handler) wsrpc.Handler {
	return func(ctx context.Context, s *wsrpc.Stream) error {
		user, _ := ctx.Value(userKey{}).(string)

		g.mu.Lock()
		g.calls = append(g.calls, user+" "+s.Method())
		g.mu.Unlock()

		return next(ctx, s)
	}
}

func (g *gateway) breakConnection() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.cancel()
}

func (g *gateway) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Clone(g.calls)
}

// greet is a ws-proto handler registered on the server directly, as
// generated Register<Service>Handler functions do.
func greet(ctx context.Context, s *wsrpc.Stream) error {
	var in hellov1.GreetRequest
	if err := s.Recv(&in); err != nil {
		return err
	}

	if in.GetName() == "panic" {
		panic("ws boom")
	}

	user, _ := ctx.Value(userKey{}).(string)

	return s.Send(&hellov1.GreetResponse{Greeting: "hi " + in.GetName() + " from " + user})
}

//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestWSProtoConnections(t *testing.T) {
	p := setup(t, "")

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-gateway")...)
	if err != nil {
		t.Fatal(err)
	}

	gate := &gateway{closed: make(chan struct{})}
	wsproto.Serve(svc, "/ws/", api.AnyOrigin(),
		func(s *wsrpc.Server) { s.Register(hellov1.HelloService_Greet_FullMethodName, greet) },
		api.Middleware(gate.connection), api.CallMiddleware(gate.call))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "ready", ready(p))

	url := "ws://127.0.0.1:" + p.public + "/ws/"
	if _, err := wsrpc.Dial(ctx, url); err == nil {
		t.Fatal("connection without a token admitted")
	}

	cc, err := wsrpc.Dial(ctx, url, wsrpc.WithHeader(http.Header{"Authorization": {"u1"}}))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()

	call := func(name string) (string, error) {
		s, err := cc.NewStream(ctx, hellov1.HelloService_Greet_FullMethodName, nil)
		if err != nil {
			return "", err
		}

		if err := s.Send(&hellov1.GreetRequest{Name: name}); err != nil {
			return "", err
		}

		_ = s.CloseSend()

		var out hellov1.GreetResponse

		err = s.Recv(&out)

		return out.GetGreeting(), err
	}

	if _, err := call("panic"); status.Code(err) != codes.Internal {
		t.Fatalf("panicking call: %v", err)
	}

	if got, err := call("w"); err != nil || got != "hi w from u1" {
		t.Fatalf("greet: %q %v", got, err)
	}

	method := "u1 " + hellov1.HelloService_Greet_FullMethodName
	if got := gate.seen(); !slices.Equal(got, []string{method, method}) {
		t.Fatalf("calls seen: %v", got)
	}

	gate.breakConnection()

	select {
	case <-gate.closed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	if _, err := call("after"); err == nil {
		t.Fatal("call on a broken connection")
	}

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
}
