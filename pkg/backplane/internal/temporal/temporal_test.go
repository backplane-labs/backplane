package temporal_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/client"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	inftemporal "github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal/temporaltest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

const callLimit = 20 * time.Second

var errRude = errors.New("declined: rude")

type text struct {
	Text string `json:"text"`
	Name string `json:"name,omitempty"`
}

// service starts the SDK side of service on the dev server: connection
// and worker.
func service(t *testing.T, name string, declare func(e *env.Env)) *temporal.Client {
	t.Helper()

	e := env.New(name, manifest.New(name, "0.0.0"))
	declare(e)

	c := temporal.New(temporal.Params{
		Conn: inftemporal.Config{Addr: temporaltest.Addr(t)}, Service: name, Instance: name + "-1", Log: testlog.Discard(), Env: e,
	})
	c.UseFastRetry()

	g := temporaltest.NewGroup(t)

	if err := c.Connect(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	if err := c.StartHookWorker(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	if err := c.StartWorker(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = c.StopWorker(context.Background())
		_ = c.StopHookWorker(context.Background())
		_ = c.Close(context.Background())
	})

	return c
}

// runActivity plays a binding step: a workflow on its own queue executes
// the activity by name on the service's queue.
func runActivity(t *testing.T, tc client.Client, svc, name string, payload []byte) (*backplanev1.ActivityResult, error) {
	t.Helper()

	queue := "wf-" + svc
	step := func(ctx workflow.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			TaskQueue:           svc,
			StartToCloseTimeout: 5 * time.Second,
			RetryPolicy:         &sdktemporal.RetryPolicy{InitialInterval: 50 * time.Millisecond, MaximumAttempts: 3},
		})

		var res backplanev1.ActivityResult

		err := workflow.ExecuteActivity(ctx, call.GetActivity(), call).Get(ctx, &res)

		return &res, err
	}

	temporaltest.Serve(t, tc, queue, func(w worker.Worker) {
		w.RegisterWorkflowWithOptions(step, workflow.RegisterOptions{Name: "step"})
	})

	ctx, cancel := context.WithTimeout(t.Context(), callLimit)
	defer cancel()

	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{TaskQueue: queue}, "step",
		&backplanev1.ActivityCall{Activity: name, Payload: payload, Binding: svc + ".Test", Step: "one"})
	if err != nil {
		t.Fatal(err)
	}

	var res backplanev1.ActivityResult

	err = run.Get(ctx, &res)

	return &res, err
}

func TestActivityByName(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("act")

	var attempts, flaky atomic.Int32

	service(t, svc, func(e *env.Env) {
		e.Manifest.Activity(&backplanev1.Activity{Name: "Echo"})
		e.Activity("Echo", func(_ context.Context, in []byte) ([]byte, error) {
			attempts.Add(1)

			var v text
			if err := json.Unmarshal(in, &v); err != nil {
				return nil, env.NonRetryableError{Err: err}
			}

			switch v.Text {
			case "bad":
				return nil, env.NonRetryableError{Err: errors.New("bad input")}
			case "flaky":
				if flaky.Add(1) < 2 {
					return nil, errors.New("try again")
				}
			}

			return json.Marshal(text{Text: "echo: " + v.Text})
		})
	})

	tc := temporaltest.Dial(t)

	res, err := runActivity(t, tc, svc, "Echo", []byte(`{"text":"hi"}`))
	if err != nil || string(res.GetPayload()) != `{"text":"echo: hi"}` {
		t.Fatalf("echo: %s %v", res.GetPayload(), err)
	}

	res, err = runActivity(t, tc, svc, "Echo", []byte(`{"text":"flaky"}`))
	if err != nil || string(res.GetPayload()) != `{"text":"echo: flaky"}` || flaky.Load() != 2 {
		t.Fatalf("retried: %s %v (%d)", res.GetPayload(), err, flaky.Load())
	}

	before := attempts.Load()

	_, err = runActivity(t, tc, svc, "Echo", []byte(`{"text":"bad"}`))

	var app *sdktemporal.ApplicationError
	if !errors.As(err, &app) || !app.NonRetryable() || app.Message() != svc+".Echo: bad input" {
		t.Fatalf("non-retryable: %v", err)
	}

	if n := attempts.Load() - before; n != 1 {
		t.Fatalf("non-retryable error retried: %d attempts", n)
	}
}

