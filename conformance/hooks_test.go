package conformance_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nexus-rpc/sdk-go/nexus"
	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/temporalnexus"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/standard"
	"github.com/backplane-labs/backplane/pkg/backplane/hook"
)

// The hook and activity of hello, declared by a service of their own so
// every run has a unique service name (endpoint, queue).
type (
	greetIn struct {
		Name string `json:"name"`
	}
	greetOut struct {
		Text string `json:"text"`
	}
	echoIn struct {
		Text string `json:"text"`
	}
	echoOut struct {
		Text string `json:"text"`
	}
)

type hooksConfig struct {
	config.Backplane `json:"backplane"`
}

type hooksState struct {
	Greet hook.Ref[greetIn, greetOut]
}

// Contract with backplane: the application error type of a missing binding.
const noBindingType = "backplane.NoBinding"

// TestHookThroughBinding: a service raises its hook Greet from an HTTP
// handler; the test plays backplane — Nexus endpoint <service> and a
// binding <service>.Greet := <service>.Echo run as a workflow that calls the
// service's activity Echo by name on the service's queue.
//
//nolint:paralleltest // sets the environment
func TestHookThroughBinding(t *testing.T) {
	addr := os.Getenv("BACKPLANE_TEST_TEMPORAL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_TEMPORAL not set (make up)")
	}

	name := uniqueName("conf")
	playBackplane(t, addr, name)
	public := runService(t, addr, name)

	if code, body := httpGet("http://" + public + "/greet/?name=conformance"); code != http.StatusOK ||
		strings.TrimSpace(body) != "Hello, conformance" {
		t.Errorf("bound hook: %d %q", code, body)
	}

	if code, body := httpGet("http://" + public + "/greet/?name=nobody"); code != http.StatusBadGateway ||
		strings.TrimSpace(body) != "hook "+name+".Greet: no binding" {
		t.Errorf("no binding: %d %q", code, body)
	}
}

func uniqueName(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)

	return prefix + "-" + hex.EncodeToString(b)
}

// runService opens the service on free ports with Temporal at addr and
// runs it until the test ends; it returns the public host:port.
func runService(t *testing.T, addr, name string) string {
	t.Helper()

	platform, public := freePort(t), freePort(t)

	t.Setenv("BACKPLANE_TEMPORAL_ADDR", addr)
	t.Setenv("BACKPLANE_CONSUL_ADDR", "")
	t.Setenv("BACKPLANE_INTERNAL_PORT", platform)
	t.Setenv("BACKPLANE_PUBLIC_PORT", public)
	t.Setenv("BACKPLANE_SHUTDOWN_DRAIN", "0s")

	logs := &syncBuffer{}

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[hooksConfig]) (*hooksState, error) {
		activity.Handle(root, "Echo", func(_ context.Context, in echoIn) (echoOut, error) { return echoOut(in), nil })

		return &hooksState{Greet: hook.Declare[greetIn, greetOut](root, "Greet", hook.Required())}, nil
	},
		standard.Drivers(), backplane.Name(name), backplane.Instance(name+"-1"), backplane.Advertise("127.0.0.1"),
		backplane.Logger(xlog.NewJSON(xlog.WithWriter(logs))), backplane.ConfigOptions(config.WithoutFile()),
	)
	if err != nil {
		t.Fatal(err)
	}

	st := svc.State()
	svc.HTTP("/greet/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := st.Greet.Call(r.Context(), greetIn{Name: r.URL.Query().Get("name")})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)

			return
		}

		fmt.Fprintln(w, out.Text)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- svc.Run(ctx) }()

	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Errorf("run: %v", err)
		}

		if t.Failed() {
			t.Logf("service logs:\n%s", logs)
		}
	})

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if code, _ := httpGet("http://127.0.0.1:" + platform + "/healthz/readiness"); code == http.StatusOK {
			return "127.0.0.1:" + public
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("service not ready")

	return ""
}

