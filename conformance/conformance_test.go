// Package conformance checks the SDK contract from outside, over the network
// and Consul only: it builds examples/hello, runs it against
// platform-in-a-box (make up) and asserts what any SDK, in any language,
// must produce. Requires BACKPLANE_TEST_CONSUL (e.g. localhost:8500).
package conformance_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	hv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/gopherex/ws-proto/wsrpc"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	helloconsolev1 "github.com/gopherex/backplane/examples/hello/proto/hello/console/v1"
	hellov1 "github.com/gopherex/backplane/examples/hello/proto/hello/v1"
)

// The service is named hello: its internal API lives in hello.console.v1,
// and the SDK accepts internal services only from <service>.console.v1.
const (
	service  = "hello"
	instance = "conformance-hello-1"
	secret   = "conformance-secret"
	version  = "9.9.9"
)

type env struct {
	consul   *api.Client
	host     string
	platform string // host:port
	public   string // host:port
	cmd      *exec.Cmd
	logs     *bytes.Buffer
}

// advertise is an address of this host that a Consul agent running in a
// container can reach: the source address of the default route.
func advertise(t *testing.T) string {
	t.Helper()

	conn, err := net.Dial("udp", "192.0.2.1:9") // TEST-NET-1: nothing is sent
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}

	return host
}

func freePort(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	return port
}

// start builds hello with the stamped identity and runs it.
func start(t *testing.T) *env {
	t.Helper()

	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set (make up)")
	}

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	// The run owns every key of the service: refuse to wipe a live one.
	if keys, _, err := c.KV().Keys("backplane/services/"+service+"/instances/", "", nil); err == nil && len(keys) > 0 {
		t.Fatalf("a %s instance is live on this Consul (%v): stop it before running conformance", service, keys)
	}

	cleanup := func() {
		_, _ = c.KV().DeleteTree("backplane/services/"+service+"/", nil)
		_, _ = c.KV().DeleteTree("config/"+service+"/", nil)
	}
	cleanup()
	t.Cleanup(cleanup)

	bin := filepath.Join(t.TempDir(), "hello")
	pkg := "github.com/gopherex/backplane/pkg/backplane/build"

	build := exec.Command("go", "build", "-o", bin,
		"-ldflags", "-X "+pkg+".Service="+service+" -X "+pkg+".Version="+version,
		"../examples/hello/cmd/hello")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build hello: %v\n%s", err, out)
	}

	platformPort, publicPort, host := freePort(t), freePort(t), advertise(t)
	e := &env{consul: c, host: host, platform: host + ":" + platformPort, public: host + ":" + publicPort, logs: &bytes.Buffer{}}
	e.cmd = exec.Command(bin)

	e.cmd.Env = append(os.Environ(),
		"BACKPLANE_CONSUL_ADDR="+addr,
		"BACKPLANE_INSTANCE="+instance,
		"BACKPLANE_ADVERTISE="+host,
		"BACKPLANE_INTERNAL_PORT="+platformPort,
		"BACKPLANE_PUBLIC_PORT="+publicPort,
		"BACKPLANE_INTERNAL_SECRET="+secret,
		"BACKPLANE_SHUTDOWN_DRAIN=100ms",
		"HELLO_GREETER_SUFFIX=?",
		"HELLO_LEGACY_LISTEN=:"+freePort(t),
	)

	e.cmd.Stdout, e.cmd.Stderr = e.logs, e.logs
	if err := e.cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		// Wait also joins the output copying, so the log is complete and
		// no longer written. A test that already waited gets an error here.
		_ = e.cmd.Process.Kill()
		_ = e.cmd.Wait()

		if t.Failed() {
			t.Logf("hello logs:\n%s", e.logs)
		}
	})
	e.eventually(t, "readiness", func() bool {
		code, _ := httpGet("http://" + e.platform + "/healthz/readiness")
		return code == http.StatusOK
	})

	return e
}

func (e *env) eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("%s: not reached", what)
}

