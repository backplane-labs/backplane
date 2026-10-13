package conformance_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ws-proto/wsrpc"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	helloconsolev1 "github.com/gopherex/backplane/examples/hello/proto/hello/console/v1"
	hellov1 "github.com/gopherex/backplane/examples/hello/proto/hello/v1"
)

// M1 exercises configuration, relay and bundles using direct process listeners.
// It owns the hello service identity in Consul, but allocates its own ports.
const (
	m1Prefix     = "/backplane" // directly mounted console prefix
	m1Hello      = "m1-hello-1"
	m1Backplane  = "m1-backplane-1"
	m1Version    = "0.0.0-m1" // backplane's manifest version: removed after the run
	m1HelloVer   = "9.9.7"
	m1Reconcile  = 500 * time.Millisecond
	m1Wait       = 30 * time.Second
	m1StopWait   = 30 * time.Second
	m1SuffixPath = "greeter.suffix"
	m1SuffixKey  = "config/" + service + "/greeter/suffix"
	m1Revision   = "config/" + service + "/_revision"
)

// m1Proc is one child process; its output may be read while it runs.
type m1Proc struct {
	name string
	cmd  *exec.Cmd
	logs *syncBuffer
	done bool
}

// m1Start runs bin with env (on top of this process's environment, less
// the test's BACKPLANE_TEST_* and the SDK dependencies it does not want).
// The cleanup stops it and, when the test failed, logs its output.
func m1Start(t *testing.T, name, bin string, env ...string) *m1Proc {
	t.Helper()

	p := &m1Proc{name: name, cmd: exec.Command(bin), logs: &syncBuffer{}}

	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BACKPLANE_") && !strings.HasPrefix(kv, "HELLO_") {
			p.cmd.Env = append(p.cmd.Env, kv)
		}
	}

	p.cmd.Env = append(p.cmd.Env, env...)
	p.cmd.Stdout, p.cmd.Stderr = p.logs, p.logs

	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}

	t.Cleanup(func() {
		p.stop(t)

		if t.Failed() {
			t.Logf("---- %s logs ----\n%s", p.name, p.logs)
		}
	})

	return p
}

// stop: SIGTERM, then SIGKILL after m1StopWait; Wait joins the output
// copying, so the log is complete afterwards.
func (p *m1Proc) stop(t *testing.T) {
	t.Helper()

	if p.done {
		return
	}

	p.done = true

	_ = p.cmd.Process.Signal(syscall.SIGTERM)

	exited := make(chan error, 1)

	go func() { exited <- p.cmd.Wait() }()

	select {
	case err := <-exited:
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			t.Errorf("%s: wait: %v", p.name, err)
		} else if err != nil {
			t.Errorf("%s exited with %v on SIGTERM", p.name, err)
		}
	case <-time.After(m1StopWait):
		_ = p.cmd.Process.Kill()

		<-exited

		t.Errorf("%s did not stop within %s: killed", p.name, m1StopWait)
	}
}

// m1Until polls ok until it holds; the failure names what and ok's last
// answer.
func m1Until(t *testing.T, limit time.Duration, what string, ok func() (bool, string)) {
	t.Helper()

	var last string

	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		var done bool
		if done, last = ok(); done {
			return
		}
	}

	t.Fatalf("%s: not reached in %s; last: %s", what, limit, last)
}

func m1Build(t *testing.T, dir, name, pkg, version string) string {
	t.Helper()

	bin := filepath.Join(dir, name)
	build := "github.com/gopherex/backplane/pkg/backplane/build"

	cmd := exec.Command("go", "build", "-race", "-o", bin,
		"-ldflags", "-X "+build+".Service="+name+" -X "+build+".Version="+version, pkg)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}

	return bin
}

func m1Random(t *testing.T) string {
	t.Helper()

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	return hex.EncodeToString(b)
}

