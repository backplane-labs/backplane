package console_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	helloconsolev1 "github.com/gopherex/backplane/examples/hello/proto/hello/console/v1"
	"github.com/gopherex/backplane/internal/console"
	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/internal/store"
	"github.com/gopherex/backplane/pkg/backplane/backplanetest"
	"github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
	"github.com/gopherex/backplane/pkg/backplane/infra/postgres"
)

// pgSessions starts a store on a database of its own, created on the
// server of BACKPLANE_TEST_PG and dropped after the test: deleting every
// other session must not touch a database someone uses.
func pgSessions(t *testing.T) console.PG {
	t.Helper()

	dsn := os.Getenv("BACKPLANE_TEST_PG")
	if dsn == "" {
		t.Skip("BACKPLANE_TEST_PG not set")
	}

	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}

	name := "backplane_console_test_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+name); err != nil {
		_ = admin.Close(t.Context())
		t.Skipf("cannot create a scratch database: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})

	scratch, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}

	scratch.Path = "/" + name

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: config.Secret(scratch.String())}))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	return console.NewPG(st)
}

// The PostgreSQL store and the memory fake behave alike.
func TestSessionsContract(t *testing.T) {
	t.Parallel()

	t.Run("memory", func(t *testing.T) {
		t.Parallel()
		contract(t, newMemSessions())
	})
	t.Run("postgres", func(t *testing.T) {
		t.Parallel()
		contract(t, pgSessions(t))
	})
}

func contract(t *testing.T, s console.Sessions) {
	t.Helper()

	ctx := t.Context()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a, b := contractSessions(t, s, start)

	if n, err := s.DeleteOthers(ctx, a); err != nil || n != 1 {
		t.Fatalf("delete others: %d %v", n, err)
	}

	if list, _ := s.List(ctx); len(list) != 1 || list[0].ID != a || list[0].ID == b {
		t.Fatalf("after delete others: %+v", list)
	}
}

// contractSessions leaves two sessions: a (touched) and one more; b is
// gone (stale).
func contractSessions(t *testing.T, s console.Sessions, start time.Time) (uuid.UUID, uuid.UUID) {
	t.Helper()

	ctx := t.Context()
	mk := func(token string, at time.Time) uuid.UUID {
		id, err := s.Create(ctx, []byte(token), console.Session{
			CreatedAt: at, ExpiresAt: at.Add(console.SessionTTL), Address: "192.0.2.1", UserAgent: "test",
		})
		if err != nil {
			t.Fatal(err)
		}

		return id
	}

	a, b, c := mk("ta", start), mk("tb", start.Add(time.Second)), mk("tc", start.Add(2*time.Second))

	byToken, err := s.ByToken(ctx, []byte("ta"))
	if err != nil || byToken.ID != a || !byToken.LastSeenAt.Equal(start) || byToken.Address != "192.0.2.1" ||
		!byToken.ExpiresAt.Equal(start.Add(console.SessionTTL)) {
		t.Fatalf("by token: %+v %v", byToken, err)
	}

	if _, err := s.ByToken(ctx, []byte("nope")); !errors.Is(err, console.ErrNoSession) {
		t.Fatalf("unknown token: %v", err)
	}

	_ = s.Touch(ctx, a, start.Add(time.Hour))
	_ = s.Touch(ctx, a, start.Add(time.Minute)) // never back

	if got, err := s.Get(ctx, a); err != nil || !got.LastSeenAt.Equal(start.Add(time.Hour)) {
		t.Fatalf("touched: %+v %v", got, err)
	}

	all, err := s.List(ctx)
	if err != nil || len(all) != 3 || all[0].ID != c || all[2].ID != a {
		t.Fatalf("list: %+v %v", all, err)
	}

	if ok, err := s.Delete(ctx, c); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}

	if ok, err := s.Delete(ctx, c); err != nil || ok {
		t.Fatalf("delete twice: %v %v", ok, err)
	}

	// b idles out; a was touched.
	if n, err := s.DeleteStale(ctx, start.Add(90*time.Minute), start.Add(30*time.Minute)); err != nil || n != 1 {
		t.Fatalf("stale: %d %v", n, err)
	}

	if _, err := s.Get(ctx, b); !errors.Is(err, console.ErrNoSession) {
		t.Fatalf("stale kept: %v", err)
	}

	mk("td", start)

	return a, b
}

