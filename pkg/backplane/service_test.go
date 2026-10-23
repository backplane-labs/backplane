package backplane_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/gopherex/xlog"

	helloconsolev1 "github.com/backplane-labs/backplane/examples/hello/proto/hello/console/v1"
	hellov1 "github.com/backplane-labs/backplane/examples/hello/proto/hello/v1"
	"github.com/backplane-labs/backplane/pkg/backplane"
	bpapi "github.com/backplane-labs/backplane/pkg/backplane/api"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/standard"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/guard"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

const (
	secret   = "s3cret"
	runLimit = 10 * time.Second
)

var errDown = errors.New("down")

type testConfig struct {
	config.Backplane `json:"backplane"`

	Greeting config.Live[string] `json:"greeting" schemapb:"default=hi"`
}

type testState struct {
	Cfg  testConfig
	Conn deps.Dependency[string]
}

// ports are the platform and public ports of a service under test.
type ports struct{ platform, public string }

func (p ports) platformURL() string { return "http://127.0.0.1:" + p.platform }
func (p ports) publicURL() string   { return "http://127.0.0.1:" + p.public }

// setup points the SDK block at free ports, no drain, the internal secret and
// the given Consul ("" = none).
func setup(t *testing.T, consul string) ports {
	t.Helper()

	p := ports{platform: freePort(t), public: freePort(t)}

	t.Setenv("BACKPLANE_INTERNAL_PORT", p.platform)
	t.Setenv("BACKPLANE_PUBLIC_PORT", p.public)
	t.Setenv("BACKPLANE_INTERNAL_SECRET", secret)
	t.Setenv("BACKPLANE_SHUTDOWN_DRAIN", "0s")
	t.Setenv("BACKPLANE_CONSUL_ADDR", consul)

	return p
}

func baseOptions(name string, extra ...backplane.Option) []backplane.Option {
	return append([]backplane.Option{
		standard.Drivers(), backplane.Name(name), backplane.Instance(name + "-1"), backplane.Advertise("127.0.0.1"),
		backplane.Logger(testlog.Discard()), backplane.ConfigOptions(config.WithoutFile()),
	}, extra...)
}

func freePort(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	_, port, _ := net.SplitHostPort(ln.Addr().String())

	return port
}

func get(url string, header ...string) (int, string) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if len(header) == 2 {
		req.Header.Set(header[0], header[1])
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)

	return res.StatusCode, string(body)
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()

	for deadline := time.Now().Add(runLimit); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}

	t.Fatalf("%s: timed out", what)
}

func ready(p ports) func() bool {
	return func() bool { code, _ := get(p.platformURL() + "/healthz/readiness"); return code == http.StatusOK }
}

type runner interface {
	Run(ctx context.Context) error
}

// run starts Run in the background.
func run(ctx context.Context, svc runner) <-chan error {
	done := make(chan error, 1)

	go func() { done <- svc.Run(ctx) }()

	return done
}

func wait(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(runLimit):
		t.Fatal("Run did not return")

		return nil
	}
}

func dial(t *testing.T, port string) *grpc.ClientConn {
	t.Helper()

	cc, err := grpc.NewClient("127.0.0.1:"+port, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = cc.Close() })

	return cc
}

