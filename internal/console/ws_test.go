package console_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/gopherex/ws-proto/wsrpc"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	helloconsolev1 "github.com/backplane-labs/backplane/examples/hello/proto/hello/console/v1"
	"github.com/backplane-labs/backplane/internal/console"
	"github.com/backplane-labs/backplane/internal/registry"
)

const waitFor = 5 * time.Second

func code(err error) codes.Code {
	if err == nil {
		return codes.OK
	}

	return wsrpc.FromError(err).Code
}

func TestUpgradeRequiresSessionAndOrigin(t *testing.T) {
	t.Parallel()

	e := newEnv(t, nil, func(s *console.Settings) { s.Origins = []string{"https://console.example.com"} })
	c := e.mustLogin()

	for name, tc := range map[string]struct {
		cookie *http.Cookie
		origin string
	}{
		"no cookie":        {nil, "https://console.example.com"},
		"no origin":        {c, ""},
		"foreign origin":   {c, "https://evil.example.com"},
		"own host, listed": {c, e.origin()}, // Origins replace the default
		"forged cookie":    {&http.Cookie{Name: console.CookieName, Value: "forged"}, "https://console.example.com"},
	} {
		if _, err := e.dial(tc.cookie, tc.origin); err == nil {
			t.Fatalf("%s: upgraded", name)
		}
	}

	if _, err := e.dial(c, "https://console.example.com"); err != nil {
		t.Fatalf("listed origin: %v", err)
	}

	// Without Origins: the request's own host only.
	own := newEnv(t, nil)
	ownCookie := own.mustLogin()

	if _, err := own.dial(ownCookie, "https://console.example.com"); err == nil {
		t.Fatal("other origin upgraded")
	}

	own.mustDial(ownCookie)
}

// Behind a reverse proxy Host arrives without its port (strip_any_host_port)
// and the port comes in X-Forwarded-Port: the console still knows its own
// origin on a non-default port, and only that one.
func TestOwnOriginBehindProxy(t *testing.T) {
	t.Parallel()

	e := newEnv(t, nil)

	login := func(origin, port string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost/auth/login",
			strings.NewReader(`{"token":"`+adminToken+`"}`))
		req.Host = "localhost"
		req.Header.Set("Origin", origin)

		if port != "" {
			req.Header.Set("X-Forwarded-Port", port)
		}

		rec := httptest.NewRecorder()
		e.console.Handler().ServeHTTP(rec, req)

		return rec.Code
	}

	for name, tc := range map[string]struct {
		origin, port string
		want         int
	}{
		"forwarded port":         {"http://localhost:10000", "10000", http.StatusOK},
		"default port":           {"https://localhost", "443", http.StatusOK},
		"no forwarded port":      {"http://localhost:10000", "", http.StatusForbidden},
		"another port":           {"http://localhost:9999", "10000", http.StatusForbidden},
		"another host, its port": {"http://evil.example.com:10000", "10000", http.StatusForbidden},
	} {
		if got := login(tc.origin, tc.port); got != tc.want {
			t.Errorf("%s: login %d, want %d", name, got, tc.want)
		}
	}
}

func manifest(name, version string, internal ...string) *backplanev1.Manifest {
	return &backplanev1.Manifest{Service: name, Version: version, InternalServices: internal}
}

func instance(id, version, addr string, port int, phase backplanev1.InstancePhase, healthy bool) registry.Instance {
	return registry.Instance{
		ID: id, Registered: healthy, Healthy: healthy, Address: addr,
		State: &backplanev1.InstanceState{
			Id: id, Version: version, Address: addr, PlatformPort: uint32(port), Phase: phase,
		},
	}
}

const serving = backplanev1.InstancePhase_INSTANCE_PHASE_SERVING