// The whole flow over PostgreSQL: login, /ws, revoking the other sessions.
func TestLivePostgresFlow(t *testing.T) {
	t.Parallel()

	e := newEnv(t, pgSessions(t))
	c := e.mustLogin()

	if res := e.request(http.MethodGet, "/auth/session", nil, "Cookie", cookieHeader(c)); res.StatusCode != http.StatusOK {
		t.Fatalf("session: %s", res.Status)
	}

	cc := e.mustDial(c)

	e.mustLogin()

	var revoked consolev1.RevokeOtherSessionsResponse
	if err := call(t.Context(), cc, "/backplane.console.v1.SessionService/RevokeOtherSessions",
		&consolev1.RevokeOtherSessionsRequest{}, &revoked); err != nil || revoked.GetRevokedSessions() != 1 {
		t.Fatalf("revoke others: %v %v", &revoked, err)
	}
}

// hello is examples/hello built and running without Consul, its platform
// port behind the secret.
type hello struct {
	platform string // 127.0.0.1:port
	port     int
	cmd      *exec.Cmd
}

const helloSecret = "console-live-secret"

// startHello builds and runs hello (the suite of BACKPLANE_TEST_CONSUL:
// the one that builds and runs examples/hello). It runs without Consul,
// so it never meets the conformance suite's hello; the test hands the
// console a catalog of its own.
func startHello(t *testing.T) *hello {
	t.Helper()

	if os.Getenv("BACKPLANE_TEST_CONSUL") == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set (the suites that run examples/hello)")
	}

	bin := filepath.Join(t.TempDir(), "hello")
	pkg := "github.com/gopherex/backplane/pkg/backplane/build"

	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin,
		"-ldflags", "-X "+pkg+".Service=hello -X "+pkg+".Version=1.0.0", "../../examples/hello/cmd/hello")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build hello: %v\n%s", err, out)
	}

	uiAssets := t.TempDir()
	if err := os.WriteFile(filepath.Join(uiAssets, "plugin.json"), []byte(`{"sdk_major":0}`), 0o600); err != nil {
		t.Fatal(err)
	}

	platformPort, publicPort := freePort(t), freePort(t)
	h := &hello{platform: "127.0.0.1:" + strconv.Itoa(platformPort), port: platformPort}

	logs := &bytes.Buffer{}
	h.cmd = exec.Command(bin)

	h.cmd.Env = append(os.Environ(),
		"BACKPLANE_CONSUL_ADDR=", "BACKPLANE_NATS_URL=", "BACKPLANE_TEMPORAL_ADDR=",
		"BACKPLANE_INSTANCE=console-test-hello", "BACKPLANE_ADVERTISE=127.0.0.1",
		"BACKPLANE_INTERNAL_PORT="+strconv.Itoa(platformPort), "BACKPLANE_PUBLIC_PORT="+strconv.Itoa(publicPort),
		"HELLO_LEGACY_LISTEN=:"+strconv.Itoa(freePort(t)), "HELLO_UI_ASSETS="+uiAssets,
		"BACKPLANE_INTERNAL_SECRET="+helloSecret, "BACKPLANE_SHUTDOWN_DRAIN=100ms",
	)
	h.cmd.Stdout, h.cmd.Stderr = logs, logs

	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = h.cmd.Process.Kill()
		_ = h.cmd.Wait()

		if t.Failed() {
			t.Logf("hello logs:\n%s", logs)
		}
	})

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if code, _ := get(t, "http://"+h.platform+"/healthz/readiness", ""); code == http.StatusOK {
			return h
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("hello not ready")

	return nil
}