// TestRunServes: a required dependency holds readiness until it comes up;
// the public route, the guarded UI, gRPC health and Consul follow. Open's
// context is cancelled right after Open: it bounds loading only.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestRunServes(t *testing.T) {
	const name = "svc-e2e"

	consul := os.Getenv("BACKPLANE_TEST_CONSUL")
	p := setup(t, consul)

	var connUp atomic.Bool

	conn := deps.Func(func(context.Context) (string, error) {
		if !connUp.Load() {
			return "", errDown
		}

		return "conn", nil
	})

	openCtx, cancelOpen := context.WithCancel(t.Context())

	svc, err := backplane.Open(openCtx, func(root backplane.Root[testConfig]) (*testState, error) {
		return &testState{
			Cfg:  root.Config(),
			Conn: deps.NewDependency(root, conn, deps.Name("conn"), deps.Backoff(10*time.Millisecond, 20*time.Millisecond)),
		}, nil
	}, baseOptions(name)...)
	if err != nil {
		t.Fatal(err)
	}

	cancelOpen()

	svc.HTTP("/hi", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, svc.State().Cfg.Greeting.Get()+" "+svc.State().Conn.Get())
	}))
	svc.UI(fstest.MapFS{"plugin.json": {Data: []byte(`{"sdk_major":1}`)}})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "platform port", func() bool { code, _ := get(p.platformURL() + "/healthz/liveness"); return code != 0 })

	if ready(p)() {
		t.Fatal("ready before the required dependency")
	}

	connUp.Store(true)
	eventually(t, "ready", ready(p))

	if code, body := get(p.publicURL() + "/hi/"); code != http.StatusOK || body != "hi conn" {
		t.Fatalf("public route: %d %q", code, body)
	}

	if code, _ := get(p.platformURL() + "/_backplane/ui/plugin.json"); code != http.StatusForbidden {
		t.Fatalf("ui without secret: %d", code)
	}

	if code, _ := get(p.platformURL()+"/_backplane/ui/plugin.json", guard.HTTPHeader, secret); code != http.StatusOK {
		t.Fatalf("ui with secret: %d", code)
	}

	res, err := hv1.NewHealthClient(dial(t, p.platform)).Check(ctx, &hv1.HealthCheckRequest{})
	if err != nil || res.GetStatus() != hv1.HealthCheckResponse_SERVING {
		t.Fatalf("grpc health: %v %v", res, err)
	}

	if consul != "" {
		checkConsul(t, consul, name, p)
	}

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}

	if code, _ := get(p.platformURL() + "/healthz/readiness"); code != 0 {
		t.Fatalf("platform port still open: %d", code)
	}
}

// checkConsul: instance state, an unstamped version made unique by the
// manifest hash, and a Live value from KV served in place.
func checkConsul(t *testing.T, addr, name string, p ports) {
	t.Helper()

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = c.KV().DeleteTree("backplane/services/"+name+"/", nil)
		_, _ = c.KV().DeleteTree("config/"+name+"/", nil)
	})

	eventually(t, "instance state", func() bool {
		kv, _, _ := c.KV().Get("backplane/services/"+name+"/instances/"+name+"-1", nil)

		return kv != nil
	})

	keys, _, err := c.KV().Keys("backplane/services/"+name+"/manifests/", "", nil)
	if err != nil || len(keys) != 1 || !strings.HasPrefix(keys[0], "backplane/services/"+name+"/manifests/0.0.0+") {
		t.Fatalf("manifest keys: %v %v", keys, err)
	}

	if _, err := c.KV().Put(&api.KVPair{Key: "config/" + name + "/greeting", Value: []byte("hey")}, nil); err != nil {
		t.Fatal(err)
	}

	eventually(t, "live greeting", func() bool { _, body := get(p.publicURL() + "/hi/"); return body == "hey conn" })
}

type admin struct {
	helloconsolev1.UnimplementedAdminServiceServer
}

func (admin) GetStats(context.Context, *helloconsolev1.GetStatsRequest) (*helloconsolev1.GetStatsResponse, error) {
	return &helloconsolev1.GetStatsResponse{Greetings: 1}, nil
}

