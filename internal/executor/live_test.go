package executor_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/types/known/durationpb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/executor"
	"github.com/backplane-labs/backplane/internal/ops"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	sdkconfig "github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
)

// stack is the executor against a dev Temporal (BACKPLANE_TEST_TEMPORAL)
// and PostgreSQL (BACKPLANE_TEST_PG): the Manager saving bindings, a
// registry Hub with the services, backplane's queue served by a worker
// with the binding and call workflows, and a service worker with the
// activities by name. Names are random so runs do not meet.
type stack struct {
	tc    client.Client
	hub   *registry.Hub
	mgr   *bindings.Manager
	x     *executor.Executor
	queue string
	// caller raises hooks Greet (required), Ping (optional), Unbound
	// (required); worker implements Echo.
	caller, worker string

	mu       sync.Mutex
	services map[string]registry.Service
}

func random(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)

	return prefix + "-" + hex.EncodeToString(b)
}

func newStack(t *testing.T) *stack {
	t.Helper()

	addr, dsn := os.Getenv("BACKPLANE_TEST_TEMPORAL"), os.Getenv("BACKPLANE_TEST_PG")
	if addr == "" || dsn == "" {
		t.Skip("BACKPLANE_TEST_TEMPORAL and BACKPLANE_TEST_PG not set")
	}

	tc, err := client.DialContext(t.Context(), client.Options{
		HostPort: addr, Logger: tlog.NewStructuredLogger(slog.New(slog.DiscardHandler)),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(tc.Close)

	s := &stack{
		tc: tc, hub: registry.NewHub(), queue: random("bpx"), caller: random("caller"), worker: random("worker"),
		services: map[string]registry.Service{},
	}

	s.publish(&backplanev1.Manifest{
		Service: s.caller, Version: "1.0.0",
		Hooks: []*backplanev1.Hook{{Name: "Greet", Required: true}, {Name: "Ping"}, {Name: "Unbound", Required: true}},
	}, &backplanev1.Manifest{
		Service: s.worker, Version: "1.0.0",
		Activities: []*backplanev1.Activity{{Name: "Echo", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY}},
	})

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: sdkconfig.Secret(dsn)}))
	s.mgr = bindings.New(h.Root(), st, s.hub, bindings.PollInterval(100*time.Millisecond))

	runs := ops.Detached(s.hub, ops.WithTemporal(func() (client.Client, error) { return tc, nil }, s.queue)).Workflows()
	s.x = executor.New(h.Root(), s.mgr, s.hub,
		executor.WithTemporal(func() (client.Client, error) { return tc, nil }, s.queue),
		executor.Settle(0), executor.Resync(time.Second), executor.AbsenceGrace(0), executor.Runs(runs))

	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	// backplane's main worker: the binding workflow and the call workflows.
	serve(t, tc, s.queue, func(w worker.Worker) {
		executor.RegisterWorkflow(w)
		ops.RegisterWorkflows(w)
	})

	// The service implementing Echo: {text} -> {text}.
	serve(t, tc, wire.Queue(s.worker), func(w worker.Worker) {
		w.RegisterActivityWithOptions(func(_ context.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
			var in struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(call.GetPayload(), &in); err != nil {
				return nil, temporal.NewNonRetryableApplicationError(err.Error(), wire.NonRetryableType, nil)
			}

			if strings.HasSuffix(in.Text, "fail") {
				return nil, temporal.NewNonRetryableApplicationError(s.worker+".Echo: refused", wire.NonRetryableType, nil)
			}

			out, _ := json.Marshal(map[string]string{"text": in.Text, "step": call.GetStep(), "binding": call.GetBinding()})

			return &backplanev1.ActivityResult{Payload: out}, nil
		}, activity.RegisterOptions{Name: "Echo"})
	})

	t.Cleanup(func() {
		ctx := context.Background()
		pool := st.Get().Pool
		_, _ = pool.Exec(ctx, "DELETE FROM backplane.binding_current WHERE hook LIKE $1", s.caller+".%")
		_, _ = pool.Exec(ctx, "DELETE FROM backplane.binding_version WHERE hook LIKE $1", s.caller+".%")

		s.mu.Lock()
		defer s.mu.Unlock()

		for name := range s.services {
			deleteEndpoint(tc, name)
		}
	})

	return s
}