func TestCatalogService(t *testing.T) {
	t.Parallel()

	e := newEnv(t, nil)
	withUI := manifest("billing", "1.2.0")
	withUI.Ui = &backplanev1.UI{Hash: "abc", SdkMajor: 1}

	e.hub.Publish(map[string]registry.Service{
		"billing": {
			Name: "billing",
			Manifests: map[string]*backplanev1.Manifest{
				"1.2.0": withUI, "1.10.0": manifest("billing", "1.10.0"),
			},
			Instances: []registry.Instance{
				instance("billing-b", "1.2.0", "10.0.0.2", 9400, serving, true),
				instance("billing-a", "1.2.0", "10.0.0.1", 9400, serving, false),
			},
		},
	})

	cc := e.mustDial(e.mustLogin())
	ctx := t.Context()

	var list consolev1.ListServicesResponse
	if err := call(ctx, cc, "/backplane.console.v1.CatalogService/ListServices", &consolev1.ListServicesRequest{}, &list); err != nil {
		t.Fatal(err)
	}

	s := list.GetServices()[0]
	if len(list.GetServices()) != 1 || s.GetName() != "billing" || s.GetLatestVersion() != "1.2.0" ||
		s.GetInstances() != 2 || s.GetHealthy() != 1 || s.GetHealth() != consolev1.ServiceHealth_SERVICE_HEALTH_DEGRADED ||
		!s.GetUi() || s.GetVersions()[0] != "1.10.0" {
		t.Fatalf("summary: %v", s)
	}

	var got consolev1.GetServiceResponse
	if err := call(ctx, cc, "/backplane.console.v1.CatalogService/GetService",
		&consolev1.GetServiceRequest{Name: "billing"}, &got); err != nil {
		t.Fatal(err)
	}

	if got.GetLatest().GetVersion() != "1.2.0" || len(got.GetManifests()) != 2 ||
		got.GetInstances()[0].GetId() != "billing-a" || got.GetInstances()[0].GetState().GetPlatformPort() != 9400 {
		t.Fatalf("service: %v", &got)
	}

	err := call(ctx, cc, "/backplane.console.v1.CatalogService/GetService",
		&consolev1.GetServiceRequest{Name: "nope"}, &got)
	if code(err) != codes.NotFound {
		t.Fatalf("unknown service: %v", err)
	}

	var plugins consolev1.ListPluginsResponse
	if err := call(ctx, cc, "/backplane.console.v1.CatalogService/ListPlugins", &consolev1.ListPluginsRequest{}, &plugins); err != nil {
		t.Fatal(err)
	}

	if p := plugins.GetPlugins(); len(p) != 1 || p[0].GetPath() != "/plugins/billing/abc/" || !p[0].GetAvailable() || p[0].GetSdkMajor() != 1 {
		t.Fatalf("plugins: %v", p)
	}

	// Watch: the current summary, then one per change.
	wctx, cancel := context.WithTimeout(ctx, waitFor)
	defer cancel()

	ws, err := cc.NewStream(wctx, "/backplane.console.v1.CatalogService/WatchCatalog", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := ws.Send(&consolev1.WatchCatalogRequest{}); err != nil {
		t.Fatal(err)
	}

	_ = ws.CloseSend()

	var w consolev1.WatchCatalogResponse
	if err := ws.Recv(&w); err != nil || len(w.GetServices()) != 1 {
		t.Fatalf("first: %v %v", &w, err)
	}

	e.hub.Publish(map[string]registry.Service{})

	if err := ws.Recv(&w); err != nil || len(w.GetServices()) != 0 {
		t.Fatalf("after change: %v %v", &w, err)
	}
}

func TestSessionService(t *testing.T) {
	t.Parallel()

	e := newEnv(t, nil)
	mine, other := e.mustLogin(), e.mustLogin()
	cc, otherConn := e.mustDial(mine), e.mustDial(other)
	ctx := t.Context()

	var list consolev1.ListSessionsResponse
	if err := call(ctx, cc, "/backplane.console.v1.SessionService/ListSessions", &consolev1.ListSessionsRequest{}, &list); err != nil {
		t.Fatal(err)
	}

	var current, others int

	for _, s := range list.GetSessions() {
		if s.GetCurrent() {
			current++
		} else {
			others++
		}
	}

	if current != 1 || others != 1 {
		t.Fatalf("sessions: %v", list.GetSessions())
	}

	// RevokeOtherSessions ends every session but the caller's.
	third := e.mustLogin()
	thirdConn := e.mustDial(third)

	var revoked consolev1.RevokeOtherSessionsResponse
	if err := call(ctx, cc, "/backplane.console.v1.SessionService/RevokeOtherSessions",
		&consolev1.RevokeOtherSessionsRequest{}, &revoked); err != nil {
		t.Fatal(err)
	}

	if revoked.GetRevokedSessions() != 2 {
		t.Fatalf("revoke others: %v", &revoked)
	}

	// The other sessions' connections are closed; this one lives on.
	closed(t, otherConn)
	closed(t, thirdConn)

	if res := e.request(http.MethodGet, "/auth/session", nil, "Cookie", cookieHeader(other)); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session: %s", res.Status)
	}

	if err := call(ctx, cc, "/backplane.console.v1.SessionService/ListSessions", &consolev1.ListSessionsRequest{}, &list); err != nil {
		t.Fatalf("own connection after revoking the others: %v", err)
	}

	if len(list.GetSessions()) != 1 || !list.GetSessions()[0].GetCurrent() {
		t.Fatalf("after revoking the others: %v", list.GetSessions())
	}

	// RevokeSession ends one.
	e.mustLogin()

	if err := call(ctx, cc, "/backplane.console.v1.SessionService/ListSessions", &consolev1.ListSessionsRequest{}, &list); err != nil {
		t.Fatal(err)
	}

	var id string

	for _, s := range list.GetSessions() {
		if !s.GetCurrent() {
			id = s.GetId()
		}
	}

	if err := call(ctx, cc, "/backplane.console.v1.SessionService/RevokeSession",
		&consolev1.RevokeSessionRequest{Id: id}, &consolev1.RevokeSessionResponse{}); err != nil {
		t.Fatal(err)
	}

	err := call(ctx, cc, "/backplane.console.v1.SessionService/RevokeSession",
		&consolev1.RevokeSessionRequest{Id: id}, &consolev1.RevokeSessionResponse{})
	if code(err) != codes.NotFound {
		t.Fatalf("revoke twice: %v", err)
	}
}

