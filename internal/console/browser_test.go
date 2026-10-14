package console_test

import (
	"context"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/console"
	"github.com/backplane-labs/backplane/internal/otlp"
	platformapi "github.com/backplane-labs/backplane/internal/platform"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// Real HTTP cookie, ws-proto and bundle-proxy acceptance. Only the registry and
// session persistence are in memory; the browser uses the production TS client.
func TestBrowserConsole(t *testing.T) {
	t.Parallel()

	if os.Getenv("BACKPLANE_TEST_BROWSER") != "1" {
		t.Skip("build web fixtures, then set BACKPLANE_TEST_BROWSER=1")
	}

	web, err := filepath.Abs("../../web")
	if err != nil {
		t.Fatal(err)
	}

	shell := fstest.MapFS{}

	err = fs.WalkDir(os.DirFS(filepath.Join(web, "apps", "console", "dist")), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		body, err := os.ReadFile(filepath.Join(web, "apps", "console", "dist", path))
		if err != nil {
			return err
		}

		shell[path] = &fstest.MapFile{Data: body, Mode: 0o644}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var bundleRequests atomic.Int64

	files := http.StripPrefix("/_backplane/ui/", http.FileServer(http.Dir(filepath.Join(web, "templates", "module", "dist", "plugin"))))
	module := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(console.SecretHTTPHeader) != "relay-secret" {
			http.Error(w, "internal secret required", http.StatusForbidden)
			return
		}

		bundleRequests.Add(1)
		w.Header().Set("ETag", `"fixture-v1"`)
		files.ServeHTTP(w, r)
	}))
	t.Cleanup(module.Close)
	_, portText, _ := net.SplitHostPort(module.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	hub := registry.NewHub()
	m := manifest("hello", "1.0.0")
	m.Ui = &backplanev1.UI{Hash: "fixture-v1", SdkMajor: 0}
	hub.Publish(map[string]registry.Service{
		"hello":             {Name: "hello", Manifests: map[string]*backplanev1.Manifest{"1.0.0": m}, Instances: []registry.Instance{instance("hello-1", "1.0.0", "127.0.0.1", port, serving, true)}},
		"browser-live-data": {Name: "browser-live-data"},
	})
	h := backplanetest.New(t, backplanetest.Name("backplane"))

	var reported atomic.Bool

	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)

		var batch collogspb.ExportLogsServiceRequest
		if err != nil || proto.Unmarshal(body, &batch) != nil {
			t.Error("invalid console OTLP report")
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		for _, resource := range batch.GetResourceLogs() {
			for _, scope := range resource.GetScopeLogs() {
				for _, record := range scope.GetLogRecords() {
					if strings.Contains(record.GetBody().GetStringValue(), "console-ingest-regression") {
						reported.Store(true)
					}
				}
			}
		}

		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	t.Cleanup(collector.Close)

	admission, err := otlp.New(otlp.Config{
		URL: collector.URL, Keys: []config.Secret{"browser-ingest-key-012345"},
		BodyBytes: 1 << 20, Concurrent: 2, RatePerMinute: 6000, Burst: 100, MaxIPs: 16, IdleTTL: time.Minute, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(admission.Close)

	platformAPI := platformapi.New(h.Root(), "browser-fixture", []*consolev1.Capability{{Name: "audit", Enabled: true}, {Name: "workflows", Enabled: false}}, []platformapi.Probe{
		{Name: "postgres", Required: true, Run: func(context.Context) error { return nil }},
	})
	c := console.New(h.Root(), console.Settings{Prefix: "/backplane", AdminToken: adminToken, InternalSecret: "relay-secret", InsecureCookie: true}, newMemSessions(), newAttempts(), hub,
		console.WithServices(platformAPI.Register), console.WithShell(shell), console.WithTelemetry(admission), console.WithSessionTelemetry(admission.SessionLogs()))
	h.Start()

	srv := httptest.NewServer(c.Handler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "node", "tests/console-integration.mjs", srv.URL, adminToken)
	cmd.Dir = web

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}

	if bundleRequests.Load() == 0 {
		t.Fatal("browser never used the module bundle proxy")
	}

	if !reported.Load() {
		t.Fatal("console error did not reach keyed ingest using its session")
	}

	t.Log(string(output))
}

func TestTelemetryWithoutOperatorSession(t *testing.T) {
	t.Parallel()
	h := backplanetest.New(t, backplanetest.Name("backplane"))
	admission := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/logs" {
			t.Errorf("unexpected admission path %q", r.URL.Path)
		}

		w.WriteHeader(http.StatusAccepted)
	})
	c := console.New(h.Root(), console.Settings{Prefix: "/backplane", AdminToken: adminToken}, newMemSessions(), newAttempts(), registry.NewHub(), console.WithTelemetry(admission))
	h.Start()

	request := httptest.NewRequest(http.MethodPost, "/backplane/telemetry/v1/logs", strings.NewReader("{}"))
	response := httptest.NewRecorder()
	c.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("public admission was gated by the operator session: %d", response.Code)
	}
}