func serve(t *testing.T, c client.Client, queue string, register func(w worker.Worker)) {
	t.Helper()

	w := worker.New(c, queue, worker.Options{})
	register(w)

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(w.Stop)
}

func deleteEndpoint(c client.Client, name string) {
	ctx := context.Background()

	res, err := c.OperatorService().ListNexusEndpoints(ctx, &operatorservice.ListNexusEndpointsRequest{Name: name, PageSize: 1})
	if err != nil || len(res.GetEndpoints()) == 0 {
		return
	}

	e := res.GetEndpoints()[0]
	_, _ = c.OperatorService().DeleteNexusEndpoint(ctx, &operatorservice.DeleteNexusEndpointRequest{
		Id: e.GetId(), Version: e.GetVersion(),
	})
}

// publish adds manifests to the registry.
func (s *stack) publish(ms ...*backplanev1.Manifest) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, m := range ms {
		s.services[m.GetService()] = registry.Service{
			Name: m.GetService(), Manifests: map[string]*backplanev1.Manifest{m.GetVersion(): m},
			Instances: []registry.Instance{{ID: "live", Registered: true}},
		}
	}

	s.hub.Publish(clone(s.services))
}

func clone(in map[string]registry.Service) map[string]registry.Service {
	out := make(map[string]registry.Service, len(in))
	for k, v := range in {
		out[k] = v
	}

	return out
}

// endpoint waits for the Nexus endpoint name targeting the stack's queue.
func (s *stack) endpoint(t *testing.T, name string) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		res, err := s.tc.OperatorService().ListNexusEndpoints(t.Context(), &operatorservice.ListNexusEndpointsRequest{
			Name: name, PageSize: 1,
		})
		if err == nil && len(res.GetEndpoints()) == 1 &&
			res.GetEndpoints()[0].GetSpec().GetTarget().GetWorker().GetTaskQueue() == s.queue {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("no endpoint %s", name)
}