// m1Database creates a scratch database on the server of dsn and drops it
// after the test: backplane migrates its schema there, and its revisions
// and sessions never touch a database someone uses.
func m1Database(t *testing.T, dsn string) string {
	t.Helper()

	ctx := context.Background()

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}

	name := "backplane_m1_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		_ = admin.Close(ctx)

		t.Fatalf("create the scratch database (the role needs CREATEDB): %v", err)
	}

	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}

		_ = admin.Close(ctx)
	})

	scratch, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}

	scratch.Path = "/" + name

	return scratch.String()
}

// m1 is the scenario's world.
type m1 struct {
	consul    *api.Client
	consulURL string
	host      string // this host as containers reach it
	secret    string // backplane's internal secret, accepted by hello
	token     string // the console's admin token
	helloBin  string
	helloEnv  []string
	hello     *m1Proc
	console   string // host:port of the console listener
	helloURL  string // direct public API address
}

// m1Env skips without the stack: Consul and PostgreSQL.
func m1Env(t *testing.T) (string, string) {
	t.Helper()

	addr, dsn := os.Getenv("BACKPLANE_TEST_CONSUL"), os.Getenv("BACKPLANE_TEST_PG")
	if addr == "" || dsn == "" {
		t.Skip("BACKPLANE_TEST_CONSUL and BACKPLANE_TEST_PG not set (make up; make test-m1)")
	}

	return addr, dsn
}

// TestM1 runs backplane and hello and walks the M1 scenario.
//
//nolint:paralleltest // owns the hello service identity
func TestM1(t *testing.T) {
	addr, dsn := m1Env(t)

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	w := &m1{consul: c, consulURL: addr, host: advertise(t), secret: m1Random(t), token: m1Random(t)}

	// One hello per Consul: wait out another suite's, then own every key of
	// the service (and of this run's backplane instance and version).
	m1Until(t, m1Wait, "no live hello instance on this Consul (stop other hello runs)", func() (bool, string) {
		keys, _, err := c.KV().Keys(m1InstancesPrefix(service), "", nil)

		return err == nil && len(keys) == 0, fmt.Sprint(keys, err)
	})

	wipe := func() {
		_, _ = c.KV().DeleteTree("backplane/services/"+service+"/", nil)
		_, _ = c.KV().DeleteTree("config/"+service+"/", nil)
		_, _ = c.KV().Delete(m1InstancesPrefix("backplane")+m1Backplane, nil)
		_, _ = c.KV().Delete("backplane/services/backplane/manifests/"+m1Version, nil)
	}

	scratch := m1Database(t, dsn) // dropped last

	wipe()
	t.Cleanup(wipe) // after both processes stopped: no reconciler rewrites config/

	dir := t.TempDir()
	backplaneBin := m1Build(t, dir, "backplane", "../cmd/backplane", m1Version)
	w.helloBin = m1Build(t, dir, service, "../examples/hello/cmd/hello", m1HelloVer)

	consolePort := freePort(t)
	w.console = "127.0.0.1:" + consolePort
	m1Start(t, "backplane", backplaneBin,
		"BACKPLANE_CONSUL_ADDR="+addr,
		"BACKPLANE_PG_DSN="+scratch,
		"BACKPLANE_VALKEY_ADDR="+testValkey(t),
		"BACKPLANE_INSTANCE="+m1Backplane,
		"BACKPLANE_ADVERTISE="+w.host,
		"BACKPLANE_INTERNAL_PORT="+freePort(t),
		"BACKPLANE_PUBLIC_PORT="+freePort(t),
		"BACKPLANE_INTERNAL_SECRET="+w.secret,
		"BACKPLANE_ADMIN_TOKEN="+w.token,
		"BACKPLANE_CONSOLE_LISTEN=:"+consolePort,
		"BACKPLANE_CONSOLE_PREFIX="+m1Prefix,
		"BACKPLANE_CONSOLE_INSECURE_COOKIE=true",
		"BACKPLANE_LIVE_CONFIG_RECONCILE_INTERVAL="+m1Reconcile.String(),
		"BACKPLANE_SHUTDOWN_DRAIN=100ms",
		"BACKPLANE_CONSUL_CHECK_INTERVAL=2s",
	)

	helloPort := freePort(t)
	w.helloURL = "http://127.0.0.1:" + helloPort
	w.helloEnv = []string{
		"BACKPLANE_CONSUL_ADDR=" + addr,
		"BACKPLANE_INSTANCE=" + m1Hello,
		"HELLO_LEGACY_LISTEN=:" + freePort(t),
		"BACKPLANE_ADVERTISE=" + w.host,
		"BACKPLANE_INTERNAL_PORT=" + freePort(t),
		"BACKPLANE_PUBLIC_PORT=" + helloPort,
		"BACKPLANE_INTERNAL_SECRET=" + w.secret,
		"BACKPLANE_SHUTDOWN_DRAIN=100ms",
		"BACKPLANE_CONSUL_CHECK_INTERVAL=2s",
	}
	w.hello = m1Start(t, service, w.helloBin, w.helloEnv...)

	m1Until(t, m1Wait, "hello ready", func() (bool, string) {
		return m1Ready(w.hello, w.helloEnv)
	})

	// The console and registry are available on their own listener.
	m1Until(t, m1Wait, "console listening", func() (bool, string) {
		code, body := httpGet("http://" + w.console + m1Prefix + "/auth/session")
		return code == http.StatusUnauthorized, fmt.Sprintf("%d %q", code, body)
	})

	cc, err := w.dial(t, "http://"+w.console)
	if err != nil {
		t.Fatalf("console directly: %v", err)
	}

	m1Until(t, m1Wait, "CatalogService.ListServices lists hello", func() (bool, string) {
		return m1Listed(t, cc)
	})

	ui := m1Plugin(t, cc)

	// b. hello directly with its default suffix.
	w.greets(t, "Hello, m1!")

	// c. Validate, save, apply.
	w.saves(t, cc)

	// e. The reconciler repairs Consul KV.
	w.repairs(t)

	// d. Rollback is a new revision with the old value.
	w.rollsBack(t, cc)

	// f. The relay to hello's internal API.
	m1Relay(t, cc)

	// g. The plugin bundle through the console, directly.
	w.bundle(t, ui)

	// h. hello leaves Consul when it stops, returns when it starts.
	w.restarts(t)
}

