package backplane_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"
	rpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"

	"github.com/gopherex/ws-proto/wsrpc"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	helloconsolev1 "github.com/gopherex/backplane/examples/hello/proto/hello/console/v1"
	hellov1 "github.com/gopherex/backplane/examples/hello/proto/hello/v1"
	"github.com/gopherex/backplane/pkg/backplane"
	"github.com/gopherex/backplane/pkg/backplane/api"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/drivers/standard"
	"github.com/gopherex/backplane/pkg/backplane/internal/guard"
	"github.com/gopherex/backplane/pkg/backplane/wsproto"
)

// hello greets, panics on "panic", and streams until the call ends.
type hello struct {
	hellov1.UnimplementedHelloServiceServer

	streaming chan struct{} // receives when a Countdown starts
}

func (hello) Greet(_ context.Context, in *hellov1.GreetRequest) (*hellov1.GreetResponse, error) {
	if in.GetName() == "panic" {
		panic("greet boom")
	}

	return &hellov1.GreetResponse{Greeting: "hi " + in.GetName()}, nil
}

func (h hello) Countdown(_ *hellov1.CountdownRequest, s grpc.ServerStreamingServer[hellov1.CountdownResponse]) error {
	if err := s.Send(&hellov1.CountdownResponse{Left: 1}); err != nil {
		return err
	}

	if h.streaming != nil {
		h.streaming <- struct{}{}
	}

	<-s.Context().Done()

	return s.Context().Err()
}

// tags records which interceptors saw a call.
type tags struct {
	mu   sync.Mutex
	seen []string
}

func (tg *tags) unary(name string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		tg.mu.Lock()
		tg.seen = append(tg.seen, name)
		tg.mu.Unlock()

		return next(ctx, req)
	}
}

func (tg *tags) take() []string {
	tg.mu.Lock()
	defer tg.mu.Unlock()

	out := tg.seen
	tg.seen = nil

	return out
}

func fastHealth(t *testing.T) {
	t.Helper()

	t.Setenv("BACKPLANE_HEALTH_INTERVAL", "100ms")
	t.Setenv("BACKPLANE_HEALTH_TIMEOUT", "100ms")
}

func getHost(url, host string) (int, string, http.Header) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	req.Host = host

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error(), nil
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)

	return res.StatusCode, string(body), res.Header
}

func text(s string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, s) })
}