// closed waits until calls on cc fail.
func closed(t *testing.T, cc *wsrpc.ClientConn) {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		err := call(t.Context(), cc, "/backplane.console.v1.CatalogService/ListServices",
			&consolev1.ListServicesRequest{}, &consolev1.ListServicesResponse{})
		if err != nil {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("connection still open")
}

// admin is a fake of hello's internal API that records what it was sent.
type admin struct {
	helloconsolev1.UnimplementedAdminServiceServer

	md atomic.Pointer[metadata.MD]
}

func (a *admin) GetStats(ctx context.Context, _ *helloconsolev1.GetStatsRequest) (*helloconsolev1.GetStatsResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	a.md.Store(&md)

	if v := md.Get("x-fail"); len(v) > 0 {
		return nil, status.Error(codes.FailedPrecondition, "asked to fail")
	}

	_ = grpc.SetHeader(ctx, metadata.Pairs("x-served-by", "fake"))

	return &helloconsolev1.GetStatsResponse{Greetings: 7, CurrentGreeting: "hi"}, nil
}

// platform serves a fake platform port: AdminService and grpc.health.v1
// (a server stream) behind nothing.
func platform(t *testing.T) (*admin, int) {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := grpc.NewServer()
	a := &admin{}
	helloconsolev1.RegisterAdminServiceServer(srv, a)
	hv1.RegisterHealthServer(srv, health.NewServer())

	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(srv.Stop)

	tcp, _ := ln.Addr().(*net.TCPAddr)

	return a, tcp.Port
}

func TestRelay(t *testing.T) {
	t.Parallel()

	fake, port := platform(t)
	e := newEnv(t, nil)
	e.hub.Publish(map[string]registry.Service{
		"hello": {
			Name: "hello",
			Manifests: map[string]*backplanev1.Manifest{
				"1.0.0": manifest("hello", "1.0.0", "hello.console.v1.AdminService", "grpc.health.v1.Health"),
			},
			Instances: []registry.Instance{
				instance("hello-1", "1.0.0", "127.0.0.1", port, serving, true),
				// Not serving: never picked.
				instance("hello-2", "1.0.0", "127.0.0.1", 1, backplanev1.InstancePhase_INSTANCE_PHASE_STARTING, true),
			},
		},
	})

	cc := e.mustDial(e.mustLogin())
	ctx := t.Context()

	s, err := cc.NewStream(ctx, "/hello.console.v1.AdminService/GetStats", map[string]string{
		"authorization": "Bearer x", "bp-internal-secret": "forged", "bp-console-session": "forged", "x-custom": "kept",
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = s.Send(&helloconsolev1.GetStatsRequest{})
	_ = s.CloseSend()

	var res helloconsolev1.GetStatsResponse
	if err := s.Recv(&res); err != nil || res.GetGreetings() != 7 {
		t.Fatalf("relay: %v %v", &res, err)
	}

	if s.Header()["x-served-by"] != "fake" {
		t.Fatalf("response header: %v", s.Header())
	}

	sent := *fake.md.Load()
	if got := sent.Get(console.SecretHeader); len(got) != 1 || got[0] != "relay-secret" {
		t.Fatalf("secret: %v", got)
	}

	if got := sent.Get(console.SessionHeader); len(got) != 1 || got[0] == "forged" || got[0] == "" {
		t.Fatalf("session: %v", got)
	}

	if len(sent.Get("authorization")) != 0 || sent.Get("x-custom")[0] != "kept" {
		t.Fatalf("metadata: %v", sent)
	}

	// The service's own error status comes through.
	fs, _ := cc.NewStream(ctx, "/hello.console.v1.AdminService/GetStats", map[string]string{"x-fail": "1"})
	_ = fs.Send(&helloconsolev1.GetStatsRequest{})
	_ = fs.CloseSend()

	if err := fs.Recv(&res); code(err) != codes.FailedPrecondition {
		t.Fatalf("upstream status: %v", err)
	}

	// Server streaming.
	wctx, cancel := context.WithTimeout(ctx, waitFor)
	defer cancel()

	watch, err := cc.NewStream(wctx, "/grpc.health.v1.Health/Watch", nil)
	if err != nil {
		t.Fatal(err)
	}

	_ = watch.Send(&hv1.HealthCheckRequest{})
	_ = watch.CloseSend()

	var healthy hv1.HealthCheckResponse
	if err := watch.Recv(&healthy); err != nil || healthy.GetStatus() != hv1.HealthCheckResponse_SERVING {
		t.Fatalf("stream: %v %v", &healthy, err)
	}

	// Not declared by any manifest.
	err = call(ctx, cc, "/other.console.v1.AdminService/GetStats", &helloconsolev1.GetStatsRequest{}, &res)
	if code(err) != codes.PermissionDenied {
		t.Fatalf("undeclared: %v", err)
	}

	// Declared, but no instance serves it.
	e.hub.Publish(map[string]registry.Service{
		"hello": {Name: "hello", Manifests: map[string]*backplanev1.Manifest{
			"1.0.0": manifest("hello", "1.0.0", "hello.console.v1.AdminService"),
		}},
	})

	err = call(ctx, cc, "/hello.console.v1.AdminService/GetStats", &helloconsolev1.GetStatsRequest{}, &res)
	if code(err) != codes.Unavailable {
		t.Fatalf("no instance: %v", err)
	}
}

func TestConsoleKeepsAvailableVersionDuringRollout(t *testing.T) {
	t.Parallel()
	oldServer, oldPort := platform(t)
	nextServer, nextPort := platform(t)
	e := newEnv(t, nil)
	old := manifest("hello", "1.0.0", "hello.console.v1.AdminService")
	old.Ui = &backplanev1.UI{Hash: "old", SdkMajor: 0}
	next := manifest("hello", "2.0.0", "other.console.v1.AdminService")
	next.Ui = &backplanev1.UI{Hash: "next", SdkMajor: 0}
	cc := e.mustDial(e.mustLogin())

	for _, phase := range []backplanev1.InstancePhase{
		backplanev1.InstancePhase_INSTANCE_PHASE_STARTING,
		backplanev1.InstancePhase_INSTANCE_PHASE_STOPPING,
		backplanev1.InstancePhase_INSTANCE_PHASE_SERVING,
	} {
		candidate := instance("next", "2.0.0", "127.0.0.1", nextPort, phase, false)
		e.hub.Publish(map[string]registry.Service{"hello": {
			Name: "hello", Manifests: map[string]*backplanev1.Manifest{"1.0.0": old, "2.0.0": next},
			Instances: []registry.Instance{instance("old", "1.0.0", "127.0.0.1", oldPort, serving, true), candidate},
		}})

		var service consolev1.GetServiceResponse
		if err := call(t.Context(), cc, consolev1.CatalogService_GetService_FullMethodName, &consolev1.GetServiceRequest{Name: "hello"}, &service); err != nil || service.GetLatest().GetVersion() != "1.0.0" {
			t.Fatalf("rollout catalog: %v %v", &service, err)
		}

		var plugins consolev1.ListPluginsResponse
		if err := call(t.Context(), cc, consolev1.CatalogService_ListPlugins_FullMethodName, &consolev1.ListPluginsRequest{}, &plugins); err != nil || len(plugins.GetPlugins()) != 1 || plugins.GetPlugins()[0].GetHash() != "old" || !plugins.GetPlugins()[0].GetAvailable() {
			t.Fatalf("rollout plugin: %v %v", &plugins, err)
		}

		if err := call(t.Context(), cc, "/hello.console.v1.AdminService/GetStats", &helloconsolev1.GetStatsRequest{}, &helloconsolev1.GetStatsResponse{}); err != nil {
			t.Fatalf("old API disappeared during rollout: %v", err)
		}
	}

	if oldServer.md.Load() == nil || nextServer.md.Load() != nil {
		t.Fatal("relay picked unavailable new replica")
	}

	readyNext := instance("next", "2.0.0", "127.0.0.1", nextPort, serving, true)
	e.hub.Publish(map[string]registry.Service{"hello": {
		Name: "hello", Manifests: map[string]*backplanev1.Manifest{"1.0.0": old, "2.0.0": next},
		Instances: []registry.Instance{readyNext},
	}})

	var plugins consolev1.ListPluginsResponse
	if err := call(t.Context(), cc, consolev1.CatalogService_ListPlugins_FullMethodName, &consolev1.ListPluginsRequest{}, &plugins); err != nil || plugins.GetPlugins()[0].GetHash() != "next" {
		t.Fatalf("ready rollout: %v %v", &plugins, err)
	}

	readyNext.Healthy = false
	e.hub.Publish(map[string]registry.Service{"hello": {
		Name: "hello", Manifests: map[string]*backplanev1.Manifest{"2.0.0": next}, Instances: []registry.Instance{readyNext},
	}})
	plugins.Reset()

	if err := call(t.Context(), cc, consolev1.CatalogService_ListPlugins_FullMethodName, &consolev1.ListPluginsRequest{}, &plugins); err != nil || plugins.GetPlugins()[0].GetAvailable() {
		t.Fatalf("unhealthy plugin reported available: %v %v", &plugins, err)
	}
}

func TestRelayRejectsAmbiguousOwnership(t *testing.T) {
	t.Parallel()

	for _, firstAvailable := range []bool{false, true} {
		t.Run(strconv.FormatBool(firstAvailable), func(t *testing.T) {
			t.Parallel()
			fake, port := platform(t)
			e := newEnv(t, nil)
			services := map[string]registry.Service{}

			for _, name := range []string{"alpha", "zeta"} {
				svc := registry.Service{Name: name, Manifests: map[string]*backplanev1.Manifest{
					"1.0.0": manifest(name, "1.0.0", "hello.console.v1.AdminService"),
				}}
				if name == "zeta" || firstAvailable {
					svc.Instances = []registry.Instance{instance(name+"-1", "1.0.0", "127.0.0.1", port, serving, true)}
				}

				services[name] = svc
			}

			e.hub.Publish(services)
			cc := e.mustDial(e.mustLogin())

			err := call(t.Context(), cc, "/hello.console.v1.AdminService/GetStats", &helloconsolev1.GetStatsRequest{}, &helloconsolev1.GetStatsResponse{})
			if code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "alpha, zeta") {
				t.Fatalf("ambiguous owner: %v", err)
			}

			if fake.md.Load() != nil {
				t.Fatal("ambiguous call reached an upstream")
			}
		})
	}
}

// bundle is a fake platform port serving /_backplane/ui/ like the SDK.
func bundle(t *testing.T, hash string, hits *atomic.Int64) int {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)

		if r.Header.Get(console.SecretHTTPHeader) != "relay-secret" {
			http.Error(w, "internal secret required", http.StatusForbidden)

			return
		}

		if r.URL.Path != "/_backplane/ui/remoteEntry.js" {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("ETag", `"`+hash+`"`)
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = io.WriteString(w, "export default 1;")
	}))
	t.Cleanup(srv.Close)

	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(p)

	return port
}