// TestInternalAPIGate: the internal API answers Unavailable while the
// author's tree is still starting, and serves behind the secret once it is
// up.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestInternalAPIGate(t *testing.T) {
	p := setup(t, "")

	var connUp atomic.Bool

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*testState, error) {
		conn := deps.Func(func(context.Context) (string, error) {
			if !connUp.Load() {
				return "", errDown
			}

			return "conn", nil
		})

		return &testState{Conn: deps.NewDependency(root, conn, deps.Backoff(10*time.Millisecond, 20*time.Millisecond))}, nil
	}, baseOptions("hello")...)
	if err != nil {
		t.Fatal(err)
	}

	svc.Internal(func(r grpc.ServiceRegistrar) { helloconsolev1.RegisterAdminServiceServer(r, admin{}) })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)
	client := helloconsolev1.NewAdminServiceClient(dial(t, p.platform))
	withSecret := metadata.AppendToOutgoingContext(ctx, guard.Header, secret)
	call := func(ctx context.Context) error {
		ctx, stop := context.WithTimeout(ctx, time.Second)
		defer stop()

		_, err := client.GetStats(ctx, &helloconsolev1.GetStatsRequest{}, grpc.WaitForReady(true))

		return err
	}

	// The platform port is up before the author's tree: calls reach the gate.
	eventually(t, "platform port", func() bool { code, _ := get(p.platformURL() + "/healthz/liveness"); return code != 0 })

	if err := call(withSecret); status.Code(err) != codes.Unavailable ||
		!strings.Contains(status.Convert(err).Message(), "internal API") {
		t.Fatalf("while a dependency retries: %v", err)
	}

	connUp.Store(true)

	eventually(t, "ready", ready(p))

	if err := call(withSecret); err != nil {
		t.Fatalf("with secret once up: %v", err)
	}

	if err := call(ctx); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("without secret: %v", err)
	}

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// operatorAdmin counts a greeting when the call names the console operator
// operatorSession.
type operatorAdmin struct {
	helloconsolev1.UnimplementedAdminServiceServer
}

const operatorSession = "4b1e3f0a-6f2d-4c0e-9a51-0d3c2b7e8f11"

func (operatorAdmin) GetStats(ctx context.Context, _ *helloconsolev1.GetStatsRequest) (*helloconsolev1.GetStatsResponse, error) {
	op, ok := backplane.OperatorOf(ctx)
	if !ok || op.Session != operatorSession || op.Actor() != "console:"+operatorSession {
		return &helloconsolev1.GetStatsResponse{}, nil
	}

	return &helloconsolev1.GetStatsResponse{Greetings: 1}, nil
}

// TestOperatorOf: the internal API sees the console session the relay names,
// and nothing for a call without one or with a malformed one.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestOperatorOf(t *testing.T) {
	p := setup(t, "")

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("hello")...)
	if err != nil {
		t.Fatal(err)
	}

	svc.Internal(func(r grpc.ServiceRegistrar) { helloconsolev1.RegisterAdminServiceServer(r, operatorAdmin{}) })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "ready", ready(p))

	client := helloconsolev1.NewAdminServiceClient(dial(t, p.platform))

	for _, tc := range []struct {
		session string
		want    uint64
	}{{operatorSession, 1}, {"", 0}, {"not-a-uuid", 0}} {
		md := []string{guard.Header, secret}
		if tc.session != "" {
			md = append(md, guard.SessionHeader, tc.session)
		}

		out, err := client.GetStats(metadata.AppendToOutgoingContext(ctx, md...), &helloconsolev1.GetStatsRequest{})
		if err != nil || out.GetGreetings() != tc.want {
			t.Fatalf("session %q: %v, %d greetings", tc.session, err, out.GetGreetings())
		}
	}

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// TestStopWhileDependencyRetries: a stop requested while a required
// dependency is still retrying is not an error, and what was already
// provided is closed.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestStopWhileDependencyRetries(t *testing.T) {
	setup(t, "")

	var provided, closed atomic.Bool

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*testState, error) {
		first := deps.Func(func(context.Context) (string, error) { provided.Store(true); return "a", nil },
			deps.WithClose(func(context.Context, string) error { closed.Store(true); return nil }))
		never := deps.Func(func(context.Context) (string, error) { return "", errDown })

		deps.NewDependency(root, first, deps.Name("first"))

		return &testState{
			Conn: deps.NewDependency(root, never, deps.Name("never"), deps.Backoff(10*time.Millisecond, 20*time.Millisecond)),
		}, nil
	}, baseOptions("svc-stop")...)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "first provided", provided.Load)
	time.Sleep(50 * time.Millisecond) // a few failed attempts of "never"
	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !closed.Load() {
		t.Fatal("provided dependency not closed")
	}
}