// TestServerFeatures: panics become errors on every server, author
// interceptors wrap only their registration, routes match host and prefix,
// middleware wraps its route, public gRPC serves health and reflection, the
// platform port serves info and pprof behind the secret.
func TestServerFeatures(t *testing.T) {
	p := setup(t, "")
	fastHealth(t)
	t.Setenv("BACKPLANE_PPROF", "true")

	seen := &tags{}

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-features", backplane.GRPCServerOptions(grpc.ChainUnaryInterceptor(seen.unary("global"))))...)
	if err != nil {
		t.Fatal(err)
	}

	svc.GRPC(func(r grpc.ServiceRegistrar) { hellov1.RegisterHelloServiceServer(r, hello{}) },
		api.Interceptors(seen.unary("hello")), api.Reflection())
	svc.GRPC(func(r grpc.ServiceRegistrar) { helloconsolev1.RegisterAdminServiceServer(r, admin{}) },
		api.Interceptors(seen.unary("admin")))
	wsproto.Serve(svc, "/ws/", api.AnyOrigin(),
		func(r grpc.ServiceRegistrar) { hellov1.RegisterHelloServiceServer(r, hello{}) },
		api.Interceptors(seen.unary("ws")))
	svc.HTTP("/boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("http boom") }))
	svc.HTTP("/api", text("a"), api.Host("a.example.com"))
	svc.HTTP("/api", text("b"), api.Host("*.b.example.com"))
	svc.HTTP("/mw", text("mw"), api.Middleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Middleware", "on")
			next.ServeHTTP(w, r)
		})
	}))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "ready", ready(p))

	checkGRPC(ctx, t, p, seen)
	checkWS(ctx, t, p, seen)
	checkHTTP(t, p)
	checkPlatform(t, p)

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func checkGRPC(ctx context.Context, t *testing.T, p ports, seen *tags) {
	t.Helper()

	cc := dial(t, p.public)
	greeter := hellov1.NewHelloServiceClient(cc)

	if _, err := greeter.Greet(ctx, &hellov1.GreetRequest{Name: "panic"}); status.Code(err) != codes.Internal {
		t.Fatalf("panicking call: %v", err)
	}

	if res, err := greeter.Greet(ctx, &hellov1.GreetRequest{Name: "x"}); err != nil || res.GetGreeting() != "hi x" {
		t.Fatalf("greet after a panic: %v %v", res, err)
	}

	if got := seen.take(); !slices.Equal(got, []string{"global", "hello", "global", "hello"}) {
		t.Fatalf("hello interceptors: %v", got)
	}

	if _, err := helloconsolev1.NewAdminServiceClient(cc).GetStats(ctx, &helloconsolev1.GetStatsRequest{}); err != nil {
		t.Fatal(err)
	}

	if got := seen.take(); !slices.Equal(got, []string{"global", "admin"}) {
		t.Fatalf("admin interceptors: %v", got)
	}

	res, err := hv1.NewHealthClient(cc).Check(ctx, &hv1.HealthCheckRequest{})
	if err != nil || res.GetStatus() != hv1.HealthCheckResponse_SERVING {
		t.Fatalf("public health: %v %v", res, err)
	}

	seen.take()

	refl, err := rpb.NewServerReflectionClient(cc).ServerReflectionInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := refl.Send(&rpb.ServerReflectionRequest{
		MessageRequest: &rpb.ServerReflectionRequest_ListServices{},
	}); err != nil {
		t.Fatal(err)
	}

	list, err := refl.Recv()
	if err != nil {
		t.Fatal(err)
	}

	services := list.GetListServicesResponse().GetService()
	names := make([]string, 0, len(services))

	for _, s := range services {
		names = append(names, s.GetName())
	}

	if !slices.Contains(names, "hello.v1.HelloService") {
		t.Fatalf("reflection lists %v", names)
	}

	_ = refl.CloseSend()
}

func checkWS(ctx context.Context, t *testing.T, p ports, seen *tags) {
	t.Helper()

	cc, err := wsrpc.Dial(ctx, "ws://127.0.0.1:"+p.public+"/ws/")
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()

	call := func(name string) (*hellov1.GreetResponse, error) {
		s, err := cc.NewStream(ctx, hellov1.HelloService_Greet_FullMethodName, nil)
		if err != nil {
			return nil, err
		}

		if err := s.Send(&hellov1.GreetRequest{Name: name}); err != nil {
			return nil, err
		}

		_ = s.CloseSend()

		var out hellov1.GreetResponse

		return &out, s.Recv(&out)
	}

	seen.take()

	if _, err := call("panic"); status.Code(err) != codes.Internal {
		t.Fatalf("ws panicking call: %v", err)
	}

	if res, err := call("w"); err != nil || res.GetGreeting() != "hi w" {
		t.Fatalf("ws greet: %v %v", res, err)
	}

	if got := seen.take(); !slices.Equal(got, []string{"ws", "ws"}) {
		t.Fatalf("ws interceptors: %v", got)
	}
}

func checkHTTP(t *testing.T, p ports) {
	t.Helper()

	if code, _, _ := getHost(p.publicURL()+"/boom/", ""); code != http.StatusInternalServerError {
		t.Fatalf("panicking handler: %d", code)
	}

	if !ready(p)() {
		t.Fatal("not serving after a panic")
	}

	for host, want := range map[string]string{"a.example.com": "a", "x.b.example.com": "b"} {
		if code, body, _ := getHost(p.publicURL()+"/api/", host); code != http.StatusOK || body != want {
			t.Fatalf("host %s: %d %q", host, code, body)
		}
	}

	if code, _, _ := getHost(p.publicURL()+"/api/", "other.org"); code != http.StatusNotFound {
		t.Fatalf("unmatched host: %d", code)
	}

	if code, body, h := getHost(p.publicURL()+"/mw/", ""); code != http.StatusOK || body != "mw" ||
		h.Get("X-Middleware") != "on" {
		t.Fatalf("middleware: %d %q %v", code, body, h)
	}
}