func m1InstancesPrefix(name string) string { return "backplane/services/" + name + "/instances/" }

// m1Ready is hello's readiness on its platform port.
func m1Ready(p *m1Proc, env []string) (bool, string) {
	if p.cmd.ProcessState != nil {
		return false, "exited"
	}

	port := ""

	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "BACKPLANE_INTERNAL_PORT="); ok {
			port = v
		}
	}

	code, body := httpGet("http://127.0.0.1:" + port + "/healthz/readiness")

	return code == http.StatusOK, fmt.Sprintf("%d %s", code, body)
}

// login posts the admin token to the console at base (direct listener or a deployment-owned proxy),
// as the shell does: same-origin, JSON.
func (w *m1) login(t *testing.T, base string) (string, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+m1Prefix+"/auth/login",
		strings.NewReader(`{"token":"`+w.token+`"}`))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", base)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login: %d %s", res.StatusCode, body)
	}

	for _, cookie := range res.Cookies() {
		if cookie.Name == "bp_session" {
			if !cookie.HttpOnly || cookie.Path != m1Prefix+"/" {
				return "", fmt.Errorf("cookie: %+v", cookie)
			}

			return cookie.Name + "=" + cookie.Value, nil
		}
	}

	return "", fmt.Errorf("login: no session cookie: %v", res.Header)
}

