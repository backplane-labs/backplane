package conformance_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/trace"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/standard"
	"github.com/backplane-labs/backplane/pkg/backplane/event"
)

// Greeted is hello's event, decoded by the subscriber's own copy of the
// type.
type Greeted struct {
	Name  string `json:"name"`
	Count uint64 `json:"count"`
}

type watchConfig struct {
	config.Backplane `json:"backplane"`
}

// received is one event as the reactor saw it.
type received struct {
	event Greeted
	trace trace.TraceID
}

// TestEvents runs hello with NATS and an in-process subscriber that reacts
// to hello.Greeted: the event travels hello → JetStream → reactor with its
// CloudEvents metadata and trace. Requires BACKPLANE_TEST_NATS (e.g.
// localhost:4222).
//
//nolint:paralleltest // hello's ports and stream are shared with TestContract
func TestEvents(t *testing.T) {
	addr := os.Getenv("BACKPLANE_TEST_NATS")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_NATS not set (make up)")
	}

	url := "nats://" + addr
	watcher := "watch-" + randomHex(t)
	// The durable consumer is named <subscriber>__<consumer>, NATS-escaped:
	// the reactor on the root is consumer "hello.Greeted".
	durable := watcher + "__hello_2EGreeted"
	jet := natsAdmin(t, url, watcher)

	got := make(chan received, 1)
	runWatcher(t, url, watcher, got)
	awaitConsumer(t, jet, durable)

	h := runHello(t, url)
	name := "ev-" + randomHex(t)

	// The caller's trace travels with the event to the reactor.
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	traceparent := "00-" + traceID + "-00f067aa0ba902b7-01"
	if code, body := httpGet("http://"+h.public+"/hello/?name="+name, "traceparent", traceparent); code != http.StatusOK {
		t.Fatalf("greet: %d %s", code, body)
	}

	var r received
	select {
	case r = <-got:
	case <-time.After(15 * time.Second):
		t.Fatal("event not received")
	}

	if r.event != (Greeted{Name: name, Count: 1}) {
		t.Fatalf("received %+v", r.event)
	}

	s, err := jet.Stream(t.Context(), "bp_hello")
	if err != nil {
		t.Fatal(err)
	}

	m, err := s.GetLastMsgForSubject(t.Context(), "bp.hello.Greeted")
	if err != nil {
		t.Fatal(err)
	}

	for k, want := range map[string]string{
		"ce-specversion": "1.0", "ce-source": service, "ce-type": service + ".Greeted",
		"ce-subject": name, "ce-datacontenttype": "application/json", "ce-version": version,
	} {
		if v := m.Header.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}

	if id := m.Header.Get("ce-id"); id == "" || m.Header.Get("Nats-Msg-Id") != id {
		t.Errorf("ce-id %q, Nats-Msg-Id %q", id, m.Header.Get("Nats-Msg-Id"))
	}

	if tp := m.Header.Get("traceparent"); !strings.Contains(tp, traceID) || r.trace.String() != traceID {
		t.Errorf("event traceparent %q, reactor trace %s, want trace %s", tp, r.trace, traceID)
	}
}

func randomHex(t *testing.T) string {
	t.Helper()

	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}

	return hex.EncodeToString(b[:])
}

// jetStream is a plain JetStream client of the test.
type jetStream struct{ jetstream.JetStream }

// natsAdmin inspects JetStream and removes what the test created.
func natsAdmin(t *testing.T, url, watcher string) jetStream {
	t.Helper()

	conn, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}

	jet, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		_ = jet.DeleteStream(ctx, "bp_hello")
		_ = jet.DeleteStream(ctx, "bp_dlq_"+watcher)

		conn.Close()
	})

	return jetStream{jet}
}

// runWatcher runs an in-process service whose reactor forwards hello.Greeted
// to got.
func runWatcher(t *testing.T, url, watcher string, got chan<- received) {
	t.Helper()

	file := filepath.Join(t.TempDir(), "watch.yaml")
	cfg := "backplane:\n" +
		"  nats:\n    url: " + url + "\n" +
		"  internal_port: " + freePort(t) + "\n" +
		"  public_port: " + freePort(t) + "\n" +
		"  shutdown:\n    drain: 10ms\n    listeners: 3s\n    reserve: 1s\n    timeout: 5s\n"

	if err := os.WriteFile(file, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	newState := func(root backplane.Root[watchConfig]) (*struct{}, error) {
		event.React(root, "hello.Greeted", func(ctx context.Context, v Greeted) error {
			select {
			case got <- received{event: v, trace: trace.SpanContextFromContext(ctx).TraceID()}:
			default:
			}

			return nil
		})

		return &struct{}{}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	svc, err := backplane.Open(ctx, newState,
		standard.Drivers(), backplane.Name(watcher), backplane.Version("1.0.0"), backplane.KeepSlog(),
		backplane.Logger(xlog.NewJSON(xlog.WithWriter(io.Discard))),
		backplane.ConfigOptions(config.WithoutEnv(), config.WithoutConsul(), config.File(file)),
	)
	if err != nil {
		cancel()
		t.Fatal(err)
	}

	done := make(chan error, 1)

	go func() { done <- svc.Run(ctx) }()

	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Errorf("watcher: %v", err)
		}
	})
}

func awaitConsumer(t *testing.T, jet jetStream, durable string) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := jet.Consumer(t.Context(), "bp_hello", durable); err == nil {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("consumer %s not created", durable)
}

// runHello builds hello and runs it with NATS and without Consul.
func runHello(t *testing.T, url string) *env {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "hello")
	pkg := "github.com/backplane-labs/backplane/pkg/backplane/build"

	build := exec.Command("go", "build", "-o", bin,
		"-ldflags", "-X "+pkg+".Service="+service+" -X "+pkg+".Version="+version,
		"../examples/hello/cmd/hello")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build hello: %v\n%s", err, out)
	}

	platformPort, publicPort := freePort(t), freePort(t)
	e := &env{host: "127.0.0.1", platform: "127.0.0.1:" + platformPort, public: "127.0.0.1:" + publicPort}
	e.logs = &bytes.Buffer{}
	e.cmd = exec.Command(bin)

	e.cmd.Env = append(withoutConsul(os.Environ()),
		"BACKPLANE_NATS_URL="+url,
		"BACKPLANE_INSTANCE=conformance-events-1",
		"BACKPLANE_ADVERTISE=127.0.0.1",
		"BACKPLANE_INTERNAL_PORT="+platformPort,
		"BACKPLANE_PUBLIC_PORT="+publicPort,
		"HELLO_LEGACY_LISTEN=:"+freePort(t),
		"BACKPLANE_SHUTDOWN_DRAIN=100ms",
	)
	e.cmd.Stdout, e.cmd.Stderr = e.logs, e.logs

	if err := e.cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = e.cmd.Process.Signal(syscall.SIGTERM)
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

func withoutConsul(environ []string) []string {
	out := make([]string, 0, len(environ))

	for _, kv := range environ {
		if !strings.HasPrefix(kv, "BACKPLANE_CONSUL_") {
			out = append(out, kv)
		}
	}

	return out
}