func checkPlatform(t *testing.T, p ports) {
	t.Helper()

	if code, _ := get(p.platformURL() + "/_backplane/info"); code != http.StatusForbidden {
		t.Fatalf("info without secret: %d", code)
	}

	code, body := get(p.platformURL()+"/_backplane/info", guard.HTTPHeader, secret)
	if code != http.StatusOK {
		t.Fatalf("info: %d %s", code, body)
	}

	var info backplane.Info
	if err := json.Unmarshal([]byte(body), &info); err != nil || info.Service != "svc-features" ||
		info.Instance != "svc-features-1" || info.GoVersion == "" || info.SDKVersion == "" {
		t.Fatalf("info: %+v %v", info, err)
	}

	if pcode, _ := get(p.platformURL() + "/debug/pprof/"); pcode != http.StatusForbidden {
		t.Fatalf("pprof without secret: %d", pcode)
	}

	if pcode, _ := get(p.platformURL()+"/debug/pprof/", guard.HTTPHeader, secret); pcode != http.StatusOK {
		t.Fatalf("pprof: %d", pcode)
	}
}

// TestPprofOffByDefault: without BACKPLANE_PPROF there is no pprof.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestPprofOffByDefault(t *testing.T) {
	p := setup(t, "")

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-nopprof")...)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "ready", ready(p))

	if code, _ := get(p.platformURL()+"/debug/pprof/", guard.HTTPHeader, secret); code != http.StatusNotFound {
		t.Fatalf("pprof: %d", code)
	}

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}

// stopper is a component whose stop blocks until released.
type stopper struct {
	deps.Component

	entered chan struct{}
	release chan struct{}
	left    atomic.Int64 // stop budget left when its stop began
}

func newStopper(root deps.Scope) *stopper {
	s := &stopper{
		Component: deps.NewComponent(root, "stopper"), entered: make(chan struct{}), release: make(chan struct{}),
	}
	s.OnStop(func(ctx context.Context) error {
		if dl, ok := ctx.Deadline(); ok {
			s.left.Store(int64(time.Until(dl)))
		}

		close(s.entered)

		select {
		case <-s.release:
		case <-ctx.Done():
		}

		return nil
	})

	return s
}

type stopState struct{ Stopper *stopper }

// TestSecondSignalExits: the first signal stops the service; one more
// during the stop exits at once with status 1. A signal during a stop that
// began for another reason exits too.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestSecondSignalExits(t *testing.T) {
	for name, first := range map[string]func(sigs chan os.Signal, cancel context.CancelFunc){
		"second signal":           func(sigs chan os.Signal, _ context.CancelFunc) { sigs <- syscall.SIGTERM },
		"signal after ctx cancel": func(_ chan os.Signal, cancel context.CancelFunc) { cancel() },
	} {
		t.Run(name, func(t *testing.T) {
			p := setup(t, "")

			sigs := make(chan os.Signal, 2)
			exited := make(chan int, 1)

			svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*stopState, error) {
				return &stopState{Stopper: newStopper(root)}, nil
			}, baseOptions("svc-signals", backplane.WithSignals(sigs),
				backplane.WithExit(func(code int) { exited <- code }))...)
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			done := run(ctx, svc)

			eventually(t, "ready", ready(p))

			first(sigs, cancel)
			<-svc.State().Stopper.entered

			select {
			case <-exited:
				t.Fatal("exited on the first stop request")
			default:
			}

			sigs <- syscall.SIGINT

			select {
			case code := <-exited:
				if code != 1 {
					t.Fatalf("exit code %d", code)
				}
			case <-time.After(runLimit):
				t.Fatal("no exit on a signal during stop")
			}

			close(svc.State().Stopper.release)

			if err := wait(t, done); err != nil {
				t.Fatalf("run: %v", err)
			}
		})
	}
}