// dial logs in at base and opens /ws with the cookie.
func (w *m1) dial(t *testing.T, base string) (*wsrpc.ClientConn, error) {
	t.Helper()

	cookie, err := w.login(t, base)
	if err != nil {
		return nil, err
	}

	h := http.Header{}
	h.Set("Cookie", cookie)
	h.Set("Origin", base)

	cc, err := wsrpc.Dial(t.Context(), "ws"+strings.TrimPrefix(base, "http")+m1Prefix+"/ws", wsrpc.WithHeader(h))
	if err != nil {
		return nil, err
	}

	t.Cleanup(func() { _ = cc.Close() })

	// The socket answers: a session call.
	var sessions consolev1.ListSessionsResponse
	if err := m1Call(t.Context(), cc, "/backplane.console.v1.SessionService/ListSessions",
		&consolev1.ListSessionsRequest{}, &sessions); err != nil {
		return nil, fmt.Errorf("ListSessions: %w", err)
	}

	return cc, nil
}

// m1Call is a unary call over ws-proto.
func m1Call(ctx context.Context, cc *wsrpc.ClientConn, method string, req, res proto.Message) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	s, err := cc.NewStream(ctx, method, nil)
	if err != nil {
		return err
	}

	if err := s.Send(req); err != nil {
		return err
	}

	if err := s.CloseSend(); err != nil {
		return err
	}

	if err := s.Recv(res); err != nil {
		return err
	}

	if err := s.Recv(res); !errors.Is(err, io.EOF) {
		return err
	}

	return nil
}

func m1Code(err error) codes.Code {
	if err == nil {
		return codes.OK
	}

	return wsrpc.FromError(err).Code
}

// m1Listed: ListServices has hello with a healthy instance.
func m1Listed(t *testing.T, cc *wsrpc.ClientConn) (bool, string) {
	t.Helper()

	var res consolev1.ListServicesResponse
	if err := m1Call(t.Context(), cc, "/backplane.console.v1.CatalogService/ListServices",
		&consolev1.ListServicesRequest{}, &res); err != nil {
		return false, err.Error()
	}

	for _, s := range res.GetServices() {
		if s.GetName() == service && s.GetHealthy() > 0 {
			return true, ""
		}
	}

	return false, fmt.Sprint(res.GetServices())
}

// m1Plugin is hello's UI bundle hash as the catalog lists it.
func m1Plugin(t *testing.T, cc *wsrpc.ClientConn) string {
	t.Helper()

	var res consolev1.GetServiceResponse
	if err := m1Call(t.Context(), cc, "/backplane.console.v1.CatalogService/GetService",
		&consolev1.GetServiceRequest{Name: service}, &res); err != nil {
		t.Fatalf("GetService: %v", err)
	}

	if res.GetLatest().GetVersion() != m1HelloVer || len(res.GetInstances()) != 1 ||
		res.GetInstances()[0].GetId() != m1Hello || res.GetLatest().GetUi().GetHash() == "" {
		t.Fatalf("GetService hello: latest %s, instances %v, ui %v",
			res.GetLatest().GetVersion(), res.GetInstances(), res.GetLatest().GetUi())
	}

	return res.GetLatest().GetUi().GetHash()
}

// greets waits until hello directly answers want.
func (w *m1) greets(t *testing.T, want string) {
	t.Helper()

	m1Until(t, m1Wait, "GET /hello/?name=m1 directly = "+strconv.Quote(want), func() (bool, string) {
		code, body := httpGet(w.helloURL + "/hello/?name=m1")

		return code == http.StatusOK && strings.TrimSpace(body) == want, fmt.Sprintf("%d %q", code, body)
	})
}

func (w *m1) kv(key string) string {
	p, _, err := w.consul.KV().Get(key, nil)
	if err != nil || p == nil {
		return fmt.Sprintf("<none: %v>", err)
	}

	return string(p.Value)
}