func httpGet(url string, header ...string) (int, string) {
	req, err := http.NewRequest(http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, err.Error()
	}

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

func dial(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// TestContract runs the contract as one ordered scenario against a single
// hello process: each step builds on the state the previous one left.
//
//nolint:paralleltest // one process, ordered steps
func TestContract(t *testing.T) {
	e := start(t)
	for _, step := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{"manifest in KV", e.manifest},
		{"instance state and catalog", e.state},
		{"platform port", e.platformPort},
		{"public protocols", e.protocols},
		{"hot reload from KV", e.hotReload},
		{"graceful stop", e.gracefulStop},
	} {
		if !t.Run(step.name, step.run) {
			return
		}
	}
}

func (e *env) manifest(t *testing.T) {
	t.Helper()

	kv, _, err := e.consul.KV().Get("backplane/services/"+service+"/manifests/"+version, nil)
	if err != nil || kv == nil {
		t.Fatalf("manifest: %v", err)
	}

	var m backplanev1.Manifest
	if err := proto.Unmarshal(kv.Value, &m); err != nil {
		t.Fatal(err)
	}

	if m.GetService() != service || m.GetVersion() != version {
		t.Fatalf("identity: %s@%s", m.GetService(), m.GetVersion())
	}

	kinds := map[backplanev1.ApiKind]bool{}
	for _, r := range m.GetApis() {
		kinds[r.GetKind()] = true
		if k := r.GetKind(); k != backplanev1.ApiKind_API_KIND_HTTP && k != backplanev1.ApiKind_API_KIND_GRAPHQL &&
			len(r.GetServices()) == 0 {
			t.Errorf("route %v without services", r)
		}
	}

	// gRPC, Connect, ws-proto and internal services share one descriptor set.
	described := describedServices(t, m.GetDescriptors())
	for _, name := range []string{"hello.v1.HelloService", "hello.console.v1.AdminService"} {
		if !described[name] {
			t.Errorf("descriptors lack %s", name)
		}
	}

	for _, k := range []backplanev1.ApiKind{
		backplanev1.ApiKind_API_KIND_GRPC, backplanev1.ApiKind_API_KIND_WS_PROTO,
		backplanev1.ApiKind_API_KIND_HTTP, backplanev1.ApiKind_API_KIND_GRAPHQL,
	} {
		if !kinds[k] {
			t.Errorf("no %v route", k)
		}
	}

	if len(m.GetInternalServices()) != 1 || m.GetInternalServices()[0] != "hello.console.v1.AdminService" {
		t.Errorf("internal services: %v", m.GetInternalServices())
	}

	if len(m.GetHooks()) != 1 || !m.GetHooks()[0].GetRequired() || len(m.GetActivities()) != 2 || len(m.GetEvents()) != 1 {
		t.Errorf("hooks %v activities %v events %v", m.GetHooks(), m.GetActivities(), m.GetEvents())
	}

	if m.GetConfig().GetSchema() == nil || !slices.Contains(m.GetConfig().GetLive(), "greeter.suffix") || len(m.GetUi().GetHash()) != 64 {
		t.Errorf("config %v or ui missing", m.GetConfig().GetLive())
	}

	paths := make([]string, 0, len(m.GetNodes()))
	for _, n := range m.GetNodes() {
		paths = append(paths, n.GetPath())
		if n.GetOptional() != (n.GetPath() == "cache") {
			t.Errorf("node %s optional=%v", n.GetPath(), n.GetOptional())
		}
	}

	want := []string{"store", "cache", "greeter", "greeter/templates", "audit", "flows", "admin", "legacy"}
	if !slices.Equal(paths, want) {
		t.Errorf("nodes %v, want %v", paths, want)
	}
}

func describedServices(t *testing.T, raw []byte) map[string]bool {
	t.Helper()

	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &set); err != nil {
		t.Fatalf("descriptors: %v", err)
	}

	out := map[string]bool{}

	for _, f := range set.GetFile() {
		for _, svc := range f.GetService() {
			out[f.GetPackage()+"."+svc.GetName()] = true
		}
	}

	return out
}

func (e *env) state(t *testing.T) {
	t.Helper()

	kv, _, err := e.consul.KV().Get("backplane/services/"+service+"/instances/"+instance, nil)
	if err != nil || kv == nil || kv.Session == "" {
		t.Fatalf("state: %v %v", kv, err)
	}

	var st backplanev1.InstanceState
	if err := proto.Unmarshal(kv.Value, &st); err != nil {
		t.Fatal(err)
	}

	if st.GetAddress() != e.host || strconv.Itoa(int(st.GetPlatformPort())) != strings.Split(e.platform, ":")[1] {
		t.Errorf("state address: %v", &st)
	}

	if st.GetSources()["greeter.suffix"] != backplanev1.ConfigSource_CONFIG_SOURCE_ENV {
		t.Errorf("sources: %v", st.GetSources())
	}

	e.eventually(t, "consul check passing", func() bool {
		entries, _, err := e.consul.Health().Service(service, "", true, nil)
		return err == nil && len(entries) == 1
	})
}

func (e *env) platformPort(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	conn := dial(t, e.platform)
	if res, err := hv1.NewHealthClient(conn).Check(ctx, &hv1.HealthCheckRequest{}); err != nil || res.GetStatus() != hv1.HealthCheckResponse_SERVING {
		t.Errorf("grpc health open: %v %v", res, err)
	}

	admin := helloconsolev1.NewAdminServiceClient(conn)
	if _, err := admin.GetStats(ctx, &helloconsolev1.GetStatsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("internal API without secret: %v", err)
	}

	withSecret := metadata.AppendToOutgoingContext(ctx, "bp-internal-secret", secret)
	if _, err := admin.GetStats(withSecret, &helloconsolev1.GetStatsRequest{}); err != nil {
		t.Errorf("internal API with secret: %v", err)
	}

	if code, _ := httpGet("http://" + e.platform + "/_backplane/ui/plugin.json"); code != http.StatusForbidden {
		t.Errorf("ui without secret: %d", code)
	}

	if code, _ := httpGet("http://"+e.platform+"/_backplane/ui/plugin.json", "Bp-Internal-Secret", secret); code != http.StatusOK {
		t.Errorf("ui with secret: %d", code)
	}

	e.bundleCaching(t)
}