// playBackplane registers the Nexus endpoint <name> and serves
// <name>.Hooks on a queue of its own, as backplane does.
func playBackplane(t *testing.T, addr, name string) {
	t.Helper()

	tc, err := client.DialContext(t.Context(), client.Options{
		HostPort: addr, Logger: tlog.NewStructuredLogger(slog.New(slog.DiscardHandler)),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(tc.Close)

	queue := "backplane-" + name
	hooks := nexus.NewService(name + ".Hooks")

	greet := temporalnexus.NewWorkflowRunOperation("Greet", bindGreet, func(
		_ context.Context, call *backplanev1.HookCall, opts nexus.StartOperationOptions,
	) (client.StartWorkflowOptions, error) {
		return client.StartWorkflowOptions{ID: "binding/" + call.GetHook() + "/" + opts.RequestID}, nil
	})
	if err := hooks.Register(greet); err != nil {
		t.Fatal(err)
	}

	w := worker.New(tc, queue, worker.Options{})
	w.RegisterNexusService(hooks)
	w.RegisterWorkflow(bindGreet)

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(w.Stop)

	res, err := tc.OperatorService().CreateNexusEndpoint(t.Context(), &operatorservice.CreateNexusEndpointRequest{
		Spec: &nexuspb.EndpointSpec{
			Name: name,
			Target: &nexuspb.EndpointTarget{Variant: &nexuspb.EndpointTarget_Worker_{
				Worker: &nexuspb.EndpointTarget_Worker{Namespace: "default", TaskQueue: queue},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = tc.OperatorService().DeleteNexusEndpoint(context.Background(), &operatorservice.DeleteNexusEndpointRequest{
			Id: res.GetEndpoint().GetId(), Version: res.GetEndpoint().GetVersion(),
		})
	})
}

var errBadHook = errors.New("bad hook name")

// bindGreet is the binding <service>.Greet := <service>.Echo: one step,
// the activity by name on the target service's queue, JSON in and out.
// "nobody" has no binding.
func bindGreet(ctx workflow.Context, call *backplanev1.HookCall) (*backplanev1.HookResult, error) {
	service, _, ok := strings.Cut(call.GetHook(), ".")
	if !ok {
		return nil, errBadHook
	}

	var in greetIn
	if err := json.Unmarshal(call.GetPayload(), &in); err != nil {
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), "backplane.BadInput", nil)
	}

	if in.Name == "nobody" {
		return nil, temporal.NewNonRetryableApplicationError("no binding for "+call.GetHook(), noBindingType, nil)
	}

	// A binding without steps: answers without the service's worker.
	if direct, isDirect := strings.CutPrefix(in.Name, "direct:"); isDirect {
		result, err := json.Marshal(greetOut{Text: "Hello, " + direct})
		if err != nil {
			return nil, fmt.Errorf("result: %w", err)
		}

		return &backplanev1.HookResult{Payload: result}, nil
	}

	payload, err := json.Marshal(echoIn{Text: "Hello, " + in.Name})
	if err != nil {
		return nil, fmt.Errorf("step echo: %w", err)
	}

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: service, StartToCloseTimeout: 10 * time.Second,
	})

	var res backplanev1.ActivityResult

	err = workflow.ExecuteActivity(ctx, "Echo", &backplanev1.ActivityCall{
		Activity: "Echo", Payload: payload, Trace: call.GetTrace(), Binding: call.GetHook(), Step: "echo",
	}).Get(ctx, &res)
	if err != nil {
		return nil, fmt.Errorf("step echo: %w", err)
	}

	var out echoOut
	if err := json.Unmarshal(res.GetPayload(), &out); err != nil {
		return nil, fmt.Errorf("step echo: %w", err)
	}

	result, err := json.Marshal(greetOut(out))
	if err != nil {
		return nil, fmt.Errorf("result: %w", err)
	}

	return &backplanev1.HookResult{Payload: result}, nil
}

// syncBuffer is a log sink safe for concurrent writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
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

// TestHookFromLifecycle: a component raises its hook from OnStart — before
// the service's worker starts — and from OnStop — after it stopped: the
// hooks worker carries both. The same key twice runs the binding once.
func TestHookFromLifecycle(t *testing.T) {
	addr := os.Getenv("BACKPLANE_TEST_TEMPORAL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_TEMPORAL not set (make up)")
	}

	name := uniqueName("life")
	playBackplane(t, addr, name)

	t.Setenv("BACKPLANE_TEMPORAL_ADDR", addr)
	t.Setenv("BACKPLANE_CONSUL_ADDR", "")
	t.Setenv("BACKPLANE_INTERNAL_PORT", freePort(t))
	t.Setenv("BACKPLANE_PUBLIC_PORT", freePort(t))
	t.Setenv("BACKPLANE_SHUTDOWN_DRAIN", "0s")

	var (
		seenMu sync.Mutex
		seen   = map[string]string{}
	)

	record := func(what string, out greetOut, err error) {
		seenMu.Lock()
		defer seenMu.Unlock()

		if err != nil {
			seen[what] = "error: " + err.Error()
		} else {
			seen[what] = out.Text
		}
	}

	logs := &syncBuffer{}

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[hooksConfig]) (*hooksState, error) {
		greet := hook.Declare[greetIn, greetOut](root, "Greet", hook.DefaultTimeout(15*time.Second))
		activity.Handle(root, "Echo", func(_ context.Context, in echoIn) (echoOut, error) { return echoOut(in), nil })

		root.OnStart(func(ctx context.Context) error {
			out, err := greet.Call(ctx, greetIn{Name: "direct:start"})
			record("start", out, err)

			out, err = greet.Call(ctx, greetIn{Name: "direct:keyed"}, hook.Key("once"))
			record("keyed", out, err)

			out, err = greet.Call(ctx, greetIn{Name: "direct:ignored"}, hook.Key("once"))
			record("keyed again", out, err)

			return nil
		})
		root.OnStop(func(ctx context.Context) error {
			out, err := greet.Call(ctx, greetIn{Name: "direct:stop"})
			record("stop", out, err)

			return nil
		})

		return &hooksState{Greet: greet}, nil
	},
		standard.Drivers(), backplane.Name(name), backplane.Instance(name+"-1"), backplane.Advertise("127.0.0.1"),
		backplane.Logger(xlog.NewJSON(xlog.WithWriter(logs))), backplane.ConfigOptions(config.WithoutFile()),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- svc.Run(ctx) }()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		seenMu.Lock()
		_, started := seen["keyed again"]
		seenMu.Unlock()

		if started {
			break
		}

		time.Sleep(50 * time.Millisecond)
	}

	cancel()

	if err := <-done; err != nil {
		t.Errorf("run: %v", err)
	}

	seenMu.Lock()
	defer seenMu.Unlock()

	for what, want := range map[string]string{
		"start": "Hello, start", "keyed": "Hello, keyed", "keyed again": "Hello, keyed", "stop": "Hello, stop",
	} {
		if seen[what] != want {
			t.Errorf("%s: %q, want %q", what, seen[what], want)
		}
	}

	if t.Failed() {
		t.Logf("service logs:\n%s", logs)
	}
}