// state is hello's instance state in KV.
func (w *m1) state() (*backplanev1.InstanceState, error) {
	p, _, err := w.consul.KV().Get(m1InstancesPrefix(service)+m1Hello, nil)
	if err != nil || p == nil {
		return nil, fmt.Errorf("instance state: %v %w", p, err)
	}

	var st backplanev1.InstanceState
	if err := proto.Unmarshal(p.Value, &st); err != nil {
		return nil, fmt.Errorf("instance state: %w", err)
	}

	return &st, nil
}

// applied: hello reports revision rev from KV — in its instance state and
// in ConfigService.GetConfig's per-instance view — with suffix.
func (w *m1) applied(t *testing.T, cc *wsrpc.ClientConn, rev uint64, suffix string) {
	t.Helper()

	m1Until(t, m1Wait, fmt.Sprintf("instance state in KV reports revision %d", rev), func() (bool, string) {
		st, err := w.state()
		if err != nil {
			return false, err.Error()
		}

		src := st.GetSources()[m1SuffixPath]

		return st.GetConfigRevision() == rev && src == backplanev1.ConfigSource_CONFIG_SOURCE_KV,
			fmt.Sprintf("revision %d, %s source %v, rejected %d %q", st.GetConfigRevision(), m1SuffixPath, src,
				st.GetConfigRejectedRevision(), st.GetConfigError())
	})

	m1Until(t, m1Wait, fmt.Sprintf("GetConfig: %s applied revision %d", m1Hello, rev), func() (bool, string) {
		var res consolev1.GetConfigResponse
		if err := m1Call(t.Context(), cc, "/backplane.console.v1.ConfigService/GetConfig",
			&consolev1.GetConfigRequest{Service: service}, &res); err != nil {
			return false, err.Error()
		}

		cfg := res.GetConfig()
		if cfg.GetCurrent().GetRevision() != rev || len(cfg.GetInstances()) != 1 {
			return false, fmt.Sprintf("current %d, instances %d", cfg.GetCurrent().GetRevision(), len(cfg.GetInstances()))
		}

		in := cfg.GetInstances()[0]
		for _, v := range in.GetLive() {
			if v.GetPath() == m1SuffixPath {
				ok := in.GetId() == m1Hello && in.GetAppliedRevision() == rev &&
					v.GetValue() == strconv.Quote(suffix) && v.GetSource() == backplanev1.ConfigSource_CONFIG_SOURCE_KV

				return ok, fmt.Sprintf("%s applied %d: %s = %s (%v)", in.GetId(), in.GetAppliedRevision(),
					v.GetPath(), v.GetValue(), v.GetSource())
			}
		}

		return false, fmt.Sprintf("no %s in %v", m1SuffixPath, in.GetLive())
	})
}

