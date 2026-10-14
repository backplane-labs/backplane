package recovery_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/recovery"
)

// sink records log lines.
type sink struct {
	mu    sync.Mutex
	lines []string
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lines = append(s.lines, string(p))

	return len(p), nil
}

func (s *sink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return strings.Join(s.lines, "")
}

func logger() (*xlog.Logger, *sink) {
	s := &sink{}

	return xlog.NewJSON(xlog.WithWriter(s)), s
}

func TestUnaryRecovers(t *testing.T) {
	t.Parallel()

	log, out := logger()
	icpt := recovery.Unary(log, recovery.GRPCPublic)

	res, err := icpt(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/a.A/Boom"},
		func(context.Context, any) (any, error) { panic("boom") })

	if res != nil || status.Code(err) != codes.Internal {
		t.Fatalf("got %v %v", res, err)
	}

	logged := out.String()
	for _, want := range []string{"panic recovered", "boom", "/a.A/Boom", "grpc.public", "recovery_test.go"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log lacks %q: %s", want, logged)
		}
	}

	// A call that does not panic is untouched.
	res, err = icpt(t.Context(), nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { return 1, nil })
	if res != 1 || err != nil {
		t.Fatalf("passthrough: %v %v", res, err)
	}
}

func TestStreamRecovers(t *testing.T) {
	t.Parallel()

	log, out := logger()
	icpt := recovery.Stream(log, recovery.WSProto)

	err := icpt(nil, stream{ctx: t.Context()}, &grpc.StreamServerInfo{FullMethod: "/a.A/S"},
		func(any, grpc.ServerStream) error { panic(errors.New("stream boom")) })

	if status.Code(err) != codes.Internal || !strings.Contains(out.String(), "stream boom") {
		t.Fatalf("got %v, log %s", err, out)
	}

	if len(recovery.ServerOptions(log, recovery.GRPCInternal)) != 2 {
		t.Fatal("server options")
	}
}

type stream struct {
	grpc.ServerStream

	ctx context.Context
}

func (s stream) Context() context.Context { return s.ctx }

func TestHTTPRecovers(t *testing.T) {
	t.Parallel()

	log, out := logger()
	h := recovery.HTTP(log, recovery.HTTPPublic, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("http boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", http.NoBody))

	if rec.Code != http.StatusInternalServerError || !strings.Contains(out.String(), "GET /x") {
		t.Fatalf("code %d, log %s", rec.Code, out)
	}
}

func TestHTTPAbortHandlerPassesThrough(t *testing.T) {
	t.Parallel()

	log, out := logger()
	h := recovery.HTTP(log, recovery.HTTPPlatform, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // the exact sentinel is re-raised
			t.Fatalf("recovered %v", v)
		}

		if out.String() != "" {
			t.Fatalf("abort logged: %s", out)
		}
	}()

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
}