func freePort(t *testing.T) int {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	tcp, _ := ln.Addr().(*net.TCPAddr)

	return tcp.Port
}

// get answers the status and the ETag.
func get(t *testing.T, u, secret string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	if secret != "" {
		req.Header.Set(console.SecretHTTPHeader, secret)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, ""
	}
	defer res.Body.Close()

	_, _ = io.Copy(io.Discard, res.Body)

	return res.StatusCode, res.Header.Get("ETag")
}

// catalog is hello as the registry would see it.
func (h *hello) catalog(t *testing.T) map[string]registry.Service {
	t.Helper()

	code, etag := get(t, "http://"+h.platform+"/_backplane/ui/plugin.json", helloSecret)
	if code != http.StatusOK || len(etag) < 3 {
		t.Fatalf("hello's bundle: %d %q", code, etag)
	}

	m := manifest("hello", "1.0.0", "hello.console.v1.AdminService")
	m.Ui = &backplanev1.UI{Hash: etag[1 : len(etag)-1], SdkMajor: 0}

	return map[string]registry.Service{"hello": {
		Name: "hello", Manifests: map[string]*backplanev1.Manifest{"1.0.0": m},
		Instances: []registry.Instance{instance("console-test-hello", "1.0.0", "127.0.0.1", h.port, serving, true)},
	}}
}

//nolint:paralleltest // one hello process for the whole scenario
func TestLiveHello(t *testing.T) {
	h := startHello(t)
	services := h.catalog(t)
	hash := services["hello"].Manifests["1.0.0"].GetUi().GetHash()

	e := newEnv(t, nil, func(s *console.Settings) { s.InternalSecret = helloSecret })
	e.hub.Publish(services)

	c := e.mustLogin()
	cc := e.mustDial(c)
	ctx := t.Context()

	var stats helloconsolev1.GetStatsResponse
	if err := call(ctx, cc, "/hello.console.v1.AdminService/GetStats", &helloconsolev1.GetStatsRequest{}, &stats); err != nil {
		t.Fatalf("relay to hello: %v", err)
	}

	if stats.GetCurrentGreeting() == "" {
		t.Fatalf("stats: %v", &stats)
	}

	err := call(ctx, cc, "/hello.v1.HelloService/Greet", &helloconsolev1.GetStatsRequest{}, &stats)
	if code(err) != codes.PermissionDenied {
		t.Fatalf("hello's public API through the relay: %v", err)
	}

	// A console with the wrong secret: hello's guard refuses.
	wrong := newEnv(t, nil, func(s *console.Settings) { s.InternalSecret = "wrong" })
	wrong.hub.Publish(services)

	err = call(ctx, wrong.mustDial(wrong.mustLogin()), "/hello.console.v1.AdminService/GetStats",
		&helloconsolev1.GetStatsRequest{}, &stats)
	if code(err) != codes.PermissionDenied {
		t.Fatalf("wrong secret: %v", err)
	}

	// The bundle: fetched, revalidated, cached.
	path := fmt.Sprintf("/plugins/hello/%s/plugin.json", hash)
	fetch := func(header ...string) *http.Response {
		return e.request(http.MethodGet, path, nil, append([]string{"Cookie", cookieHeader(c)}, header...)...)
	}

	first := fetch()
	body, _ := io.ReadAll(first.Body)

	if first.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"sdk_major"`)) {
		t.Fatalf("bundle: %s %q", first.Status, body)
	}

	if res := fetch("If-None-Match", `"`+hash+`"`); res.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidation: %s", res.Status)
	}

	_ = h.cmd.Process.Kill()
	_ = h.cmd.Wait()

	if res := fetch(); res.StatusCode != http.StatusOK {
		t.Fatalf("from the cache with hello gone: %s", res.Status)
	}
}
