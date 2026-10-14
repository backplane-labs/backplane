package direct_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/direct"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

// serve runs a gRPC server over an in-memory connection and is a dial of
// it.
func serve(t *testing.T, register func(*grpc.Server)) func(string) (*grpc.ClientConn, error) {
	t.Helper()

	ln := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	register(g)

	go func() { _ = g.Serve(ln) }()

	t.Cleanup(g.Stop)

	return func(string) (*grpc.ClientConn, error) {
		return grpc.NewClient("passthrough:///buf", grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return ln.DialContext(ctx) }))
	}
}

// The SDK's ActivityService runs the env's handlers: the activity's input
// and info reach the handler, its output comes back; a non-retryable
// error, a retryable one, a panic and an unknown activity each have their
// status and detail.
func TestServer(t *testing.T) {
	t.Parallel()

	e := env.New("smtp", manifest.New("smtp", "1.0.0"))

	var info env.ActivityInfo

	e.Activity("Send", func(ctx context.Context, in []byte) ([]byte, error) {
		info, _ = env.ActivityInfoOf(ctx)

		return []byte(`{"got":` + string(in) + `}`), nil
	})
	e.Activity("Reject", func(context.Context, []byte) ([]byte, error) {
		return nil, env.NonRetryableError{Err: errors.New("smtp.Reject: refused")}
	})
	e.Activity("Busy", func(context.Context, []byte) ([]byte, error) { return nil, errors.New("smtp.Busy: later") })
	e.Activity("Boom", func(context.Context, []byte) ([]byte, error) { panic("kaboom") })

	dial := serve(t, func(g *grpc.Server) {
		backplanev1.RegisterActivityServiceServer(g, direct.NewServer("smtp", e, testlog.Discard()))
	})

	conn, err := dial("")
	if err != nil {
		t.Fatal(err)
	}

	client := backplanev1.NewActivityServiceClient(conn)

	res, err := client.Run(t.Context(), &backplanev1.ActivityCall{
		Activity: "smtp.Send", Payload: []byte(`{"to":"a"}`), Binding: "iam.SendEmail@3", Step: "send", Attempt: 2,
		Key: "run-1/send",
	})
	if err != nil || string(res.GetPayload()) != `{"got":{"to":"a"}}` {
		t.Fatalf("%s %v", res.GetPayload(), err)
	}

	if info.Attempt != 2 || info.Binding != "iam.SendEmail@3" || info.Step != "send" || info.Key != "run-1/send" {
		t.Fatalf("info %+v", info)
	}

	for _, tc := range []struct {
		activity  string
		code      codes.Code
		typ       string
		retryable bool
	}{
		{"smtp.Reject", codes.FailedPrecondition, direct.NonRetryableType, false},
		{"smtp.Busy", codes.Unavailable, "", true},
		{"smtp.Boom", codes.Unavailable, direct.PanicType, true},
	} {
		_, err := client.Run(t.Context(), &backplanev1.ActivityCall{Activity: tc.activity, Payload: []byte(`{}`)})
		if status.Code(err) != tc.code {
			t.Errorf("%s: %v", tc.activity, err)
		}

		if typ, retryable := direct.FailureOf(err); typ != tc.typ || retryable != tc.retryable {
			t.Errorf("%s: failure %q retryable %v", tc.activity, typ, retryable)
		}
	}

	for _, activity := range []string{"smtp.Nope", "other.Send", "Send"} {
		_, err := client.Run(t.Context(), &backplanev1.ActivityCall{Activity: activity})
		if status.Code(err) != codes.NotFound {
			t.Errorf("%s: %v", activity, err)
		}
	}
}

// hookServer is backplane's HookService: answers by hook name, records the
// secret and the key it was shown, and can be unreachable.
type hookServer struct {
	consolev1.UnimplementedHookServiceServer

	down   atomic.Bool
	calls  atomic.Int32
	secret atomic.Pointer[string]
	key    atomic.Pointer[string]
}