func TestPluginBundles(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	port := bundle(t, "h1", &hits)
	e := newEnv(t, nil)
	m := manifest("hello", "1.0.0")
	m.Ui = &backplanev1.UI{Hash: "h1", SdkMajor: 1}
	e.hub.Publish(map[string]registry.Service{
		"hello": {
			Name: "hello", Manifests: map[string]*backplanev1.Manifest{"1.0.0": m},
			Instances: []registry.Instance{instance("hello-1", "1.0.0", "127.0.0.1", port, serving, true)},
		},
	})

	c := e.mustLogin()
	get := func(p string, header ...string) *http.Response {
		return e.request(http.MethodGet, p, nil, append([]string{"Cookie", cookieHeader(c)}, header...)...)
	}

	if res := e.request(http.MethodGet, "/plugins/hello/h1/remoteEntry.js", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a session: %s", res.Status)
	}

	first := get("/plugins/hello/h1/remoteEntry.js")
	body, _ := io.ReadAll(first.Body)

	if first.StatusCode != http.StatusOK || string(body) != "export default 1;" ||
		first.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || first.Header.Get("ETag") != `"h1"` ||
		first.Header.Get("Content-Type") != "text/javascript" {
		t.Fatalf("bundle: %s %q %v", first.Status, body, first.Header)
	}

	// Cached: the instance is not asked again; If-None-Match is 304.
	if res := get("/plugins/hello/h1/remoteEntry.js"); res.StatusCode != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("cached: %s, %d fetches", res.Status, hits.Load())
	}

	if res := get("/plugins/hello/h1/remoteEntry.js", "If-None-Match", `"h1"`); res.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidation: %s", res.Status)
	}

	for p, want := range map[string]int{
		"/plugins/hello/h1/missing.js":      http.StatusNotFound, // the instance has no such file
		"/plugins/hello/other/remote.js":    http.StatusNotFound, // no instance serves that hash
		"/plugins/nobody/h1/remoteEntry.js": http.StatusNotFound,
	} {
		if res := get(p); res.StatusCode != want {
			t.Fatalf("%s: %s", p, res.Status)
		}
	}
}

// A stream the browser cancels ends (and its upstream call with it).
func TestRelayCancel(t *testing.T) {
	t.Parallel()

	_, port := platform(t)
	e := newEnv(t, nil)
	e.hub.Publish(map[string]registry.Service{
		"hello": {Name: "hello", Manifests: map[string]*backplanev1.Manifest{
			"1.0.0": manifest("hello", "1.0.0", "grpc.health.v1.Health"),
		}, Instances: []registry.Instance{instance("hello-1", "1.0.0", "127.0.0.1", port, serving, true)}},
	})

	cc := e.mustDial(e.mustLogin())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	watch, err := cc.NewStream(ctx, "/grpc.health.v1.Health/Watch", nil)
	if err != nil {
		t.Fatal(err)
	}

	_ = watch.Send(&hv1.HealthCheckRequest{})
	_ = watch.CloseSend()

	var healthy hv1.HealthCheckResponse
	if err := watch.Recv(&healthy); err != nil {
		t.Fatal(err)
	}

	cancel()

	if err := watch.Recv(&healthy); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("after cancel: %v", err)
	}
}