// greet plays backplane's binding for <svc>.Greet from the envelope alone.
func greet(seen chan<- *backplanev1.HookCall) nexus.Operation[*backplanev1.HookCall, *backplanev1.HookResult] {
	return nexus.NewSyncOperation("Greet", func(
		ctx context.Context, call *backplanev1.HookCall, _ nexus.StartOperationOptions,
	) (*backplanev1.HookResult, error) {
		if seen != nil {
			seen <- call
		}

		var in text
		if err := json.Unmarshal(call.GetPayload(), &in); err != nil {
			return nil, nexus.NewOperationFailedErrorf("bad payload")
		}

		switch in.Name {
		case "nobody":
			return nil, sdktemporal.NewNonRetryableApplicationError(
				"no binding for "+call.GetHook(), temporal.NoBindingType, nil)
		case "rude":
			return nil, nexus.NewOperationFailedErrorf("%w", errRude)
		case "slow":
			<-ctx.Done()

			return nil, ctx.Err()
		}

		out, _ := json.Marshal(text{Text: "Hello, " + in.Name})

		return &backplanev1.HookResult{Payload: out}, nil
	})
}

func withHook(e *env.Env) { e.Manifest.Hook(&backplanev1.Hook{Name: "Greet"}) }

func TestCallOutsideWorkflow(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("call")
	c := service(t, svc, withHook)
	seen := make(chan *backplanev1.HookCall, 16)
	temporaltest.Hooks(t, temporaltest.Dial(t), svc, greet(seen))

	span := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4, 5, 6}, TraceFlags: trace.FlagsSampled,
	})

	ctx, cancel := context.WithTimeout(trace.ContextWithSpanContext(t.Context(), span), callLimit)
	defer cancel()

	out, err := c.Call(ctx, svc+".Greet", []byte(`{"name":"world"}`))
	if err != nil || string(out) != `{"text":"Hello, world"}` {
		t.Fatalf("call: %s %v", out, err)
	}

	call := <-seen
	if call.GetHook() != svc+".Greet" || call.GetInstance() != svc+"-1" || call.GetDeadline().AsDuration() <= 0 ||
		!strings.Contains(call.GetTrace()["traceparent"], span.TraceID().String()) {
		t.Fatalf("envelope: %v", call)
	}

	if _, err := c.Call(ctx, svc+".Greet", []byte(`{"name":"nobody"}`)); !errors.Is(err, env.ErrNoBinding) {
		t.Fatalf("no binding: %v", err)
	}

	if _, err := c.Call(ctx, svc+".Greet", []byte(`{"name":"rude"}`)); err == nil || err.Error() != "declined: rude" {
		t.Fatalf("failure: %v", err)
	}

	short, cancelShort := context.WithTimeout(t.Context(), time.Second)
	defer cancelShort()

	start := time.Now()

	if _, err := c.Call(short, svc+".Greet", []byte(`{"name":"slow"}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}

	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("deadline not respected: %v", took)
	}

	if _, err := c.Call(ctx, "other.Greet", nil); err == nil {
		t.Fatal("foreign hook name accepted")
	}
}

// Before backplane registers the endpoint a call fails at once instead of
// waiting out its deadline.
func TestCallWithoutEndpoint(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("noep")
	c := service(t, svc, withHook)

	start := time.Now()

	if _, err := c.Call(t.Context(), svc+".Greet", []byte(`{}`)); !errors.Is(err, env.ErrUnavailable) ||
		!strings.Contains(err.Error(), "no Nexus endpoint") {
		t.Fatalf("call: %v", err)
	}

	if took := time.Since(start); took > time.Second {
		t.Fatalf("call waited %v", took)
	}

	temporaltest.Hooks(t, temporaltest.Dial(t), svc, greet(nil))

	ctx, cancel := context.WithTimeout(t.Context(), callLimit)
	defer cancel()

	if out, err := c.Call(ctx, svc+".Greet", []byte(`{"name":"late"}`)); err != nil || string(out) != `{"text":"Hello, late"}` {
		t.Fatalf("after registration: %s %v", out, err)
	}
}

func TestUnreachable(t *testing.T) {
	t.Parallel()

	e := env.New("down", manifest.New("down", "0.0.0"))
	withHook(e)

	c := temporal.New(temporal.Params{Conn: inftemporal.Config{Addr: "127.0.0.1:1"}, Service: "down", Log: testlog.Discard(), Env: e})
	c.UseFastRetry()

	g := temporaltest.NewGroup(t)

	start := time.Now()

	if err := c.Connect(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	if err := c.StartWorker(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("start blocked %v", took)
	}

	if c.Connected() {
		t.Fatal("connected to nothing")
	}

	start = time.Now()

	if _, err := c.Call(t.Context(), "down.Greet", nil); !errors.Is(err, env.ErrUnavailable) {
		t.Fatalf("call: %v", err)
	}

	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("call blocked %v", took)
	}

	g.Stop()

	if err := c.StopWorker(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