//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestDeclareAfterRunPanics(t *testing.T) {
	p := setup(t, "")

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-sealed")...)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "ready", ready(p))

	mustPanic(t, "HTTP after Run", func() { svc.HTTP("/late/", http.NotFoundHandler()) })
	mustPanic(t, "probe after Run", func() { svc.ReadinessProbe(nil) })

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}

	if err := svc.Run(t.Context()); !errors.Is(err, backplane.ErrClosed) {
		t.Fatalf("second Run: %v", err)
	}
}

func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Errorf("%s: no panic", what)
		}
	}()

	fn()
}

// TestCloseWithoutRun: Close releases the configuration (its Consul watch
// goroutines stop), is idempotent, and Run afterwards is ErrClosed.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestCloseWithoutRun(t *testing.T) {
	setup(t, "127.0.0.1:1") // unreachable: the Consul layer keeps retrying in the background

	before := runtime.NumGoroutine()

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-close", backplane.ConfigOptions(config.ConsulBackoff(time.Millisecond, 5*time.Millisecond)))...)
	if err != nil {
		t.Fatal(err)
	}

	if runtime.NumGoroutine() <= before {
		t.Fatal("no configuration goroutines after Open: the test proves nothing")
	}

	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}

	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}

	eventually(t, "goroutines released", func() bool { return runtime.NumGoroutine() <= before })

	if err := svc.Run(t.Context()); !errors.Is(err, backplane.ErrClosed) {
		t.Fatalf("Run after Close: %v", err)
	}

	mustPanic(t, "HTTP after Close", func() { svc.HTTP("/late/", http.NotFoundHandler()) })
}

type greeter struct {
	hellov1.UnimplementedHelloServiceServer
}

// TestDeclarationErrorsFailRun: declaration mistakes surface as Run errors,
// before anything listens.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestDeclarationErrorsFailRun(t *testing.T) {
	registerHello := func(r grpc.ServiceRegistrar) { hellov1.RegisterHelloServiceServer(r, greeter{}) }

	for _, tc := range []struct {
		name    string
		service string
		declare func(svc *backplane.Service[testState])
		want    string
	}{
		{
			name: "duplicate HTTP prefix", service: "svc-dup-http",
			declare: func(svc *backplane.Service[testState]) {
				svc.HTTP("/a", http.NotFoundHandler())
				svc.HTTP("/a/", http.NotFoundHandler())
			},
			want: "prefix served twice",
		},
		{
			name: "duplicate gRPC service", service: "svc-dup-grpc",
			declare: func(svc *backplane.Service[testState]) {
				svc.GRPC(registerHello)
				svc.GRPC(registerHello)
			},
			want: "registered twice",
		},
		{
			name: "internal API in a reserved package", service: "svc-internal",
			declare: func(svc *backplane.Service[testState]) {
				svc.Internal(func(r grpc.ServiceRegistrar) {
					r.RegisterService(&grpc.ServiceDesc{ServiceName: "backplane.console.v1.Fake", HandlerType: (*any)(nil)}, struct{}{})
				})
			},
			want: "backplane.console.v1.Fake: is in a reserved proto package",
		},
		{
			name: "api.MTLS without a mesh", service: "svc-mtls",
			declare: func(svc *backplane.Service[testState]) {
				svc.HTTP("/plain", http.NotFoundHandler())
				svc.HTTP("/mutual", http.NotFoundHandler(), bpapi.MTLS())
			},
			want: "api.MTLS",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := setup(t, "")

			svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
				return &testState{}, nil
			}, baseOptions(tc.service)...)
			if err != nil {
				t.Fatal(err)
			}

			tc.declare(svc)

			// Validate finds it without starting anything.
			if err := svc.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate: %v, want %q", err, tc.want)
			}

			err = wait(t, run(t.Context(), svc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run: %v, want %q", err, tc.want)
			}

			if code, _ := get(p.platformURL() + "/healthz/liveness"); code != 0 {
				t.Fatalf("platform port open after a declaration error: %d", code)
			}
		})
	}
}

