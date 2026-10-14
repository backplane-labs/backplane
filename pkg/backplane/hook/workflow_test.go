package hook_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/hook"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	internal "github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal/temporaltest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
)

// outcome is what the test workflow saw.
type outcome struct {
	Total     int    `json:"total"`
	NoBinding bool   `json:"no_binding"`
	Err       string `json:"err"`
}

// price plays backplane's binding of <svc>.Price: doubles the amount,
// declines negatives, has no binding for zero.
func price() nexus.Operation[*backplanev1.HookCall, *backplanev1.HookResult] {
	return nexus.NewSyncOperation("Price", func(
		_ context.Context, call *backplanev1.HookCall, _ nexus.StartOperationOptions,
	) (*backplanev1.HookResult, error) {
		var in Quote
		if err := json.Unmarshal(call.GetPayload(), &in); err != nil {
			return nil, nexus.NewOperationFailedErrorf("bad payload")
		}

		switch {
		case in.Amount == 0:
			return nil, temporal.NewNonRetryableApplicationError("no binding", internal.NoBindingType, nil)
		case in.Amount < 0:
			return nil, nexus.NewOperationFailedErrorf("%w", errDeclined)
		}

		out, _ := json.Marshal(Price{Total: in.Amount * 2})

		return &backplanev1.HookResult{Payload: out}, nil
	})
}

func TestWorkflowCall(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("wfcall")
	e := env.New(svc, manifest.New(svc, "0.0.0"))
	app := node.New(svc, testlog.Discard(), e).Child(svc, node.Root, false)
	root := link.Scope(app).(deps.Component)
	ref := hook.Declare[Quote, Price](root, "Price")

	tc := temporaltest.Dial(t)
	temporaltest.Hooks(t, tc, svc, price())

	// A workflow returns an error last, whether it uses it or not.
	quote := func(ctx workflow.Context, amount int) (outcome, error) { //nolint:unparam // workflow signature
		out, err := workflows.CallHook(ctx, ref, Quote{Amount: amount})
		if err != nil {
			return outcome{NoBinding: errors.Is(err, hook.ErrNoBinding), Err: err.Error()}, nil
		}

		return outcome{Total: out.Total}, nil
	}

	queue := "wf-" + svc
	temporaltest.Serve(t, tc, queue, func(w worker.Worker) {
		w.RegisterWorkflowWithOptions(quote, workflow.RegisterOptions{Name: "quote"})
	})

	for _, step := range []struct {
		amount int
		want   outcome
	}{
		{21, outcome{Total: 42}},
		{0, outcome{NoBinding: true, Err: "hook " + svc + ".Price: no binding"}},
		{-1, outcome{Err: "hook " + svc + ".Price: declined"}},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)

		run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			TaskQueue: queue, WorkflowExecutionTimeout: 15 * time.Second,
		}, "quote", step.amount)
		if err != nil {
			cancel()
			t.Fatal(err)
		}

		var got outcome

		err = run.Get(ctx, &got)

		cancel()

		if err != nil || got != step.want {
			t.Errorf("amount %d: %+v %v, want %+v", step.amount, got, err, step.want)
		}
	}
}

func TestWorkflowCallZeroRef(t *testing.T) {
	t.Parallel()

	var ref hook.Ref[Quote, Price]

	// Undeclared: fails before touching the workflow context.
	if _, err := workflows.CallHook(nil, ref, Quote{}); !errors.Is(err, hook.ErrUnavailable) {
		t.Fatalf("zero ref: %v", err)
	}
}

// deadlines plays the binding of <svc>.<name>: it answers with the
// deadline the call brought, in milliseconds.
func deadlines(name string) nexus.Operation[*backplanev1.HookCall, *backplanev1.HookResult] {
	return nexus.NewSyncOperation(name, func(
		_ context.Context, call *backplanev1.HookCall, _ nexus.StartOperationOptions,
	) (*backplanev1.HookResult, error) {
		out, _ := json.Marshal(Price{Total: int(call.GetDeadline().AsDuration().Milliseconds())})

		return &backplanev1.HookResult{Payload: out}, nil
	})
}

// workflows.CallHook is never unbounded: the call's Timeout, else the declared
// default, else the platform default — cut to what is left of the run.
func TestWorkflowCallDeadline(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("wfdl")
	e := env.New(svc, manifest.New(svc, "0.0.0"))
	e.SetHookTimeout(9 * time.Second)

	app := node.New(svc, testlog.Discard(), e).Child(svc, node.Root, false)
	root := link.Scope(app).(deps.Component)
	declared := hook.Declare[Quote, Price](root, "Probe", hook.DefaultTimeout(7*time.Second))
	platform := hook.Declare[Quote, Price](root, "Bare")

	tc := temporaltest.Dial(t)
	temporaltest.Hooks(t, tc, svc, deadlines("Probe"), deadlines("Bare"))

	probe := func(ctx workflow.Context, which string) (int, error) {
		var (
			out Price
			err error
		)

		switch which {
		case "platform":
			out, err = workflows.CallHook(ctx, platform, Quote{})
		case "declared":
			out, err = workflows.CallHook(ctx, declared, Quote{})
		default:
			out, err = workflows.CallHook(ctx, declared, Quote{}, hook.Timeout(3*time.Second))
		}

		return out.Total, err
	}

	queue := "wf-" + svc
	temporaltest.Serve(t, tc, queue, func(w worker.Worker) {
		w.RegisterWorkflowWithOptions(probe, workflow.RegisterOptions{Name: "probe"})
	})

	for _, step := range []struct {
		which    string
		run      time.Duration // workflow run timeout, 0 none
		min, max time.Duration
	}{
		{"platform", 0, 9 * time.Second, 9 * time.Second},
		{"declared", 0, 7 * time.Second, 7 * time.Second},
		{"call", 0, 3 * time.Second, 3 * time.Second},
		{"declared", 5 * time.Second, 4 * time.Second, 5 * time.Second},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)

		run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{TaskQueue: queue, WorkflowRunTimeout: step.run},
			"probe", step.which)
		if err != nil {
			cancel()
			t.Fatal(err)
		}

		var millis int

		err = run.Get(ctx, &millis)

		cancel()

		got := time.Duration(millis) * time.Millisecond
		if err != nil || got < step.min || got > step.max {
			t.Errorf("%s (run %v): deadline %v %v, want %v..%v", step.which, step.run, got, err, step.min, step.max)
		}
	}
}
