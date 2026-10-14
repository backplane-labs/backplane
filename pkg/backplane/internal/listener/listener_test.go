package listener_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/listener"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

// group mirrors a node: goroutines run on a context cancelled at stop, and
// the stop waits for them.
type group struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu   sync.Mutex
	errs []error
}

func newGroup(t *testing.T) *group {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	return &group{ctx: ctx, cancel: cancel}
}

func (g *group) Go(fn func(context.Context) error) {
	g.wg.Go(func() {
		if err := fn(g.ctx); err != nil {
			g.mu.Lock()
			g.errs = append(g.errs, err)
			g.mu.Unlock()
		}
	})
}

// stop stops l as its node would and returns how long the node took.
func (g *group) stop(l *listener.Listener) (time.Duration, error) {
	start := time.Now()

	g.cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := l.Stop(ctx)

	g.wg.Wait()

	g.mu.Lock()
	defer g.mu.Unlock()

	return time.Since(start), errors.Join(append(g.errs, err)...)
}

func start(t *testing.T, h http.Handler) (*listener.Listener, *group) {
	t.Helper()

	srv := grpc.NewServer()
	hv1.RegisterHealthServer(srv, health.NewServer())

	l := listener.New("test", "127.0.0.1:0", srv, h, testlog.Discard(), listener.Limits{})
	g := newGroup(t)

	if err := l.Start(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	return l, g
}

func get(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)

	return string(body), err
}

func healthCheck(ctx context.Context, addr string, opts ...grpc.CallOption) error {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()

	out, err := hv1.NewHealthClient(conn).Check(ctx, &hv1.HealthCheckRequest{}, opts...)
	if err != nil {
		return err
	}

	if out.GetStatus() != hv1.HealthCheckResponse_SERVING {
		return errors.New(out.GetStatus().String())
	}

	return nil
}

func TestGRPCAndHTTPOnOnePort(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") })

	l, g := start(t, mux)

	body, err := get(t.Context(), "http://"+l.Addr()+"/ping")
	if err != nil || body != "pong" {
		t.Fatalf("http: %q %v", body, err)
	}

	if err := healthCheck(t.Context(), l.Addr()); err != nil {
		t.Fatalf("grpc: %v", err)
	}

	// content-type application/grpc+proto is gRPC too.
	if err := healthCheck(t.Context(), l.Addr(), grpc.CallContentSubtype("proto")); err != nil {
		t.Fatalf("grpc+proto: %v", err)
	}

	if _, err := g.stop(l); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if _, err := get(t.Context(), "http://"+l.Addr()+"/ping"); err == nil {
		t.Fatal("still serving after stop")
	}
}

// Stop lets a request in flight finish before the socket closes.
func TestStopDrainsInFlight(t *testing.T) {
	t.Parallel()

	entered, release := make(chan struct{}), make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release

		_, _ = io.WriteString(w, "done")
	})

	l, g := start(t, mux)

	type result struct {
		body string
		err  error
	}

	got := make(chan result, 1)

	go func() {
		body, err := get(context.Background(), "http://"+l.Addr()+"/slow")
		got <- result{body, err}
	}()

	<-entered

	stopped := make(chan error, 1)

	go func() {
		_, err := g.stop(l)
		stopped <- err
	}()

	select {
	case err := <-stopped:
		t.Fatalf("stop returned with a request in flight: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	if r := <-got; r.err != nil || r.body != "done" {
		t.Fatalf("in-flight request: %q %v", r.body, r.err)
	}

	if err := <-stopped; err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// A client that connects and sends nothing must not hold the stop for the
// classification timeout.
func TestIdleConnectionDoesNotDelayStop(t *testing.T) {
	t.Parallel()

	l, g := start(t, http.NotFoundHandler())

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", l.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Let cmux accept it and start sniffing.
	time.Sleep(50 * time.Millisecond)

	took, err := g.stop(l)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}

	if took > time.Second {
		t.Fatalf("stop took %v with an idle connection open", took)
	}
}

// The HTTP limits apply: headers above MaxHeaderBytes get 431.
func TestLimits(t *testing.T) {
	t.Parallel()

	srv := grpc.NewServer()
	l := listener.New("limits", "127.0.0.1:0", srv, http.NotFoundHandler(), testlog.Discard(),
		listener.Limits{ReadHeaderTimeout: time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 1024})
	g := newGroup(t)

	if err := l.Start(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+l.Addr()+"/", http.NoBody)
	req.Header.Set("X-Big", strings.Repeat("x", 8<<10))

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = res.Body.Close()

	if res.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("status %d", res.StatusCode)
	}

	if _, err := g.stop(l); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// A stream open past the stop budget is cut and the stop says so.
func TestStopCutsStreamsAfterBudget(t *testing.T) {
	t.Parallel()

	l, g := start(t, nil)

	conn, err := grpc.NewClient(l.Addr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	watch, err := hv1.NewHealthClient(conn).Watch(t.Context(), &hv1.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := watch.Recv(); err != nil {
		t.Fatal(err)
	}

	g.cancel()

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	begin := time.Now()

	if err := l.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop: want deadline exceeded, got %v", err)
	}

	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("stop took %v", took)
	}

	if _, err := watch.Recv(); err == nil {
		t.Fatal("stream still open after a hard stop")
	}

	g.wg.Wait()
}