func (s *hookServer) Call(ctx context.Context, call *backplanev1.HookCall) (*backplanev1.HookResult, error) {
	s.calls.Add(1)

	if s.down.Load() {
		return nil, status.Error(codes.Unavailable, "down")
	}

	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("bp-internal-secret"); len(v) == 1 {
		s.secret.Store(&v[0])
	}

	if v := md.Get(direct.KeyHeader); len(v) == 1 {
		s.key.Store(&v[0])
	}

	switch call.GetHook() {
	case "iam.SendEmail":
		return &backplanev1.HookResult{Payload: []byte(`{"id":"m-1","deadline":` + call.GetDeadline().AsDuration().String() + `}`)}, nil
	case "iam.Missing":
		st, _ := status.New(codes.FailedPrecondition, "no binding for iam.Missing").WithDetails(
			&backplanev1.CallFailure{Type: direct.NoBindingType})

		return nil, st.Err()
	case "iam.Slow":
		<-ctx.Done()

		return nil, status.Error(codes.DeadlineExceeded, "timed out")
	default:
		st, _ := status.New(codes.Aborted, "step send: smtp.Send: refused").WithDetails(
			&backplanev1.CallFailure{Type: "backplane.StepFailed"})

		return nil, st.Err()
	}
}

// The direct hook Caller reaches a replica with the secret and the call's
// key; maps no binding, a failed run, a deadline and no replica to what
// hook.Call promises; skips a replica that is down.
func TestCaller(t *testing.T) {
	t.Parallel()

	a, b := &hookServer{}, &hookServer{}
	a.down.Store(true)

	dialA := serve(t, func(g *grpc.Server) { consolev1.RegisterHookServiceServer(g, a) })
	dialB := serve(t, func(g *grpc.Server) { consolev1.RegisterHookServiceServer(g, b) })

	addrs := []string{"a:1", "b:1"}
	c := direct.NewCaller(direct.CallerParams{
		Service: "iam", Instance: "iam-1", Secret: "s3cret", Timeout: 2 * time.Second,
		Discovery: direct.DiscoveryFunc(func(context.Context) ([]string, error) { return addrs, nil }),
		Dial: func(addr string) (*grpc.ClientConn, error) {
			if addr == "a:1" {
				return dialA(addr)
			}

			return dialB(addr)
		},
	})
	t.Cleanup(c.Close)

	out, err := c.Call(env.WithCallKey(t.Context(), "k-1"), "iam.SendEmail", []byte(`{"to":"a"}`))
	if err != nil || len(out) == 0 {
		t.Fatalf("%s %v", out, err)
	}

	if b.secret.Load() == nil || *b.secret.Load() != "s3cret" || b.key.Load() == nil || *b.key.Load() != "k-1" {
		t.Fatalf("secret %v key %v", b.secret.Load(), b.key.Load())
	}

	if _, err := c.Call(t.Context(), "iam.Missing", nil); !errors.Is(err, env.ErrNoBinding) {
		t.Fatalf("no binding: %v", err)
	}

	if _, err := c.Call(t.Context(), "iam.Other", nil); err == nil || err.Error() != "step send: smtp.Send: refused" {
		t.Fatalf("failed run: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	if _, err := c.Call(ctx, "iam.Slow", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}

	if _, err := c.Call(t.Context(), "other.Hook", nil); err == nil || errors.Is(err, env.ErrUnavailable) {
		t.Fatalf("foreign hook: %v", err)
	}

	addrs = nil

	if _, err := c.Call(t.Context(), "iam.SendEmail", nil); !errors.Is(err, env.ErrUnavailable) {
		t.Fatalf("no replica: %v", err)
	}

	addrs = []string{"a:1"}

	if _, err := c.Call(t.Context(), "iam.SendEmail", nil); !errors.Is(err, env.ErrUnavailable) {
		t.Fatalf("all down: %v", err)
	}
}