// call raises hook through Nexus from a workflow, as a service does.
func (s *stack) call(t *testing.T, hook, input string) (string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	run, err := s.tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: "hook/" + hook + "/" + uuid.NewString(), TaskQueue: s.queue, WorkflowExecutionTimeout: 25 * time.Second,
	}, ops.CallHookWorkflow, &backplanev1.HookCall{
		Hook: hook, Instance: "test", Payload: []byte(input), Deadline: durationpb.New(20 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	var res backplanev1.HookResult
	if err := run.Get(ctx, &res); err != nil {
		return "", err
	}

	return string(res.GetPayload()), nil
}

func errType(err error) string {
	var app *temporal.ApplicationError
	if errors.As(err, &app) {
		return app.Type() + ": " + app.Message()
	}

	return err.Error()
}

// A hook call goes through Nexus to backplane's handler, which runs the
// saved binding: a step on the service's queue, the result back.
//
//nolint:paralleltest // one database, one Temporal
func TestLiveHookThroughBinding(t *testing.T) {
	s := newStack(t)
	ctx := t.Context()
	greet := s.caller + ".Greet"

	saved, err := s.mgr.Save(ctx, bindings.Binding{
		Hook: greet,
		Steps: []bindings.Step{{
			Name: "echo", Activity: s.worker + ".Echo",
			Input: object(t, `{"text": "\"Hello, \" + req.name"}`),
		}},
		Result: object(t, `{"text": "echo.text", "step": "echo.step", "binding": "echo.binding"}`),
	}, "live", bindings.Base{})
	if err != nil || len(saved.Violations) > 0 {
		t.Fatalf("save: %v %v", saved.Violations, err)
	}

	s.endpoint(t, s.caller)

	out, err := s.call(t, greet, `{"name":"live"}`)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"binding":"` + greet + `@1","step":"echo","text":"Hello, live"}`
	if out != want {
		t.Errorf("result:\n got %s\nwant %s", out, want)
	}

	// No binding: required -> backplane.NoBinding; optional -> {}.
	if _, err := s.call(t, s.caller+".Unbound", `{}`); err == nil || errType(err) != wire.NoBindingType+": no binding for "+
		s.caller+".Unbound" {
		t.Errorf("required unbound: %v", err)
	}

	if ping, err := s.call(t, s.caller+".Ping", `{}`); err != nil || ping != "{}" {
		t.Errorf("optional unbound: %q %v", ping, err)
	}

	// A failing step: the readable error.
	if _, err := s.call(t, greet, `{"name":"fail"}`); err == nil {
		t.Error("failing step succeeded")
	}

	// The console sees the runs and their steps.
	api := s.x.BindingService(s.mgr.BindingAPI())

	var runs *consolev1.ListBindingRunsResponse

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if runs, err = api.ListBindingRuns(ctx, &consolev1.ListBindingRunsRequest{Hook: greet}); err == nil &&
			len(runs.GetRuns()) >= 2 {
			break
		}

		time.Sleep(200 * time.Millisecond)
	}

	if len(runs.GetRuns()) < 2 {
		t.Fatalf("runs: %v %v", runs, err)
	}

	for _, r := range runs.GetRuns() {
		got, err := api.GetBindingRun(ctx, &consolev1.GetBindingRunRequest{WorkflowId: r.GetWorkflowId()})
		if err != nil {
			t.Fatal(err)
		}

		if got.GetHook() != greet || got.GetVersion() != 1 || len(got.GetSteps()) != 1 ||
			got.GetSteps()[0].GetActivity() != s.worker+".Echo" {
			t.Errorf("run %s: %v", r.GetWorkflowId(), got)
		}
	}

	// A test of an unsaved definition.
	test, err := api.TestBinding(ctx, &consolev1.TestBindingRequest{
		Definition: bindings.Binding{
			Hook: greet,
			Steps: []bindings.Step{{
				Name: "shout", Activity: s.worker + ".Echo",
				Input: object(t, `{"text": "req.name.upperAscii()"}`),
			}},
			Result: bindings.Expr("shout.text"),
		}.PB(),
		Input: `{"name":"draft"}`,
	})
	if err != nil || test.GetResult().GetOutput() != `"DRAFT"` || test.GetVersion() != 0 {
		t.Errorf("test: %v %v", test, err)
	}
}

// A service that appears later gets its endpoint and is served: the Nexus
// worker is rebuilt with the new set.
//
//nolint:paralleltest // one database, one Temporal
func TestLiveNewHookService(t *testing.T) {
	s := newStack(t)
	s.endpoint(t, s.caller)

	later := random("later")
	s.publish(&backplanev1.Manifest{Service: later, Version: "1.0.0", Hooks: []*backplanev1.Hook{{Name: "Hello"}}})
	s.endpoint(t, later)

	deadline := time.Now().Add(20 * time.Second)

	for {
		out, err := s.call(t, later+".Hello", `{}`)
		if err == nil && out == "{}" {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("new service: %q %v", out, err)
		}

		time.Sleep(200 * time.Millisecond)
	}
}

//nolint:paralleltest // live Temporal and PostgreSQL
func TestLiveRetiredHookServiceReturns(t *testing.T) {
	s := newStack(t)
	s.endpoint(t, s.caller)
	s.mu.Lock()
	gone := s.services[s.caller]
	gone.Instances = nil
	s.services[s.caller] = gone
	s.hub.Publish(clone(s.services))
	s.mu.Unlock()

	deadline := time.Now().Add(20 * time.Second)

	for {
		res, err := s.tc.OperatorService().ListNexusEndpoints(t.Context(), &operatorservice.ListNexusEndpointsRequest{Name: s.caller, PageSize: 1})
		if err == nil && len(res.GetEndpoints()) == 0 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("endpoint not retired: %v %v", res, err)
		}

		time.Sleep(100 * time.Millisecond)
	}
	// The old manifest stays. The absence must not cause delete/recreate loops.
	if err := s.x.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	res, err := s.tc.OperatorService().ListNexusEndpoints(t.Context(), &operatorservice.ListNexusEndpointsRequest{Name: s.caller, PageSize: 1})
	if err != nil || len(res.GetEndpoints()) != 0 {
		t.Fatalf("retained manifest recreated endpoint: %v %v", res, err)
	}

	s.publish(gone.Manifests["1.0.0"])
	s.endpoint(t, s.caller)

	if out, err := s.call(t, s.caller+".Ping", `{}`); err != nil || out != "{}" {
		t.Fatalf("returned hook: %q %v", out, err)
	}
}

// object is a Value from its JSON form.
func object(t *testing.T, text string) bindings.Value {
	t.Helper()

	v, err := bindings.ParseValue([]byte(text))
	if err != nil {
		t.Fatal(err)
	}

	return v
}