// bundleCaching: the bundle is served with ETag = the manifest's ui.hash
// and Cache-Control: no-cache; the matching If-None-Match gets 304.
func (e *env) bundleCaching(t *testing.T) {
	t.Helper()

	kv, _, err := e.consul.KV().Get("backplane/services/"+service+"/manifests/"+version, nil)
	if err != nil || kv == nil {
		t.Fatalf("manifest: %v", err)
	}

	var m backplanev1.Manifest
	if err := proto.Unmarshal(kv.Value, &m); err != nil {
		t.Fatal(err)
	}

	etag := `"` + m.GetUi().GetHash() + `"`
	url := "http://" + e.platform + "/_backplane/ui/plugin.json"

	res := uiRequest(t, url, "")
	if res.StatusCode != http.StatusOK || res.Header.Get("ETag") != etag || res.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("bundle: %d ETag %q Cache-Control %q, want ETag %s", res.StatusCode,
			res.Header.Get("ETag"), res.Header.Get("Cache-Control"), etag)
	}

	if revalidated := uiRequest(t, url, etag); revalidated.StatusCode != http.StatusNotModified {
		t.Errorf("bundle revalidated with its ETag: %d, want 304", revalidated.StatusCode)
	}

	if stale := uiRequest(t, url, `"previous"`); stale.StatusCode != http.StatusOK {
		t.Errorf("bundle with a stale ETag: %d, want 200", stale.StatusCode)
	}
}

// uiRequest GETs url from the platform port with the internal secret and,
// when given, If-None-Match.
func uiRequest(t *testing.T, url, ifNoneMatch string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Bp-Internal-Secret", secret)

	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()

	return res
}

func (e *env) protocols(t *testing.T) {
	t.Helper()

	ctx := context.Background()

	client := hellov1.NewHelloServiceClient(dial(t, e.public))
	if res, err := client.Greet(ctx, &hellov1.GreetRequest{Name: "grpc"}); err != nil || res.GetGreeting() != "Hello, grpc?" {
		t.Errorf("grpc: %v %v", res, err)
	}

	stream, err := client.Countdown(ctx, &hellov1.CountdownRequest{From: 2})
	if err != nil {
		t.Fatal(err)
	}

	var got []uint32

	for {
		msg, err := stream.Recv()
		if err != nil {
			break
		}

		got = append(got, msg.GetLeft())
	}

	if len(got) != 3 {
		t.Errorf("grpc stream: %v", got)
	}

	if code, body := httpGet("http://" + e.public + "/hello/?name=http"); code != 200 || strings.TrimSpace(body) != "Hello, http?" {
		t.Errorf("http: %d %q", code, body)
	}

	cc, err := wsrpc.Dial(ctx, "ws://"+e.public+"/ws/")
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()

	ws, err := cc.NewStream(ctx, "/hello.v1.HelloService/Greet", nil)
	if err != nil {
		t.Fatal(err)
	}

	_ = ws.Send(&hellov1.GreetRequest{Name: "ws"})
	_ = ws.CloseSend()

	var out hellov1.GreetResponse
	if err := ws.Recv(&out); err != nil || out.GetGreeting() != "Hello, ws?" {
		t.Errorf("ws-proto: %v %v", out.GetGreeting(), err)
	}
}

func (e *env) hotReload(t *testing.T) {
	t.Helper()

	if _, err := e.consul.KV().Put(&api.KVPair{Key: "config/" + service + "/greeter/suffix", Value: []byte("!!!")}, nil); err != nil {
		t.Fatal(err)
	}

	e.eventually(t, "new suffix served", func() bool {
		_, body := httpGet("http://" + e.public + "/hello/?name=kv")
		return strings.TrimSpace(body) == "Hello, kv!!!"
	})
	e.eventually(t, "state shows KV source", func() bool {
		kv, _, _ := e.consul.KV().Get("backplane/services/"+service+"/instances/"+instance, nil)

		var st backplanev1.InstanceState

		return kv != nil && proto.Unmarshal(kv.Value, &st) == nil && st.GetSources()["greeter.suffix"] == backplanev1.ConfigSource_CONFIG_SOURCE_KV
	})
}

func (e *env) gracefulStop(t *testing.T) {
	t.Helper()

	if err := e.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	if err := e.cmd.Wait(); err != nil {
		t.Fatalf("exit: %v", err)
	}

	kv, _, _ := e.consul.KV().Get("backplane/services/"+service+"/instances/"+instance, nil)
	if kv != nil {
		t.Error("instance state left after stop")
	}

	if entries, _, _ := e.consul.Catalog().Service(service, "", nil); len(entries) != 0 {
		t.Errorf("still registered: %v", entries)
	}
}