// saves: an invalid override is refused with violations and saved
// nowhere; a baseline and then "?!" are revisions 1 and 2, delivered to
// KV and applied by hello.
func (w *m1) saves(t *testing.T, cc *wsrpc.ClientConn) {
	t.Helper()

	ctx := t.Context()

	// A value of the wrong type (schema) and a path that is not Live
	// (manifest).
	for path, value := range map[string]string{"greeter.excited": `"very"`, "greeter.salute": `"Hi"`} {
		var v consolev1.ValidateOverrideResponse
		if err := m1Call(ctx, cc, "/backplane.console.v1.ConfigService/ValidateOverride",
			&consolev1.ValidateOverrideRequest{Service: service, Values: map[string]string{path: value}}, &v); err != nil {
			t.Fatalf("ValidateOverride %s: %v", path, err)
		}

		if len(v.GetViolations()) == 0 || v.GetViolations()[0].GetPath() != path {
			t.Fatalf("ValidateOverride %s = %s: want a violation at the path, got %v", path, value, v.GetViolations())
		}

		t.Logf("ValidateOverride %s = %s: %v", path, value, v.GetViolations())
	}

	var refused consolev1.SaveRevisionResponse
	if err := m1Call(ctx, cc, "/backplane.console.v1.ConfigService/SaveRevision",
		&consolev1.SaveRevisionRequest{Service: service, Values: map[string]string{"greeter.excited": `"very"`}},
		&refused); err != nil || refused.GetRevision() != nil || len(refused.GetViolations()) == 0 {
		t.Fatalf("SaveRevision of an invalid value: %v %v", &refused, err)
	}

	if got := w.kv(m1Revision); !strings.HasPrefix(got, "<none") {
		t.Fatalf("a refused override reached KV: _revision %s", got)
	}

	for i, suffix := range []string{"!", "?!"} {
		var res consolev1.SaveRevisionResponse
		if err := m1Call(ctx, cc, "/backplane.console.v1.ConfigService/SaveRevision",
			&consolev1.SaveRevisionRequest{
				Service: service, Values: map[string]string{m1SuffixPath: strconv.Quote(suffix)}, Comment: "m1",
			}, &res); err != nil {
			t.Fatalf("SaveRevision %q: %v", suffix, err)
		}

		want := uint64(i + 1)
		if len(res.GetViolations()) > 0 || res.GetDeliveryError() != "" || res.GetRevision().GetRevision() != want ||
			!strings.HasPrefix(res.GetRevision().GetAuthor(), "console:") {
			t.Fatalf("SaveRevision %q: want revision %d by a console session, got %v", suffix, want, &res)
		}

		if got := w.kv(m1Revision); got != strconv.FormatUint(want, 10) {
			t.Fatalf("KV _revision after the save: %s, want %d", got, want)
		}

		if got := w.kv(m1SuffixKey); got != suffix {
			t.Fatalf("KV %s after the save: %q, want %q", m1SuffixKey, got, suffix)
		}
	}

	w.greets(t, "Hello, m1?!")
	w.applied(t, cc, 2, "?!")
}

// repairs: config/hello/ wiped by hand comes back; a key edited by hand
// is overwritten — and hello follows.
func (w *m1) repairs(t *testing.T) {
	t.Helper()

	if _, err := w.consul.KV().DeleteTree("config/"+service+"/", nil); err != nil {
		t.Fatal(err)
	}

	m1Until(t, m1Wait, "wiped config/hello/ restored by the reconciler", func() (bool, string) {
		rev, suffix := w.kv(m1Revision), w.kv(m1SuffixKey)

		return rev == "2" && suffix == "?!", fmt.Sprintf("_revision %s, suffix %s", rev, suffix)
	})

	if _, err := w.consul.KV().Put(&api.KVPair{Key: m1SuffixKey, Value: []byte("#")}, nil); err != nil {
		t.Fatal(err)
	}

	m1Until(t, m1Wait, "manual edit of greeter/suffix overwritten", func() (bool, string) {
		suffix := w.kv(m1SuffixKey)

		return suffix == "?!", "suffix " + suffix
	})

	w.greets(t, "Hello, m1?!")
}

// rollsBack: back to revision 1 is revision 3 with its value.
func (w *m1) rollsBack(t *testing.T, cc *wsrpc.ClientConn) {
	t.Helper()

	var res consolev1.RollbackResponse
	if err := m1Call(t.Context(), cc, "/backplane.console.v1.ConfigService/Rollback",
		&consolev1.RollbackRequest{Service: service, Revision: 1, Comment: "m1 rollback"}, &res); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	r := res.GetRevision()
	if len(res.GetViolations()) > 0 || res.GetDeliveryError() != "" || r.GetRevision() != 3 || r.GetRollbackOf() != 1 ||
		r.GetValues()[m1SuffixPath] != `"!"` {
		t.Fatalf("Rollback: want revision 3 = revision 1, got %v", &res)
	}

	var page consolev1.ListRevisionsResponse
	if err := m1Call(t.Context(), cc, "/backplane.console.v1.ConfigService/ListRevisions",
		&consolev1.ListRevisionsRequest{Service: service}, &page); err != nil || len(page.GetRevisions()) != 3 {
		t.Fatalf("ListRevisions: %v %v", &page, err)
	}

	w.greets(t, "Hello, m1!")
	w.applied(t, cc, 3, "!")
}