// syncBuffer is a log sink safe for concurrent writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// TestSlogRouting: by default Run routes log/slog into the service logger
// and restores the previous default; KeepSlog leaves it alone.
//
//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestSlogRouting(t *testing.T) {
	for _, keep := range []bool{false, true} {
		name := map[bool]string{false: "routed", true: "kept"}[keep]

		t.Run(name, func(t *testing.T) {
			setup(t, "")

			own := slog.New(slog.DiscardHandler)
			prev := slog.Default()

			slog.SetDefault(own)
			t.Cleanup(func() { slog.SetDefault(prev) })

			var sink syncBuffer

			opts := baseOptions("svc-slog-"+name, backplane.Logger(xlog.NewJSON(xlog.WithWriter(&sink))))
			if keep {
				opts = append(opts, backplane.KeepSlog())
			}

			var during *slog.Logger

			started := make(chan struct{})

			svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*testState, error) {
				root.OnStart(func(context.Context) error {
					during = slog.Default()

					slog.Info("through slog")
					close(started)

					return nil
				})

				return &testState{}, nil
			}, opts...)
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			done := run(ctx, svc)

			<-started
			cancel()

			if err := wait(t, done); err != nil {
				t.Fatalf("run: %v", err)
			}

			if routed := strings.Contains(sink.String(), "through slog"); routed == keep || (during == own) != keep {
				t.Fatalf("keep=%v: routed=%v, default during Run was ours=%v", keep, routed, during == own)
			}

			if slog.Default() != own {
				t.Fatal("slog default not restored")
			}
		})
	}
}

func TestOpenRejectsInvalidShutdown(t *testing.T) {
	setup(t, "")
	t.Setenv("BACKPLANE_SHUTDOWN_TIMEOUT", "1s")
	t.Setenv("BACKPLANE_SHUTDOWN_DRAIN", "2s")

	_, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		t.Fatal("constructor called with an invalid configuration")

		return nil, errDown
	}, baseOptions("svc-invalid")...)
	if !errors.Is(err, backplane.ErrConfig) {
		t.Fatalf("drain above timeout: %v", err)
	}
}

func TestOpenRejectsInvalidNATS(t *testing.T) {
	for name, env := range map[string][2]string{
		"dedup above max_age": {"BACKPLANE_NATS_DEDUP_WINDOW", "2h"},
		"negative max_age":    {"BACKPLANE_NATS_MAX_AGE", "-1s"},
		"negative dlq":        {"BACKPLANE_NATS_DLQ_MAX_AGE", "-1s"},
		"zero dedup":          {"BACKPLANE_NATS_DEDUP_WINDOW", "0s"},
	} {
		t.Run(name, func(t *testing.T) {
			setup(t, "")
			t.Setenv("BACKPLANE_NATS_URL", "nats://127.0.0.1:1")
			t.Setenv("BACKPLANE_NATS_MAX_AGE", "1h")
			t.Setenv(env[0], env[1])

			_, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
				t.Fatal("constructor called with an invalid configuration")

				return nil, errDown
			}, baseOptions("svc-invalid")...)
			if !errors.Is(err, backplane.ErrConfig) {
				t.Fatalf("want ErrConfig, got %v", err)
			}
		})
	}
}

//nolint:paralleltest // t.Setenv: the SDK block reads the process environment
func TestOpenFailsOnConstructorError(t *testing.T) {
	setup(t, "")

	_, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return nil, errDown
	}, baseOptions("svc-fail")...)
	if !errors.Is(err, errDown) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenRejectsInvalidHistogramRule(t *testing.T) {
	setup(t, "")
	t.Setenv("BACKPLANE_TELEMETRY_METRICS_HISTOGRAMS", `[{"instrument":"*","aggregation":"explicit","boundaries":[2,1]}]`)

	_, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		t.Fatal("constructor called with an invalid configuration")

		return nil, errDown
	}, baseOptions("svc-histogram")...)
	if !errors.Is(err, backplane.ErrConfig) || !strings.Contains(err.Error(), "strictly increase") {
		t.Fatalf("decreasing boundaries: %v", err)
	}
}