// TestShutdownBudgets: a stream still open when the listeners' budget ends
// is cut, and the author's tree still gets the reserve.
func TestShutdownBudgets(t *testing.T) {
	p := setup(t, "")
	t.Setenv("BACKPLANE_SHUTDOWN_TIMEOUT", "5s")
	t.Setenv("BACKPLANE_SHUTDOWN_LISTENERS", "300ms")
	t.Setenv("BACKPLANE_SHUTDOWN_RESERVE", "2s")

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*stopState, error) {
		return &stopState{Stopper: newStopper(root)}, nil
	}, baseOptions("svc-budgets")...)
	if err != nil {
		t.Fatal(err)
	}

	streaming := make(chan struct{}, 1)

	svc.GRPC(func(r grpc.ServiceRegistrar) {
		hellov1.RegisterHelloServiceServer(r, hello{streaming: streaming})
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "ready", ready(p))

	stream, err := hellov1.NewHelloServiceClient(dial(t, p.public)).Countdown(t.Context(), &hellov1.CountdownRequest{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}

	<-streaming

	begin := time.Now()

	cancel()
	<-svc.State().Stopper.entered

	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("listeners held the stop for %v with a 300ms budget", took)
	}

	if left := time.Duration(svc.State().Stopper.left.Load()); left < 2*time.Second {
		t.Fatalf("the tree got %v, want at least the 2s reserve", left)
	}

	close(svc.State().Stopper.release)

	if _, err := stream.Recv(); err == nil {
		t.Fatal("stream survived the listeners' budget")
	}

	if err := wait(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run: want the cut stream reported, got %v", err)
	}
}

// TestOpenValidation: what the SDK block and the name must satisfy.
func TestOpenValidation(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		opts []backplane.Option
		want string
	}{
		"name":              {opts: []backplane.Option{standard.Drivers(), backplane.Name("Bad_Name")}, want: "service name"},
		"name digit":        {opts: []backplane.Option{standard.Drivers(), backplane.Name("1svc")}, want: "service name"},
		"stages over total": {env: map[string]string{"BACKPLANE_SHUTDOWN_TIMEOUT": "5s"}, want: "listeners + reserve"},
		"no listeners":      {env: map[string]string{"BACKPLANE_SHUTDOWN_LISTENERS": "0s"}, want: "shutdown.listeners"},
		"negative reserve":  {env: map[string]string{"BACKPLANE_SHUTDOWN_RESERVE": "-1s"}, want: "shutdown.reserve"},
		"health timeout": {
			env: map[string]string{"BACKPLANE_HEALTH_INTERVAL": "1s", "BACKPLANE_HEALTH_TIMEOUT": "2s"}, want: "health.timeout",
		},
		"health interval": {env: map[string]string{"BACKPLANE_HEALTH_INTERVAL": "0s"}, want: "health.interval"},
		"require nats":    {opts: []backplane.Option{backplane.RequireNATS()}, want: "RequireNATS"},
		"require temporal": {
			opts: []backplane.Option{backplane.RequireTemporal()}, want: "RequireTemporal",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			setup(t, "")
			t.Setenv("BACKPLANE_NATS_URL", "")
			t.Setenv("BACKPLANE_TEMPORAL_ADDR", "")

			for k, v := range c.env {
				t.Setenv(k, v)
			}

			_, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
				return &testState{}, nil
			}, append(baseOptions("svc-validate"), c.opts...)...)
			if !errors.Is(err, backplane.ErrConfig) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want ErrConfig about %q, got %v", c.want, err)
			}
		})
	}
}