// m1Relay: the internal API through /ws, the public one refused.
func m1Relay(t *testing.T, cc *wsrpc.ClientConn) {
	t.Helper()

	var stats helloconsolev1.GetStatsResponse
	if err := m1Call(t.Context(), cc, "/hello.console.v1.AdminService/GetStats",
		&helloconsolev1.GetStatsRequest{}, &stats); err != nil {
		t.Fatalf("relay AdminService.GetStats: %v", err)
	}

	if stats.GetCurrentGreeting() != "Hello, you!" || stats.GetGreetings() == 0 {
		t.Fatalf("relay AdminService.GetStats: %v", &stats)
	}

	var out hellov1.GreetResponse

	err := m1Call(t.Context(), cc, "/hello.v1.HelloService/Greet", &hellov1.GreetRequest{Name: "relay"}, &out)
	if m1Code(err) != codes.PermissionDenied {
		t.Fatalf("hello's public API through the relay: want PERMISSION_DENIED, got %v", err)
	}
}

// bundle: plugin.json of hello's UI through the direct console listener, with
// the cookie; immutable by its hash.
func (w *m1) bundle(t *testing.T, hash string) {
	t.Helper()

	cookie, err := w.login(t, "http://"+w.console)
	if err != nil {
		t.Fatal(err)
	}

	bundleURL := fmt.Sprintf("http://%s%s/plugins/%s/%s/plugin.json", w.console, m1Prefix, service, hash)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, bundleURL, http.NoBody)
	req.Header.Set("Cookie", cookie)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)

	var plugin map[string]any
	if res.StatusCode != http.StatusOK || json.Unmarshal(body, &plugin) != nil {
		t.Fatalf("GET %s: %d %q", bundleURL, res.StatusCode, body)
	}

	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=") {
		t.Errorf("bundle Cache-Control %q: want immutable", cc)
	}

	if etag := res.Header.Get("ETag"); etag != `"`+hash+`"` {
		t.Errorf("bundle ETag %q: want %q", etag, hash)
	}

	// Without the session: refused.
	anon, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, bundleURL, http.NoBody)
	if code, body := m1Do(anon); code != http.StatusUnauthorized {
		t.Errorf("bundle without a session: %d %q", code, body)
	}
}

func m1Do(req *http.Request) (int, string) {
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)

	return res.StatusCode, string(body)
}

// restarts: a stopped hello deregisters and leaves Consul; started
// again, it serves directly and applies the current revision from KV.
func (w *m1) restarts(t *testing.T) {
	t.Helper()

	w.hello.stop(t)

	m1Until(t, m1Wait, "stopped hello leaves Consul catalog", func() (bool, string) {
		entries, _, err := w.consul.Health().Service(service, "", false, nil)
		return err == nil && len(entries) == 0, fmt.Sprintf("%d entries: %v", len(entries), err)
	})

	w.hello = m1Start(t, service+" (restarted)", w.helloBin, w.helloEnv...)

	m1Until(t, m1Wait, "restarted hello ready", func() (bool, string) {
		return m1Ready(w.hello, w.helloEnv)
	})

	w.greets(t, "Hello, m1!")

	m1Until(t, m1Wait, "restarted hello reports revision 3", func() (bool, string) {
		st, err := w.state()
		if err != nil {
			return false, err.Error()
		}

		return st.GetConfigRevision() == 3, fmt.Sprint("revision ", st.GetConfigRevision())
	})
}

// testValkey is BACKPLANE_TEST_VALKEY (make up: localhost:6379), which the
// built backplane needs for login attempts; skipped without.
func testValkey(t *testing.T) string {
	t.Helper()

	addr := os.Getenv("BACKPLANE_TEST_VALKEY")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_VALKEY not set (make up)")
	}

	return addr
}