// TestRequireTransports: a required transport that is down keeps the
// instance unready; a live one (BACKPLANE_TEST_NATS, _TEMPORAL) lets it
// serve.
func TestRequireTransports(t *testing.T) {
	cases := map[string]struct {
		env  string
		live string
		opt  backplane.Option
	}{
		"nats":     {"BACKPLANE_NATS_URL", natsURL(os.Getenv("BACKPLANE_TEST_NATS")), backplane.RequireNATS()},
		"temporal": {"BACKPLANE_TEMPORAL_ADDR", os.Getenv("BACKPLANE_TEST_TEMPORAL"), backplane.RequireTemporal()},
	}
	for name, c := range cases {
		t.Run(name+" down", func(t *testing.T) {
			p := setup(t, "")
			fastHealth(t)
			t.Setenv(c.env, "127.0.0.1:"+freePort(t))

			requireServes(t, p, c.opt, false)
		})

		t.Run(name+" live", func(t *testing.T) {
			if c.live == "" {
				t.Skip("BACKPLANE_TEST_" + strings.ToUpper(name) + " not set")
			}

			p := setup(t, "")
			fastHealth(t)
			t.Setenv(c.env, c.live)

			requireServes(t, p, c.opt, true)
		})
	}
}

// natsURL of a BACKPLANE_TEST_NATS address (host:port); "" stays "".
func natsURL(addr string) string {
	if addr == "" {
		return ""
	}

	return "nats://" + addr
}

func requireServes(t *testing.T, p ports, opt backplane.Option, want bool) {
	t.Helper()

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-require", opt)...)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "platform port", func() bool { code, _ := get(p.platformURL() + "/healthz/liveness"); return code != 0 })

	if want {
		eventually(t, "ready", ready(p))
	} else {
		time.Sleep(500 * time.Millisecond)

		if ready(p)() {
			t.Fatal("ready with the required transport down")
		}
	}

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// TestSystemNodeOrder: the connections and the hook worker precede the
// author's tree, so components publish and call hooks from OnStart.
func TestSystemNodeOrder(t *testing.T) {
	setup(t, "")
	t.Setenv("BACKPLANE_NATS_URL", "nats://127.0.0.1:"+freePort(t))
	t.Setenv("BACKPLANE_TEMPORAL_ADDR", "127.0.0.1:"+freePort(t))

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-order")...)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	got := backplane.SystemNodes(svc)

	at := slices.Index(got, "nats")
	if at < slices.Index(got, "platform") || !slices.Equal(got[at:], []string{"nats", "temporal", "hooks", "svc-order"}) {
		t.Fatalf("nodes %v: want platform ... nats, temporal, hooks, then the author's tree", got)
	}
}

type statusState struct {
	Store deps.Dependency[string]
	Cache deps.Optional[string]
}

// TestInstanceStatuses: the instance state lists every dependency of the
// author's tree with its readiness, and the configured transports.
func TestInstanceStatuses(t *testing.T) {
	p := setup(t, "")
	fastHealth(t)
	t.Setenv("BACKPLANE_NATS_URL", "nats://127.0.0.1:"+freePort(t))

	var storeUp atomic.Bool

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*statusState, error) {
		store := deps.Func(func(context.Context) (string, error) {
			if !storeUp.Load() {
				return "", errDown
			}

			return "db", nil
		})
		cache := deps.Func(func(context.Context) (string, error) { return "c", nil })

		return &statusState{
			Store: deps.NewDependency(root, store, deps.Name("store"), deps.Backoff(10*time.Millisecond, 20*time.Millisecond)),
			Cache: deps.NewOptional(root, cache, deps.Name("cache")),
		}, nil
	}, baseOptions("svc-status")...)
	if err != nil {
		t.Fatal(err)
	}

	byPath := func() map[string]*backplanev1.NodeStatus {
		out := map[string]*backplanev1.NodeStatus{}
		for _, n := range backplane.NodeStatuses(svc) {
			out[n.GetPath()] = n
		}

		return out
	}

	if st := byPath()["store"]; st == nil || st.GetReady() || st.GetError() == "" {
		t.Fatalf("store before start: %v", st)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	storeUp.Store(true)
	eventually(t, "ready", ready(p))
	eventually(t, "both reported ready", func() bool {
		nodes := byPath()

		return len(nodes) == 2 && nodes["store"].GetReady() && nodes["cache"].GetReady()
	})

	transports := backplane.TransportStatuses(svc)
	if len(transports) != 1 || transports[0].GetName() != "nats" || transports[0].GetConnected() {
		t.Fatalf("transports: %v", transports)
	}

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}
