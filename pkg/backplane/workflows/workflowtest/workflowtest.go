package workflowtest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/nexus-rpc/sdk-go/nexus"
	tactivity "go.temporal.io/sdk/activity"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	internal "github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
)

// New creates a Temporal test environment (go.temporal.io/sdk/
// testsuite) set up as the service's worker is: every activity declared
// with activity.Handle or workflows.HandleActivity (by name, over the
// envelope), every workflow of
// workflows.Declare and workflows.Activity, and what workflows.Register
// registers. The service's hooks are a Nexus service of the environment
// answered by Answer, so workflows.CallHook works in workflow code; an
// unanswered hook fails with hook.ErrNoBinding, a failing answer with its
// message. Declare everything first; Start the harness first when the
// handlers use dependencies. An environment runs one workflow: call
// New again for the next.
//
//	w := workflowtest.New(t, h)
//	w.ExecuteWorkflow("Welcome", in)
//	err := w.GetWorkflowResult(&out)
func New(tb testing.TB, h *backplanetest.Harness) *testsuite.TestWorkflowEnvironment {
	tb.Helper()

	e, _ := decl.Env(h.Root(), "workflow test")

	var suite testsuite.WorkflowTestSuite

	suite.SetLogger(tlog.NewStructuredLogger(slog.New(slog.DiscardHandler)))

	w := suite.NewTestWorkflowEnvironment()

	for name, handler := range e.Activities() {
		w.RegisterActivityWithOptions(internal.ActivityFunc(e.Service, name, handler),
			tactivity.RegisterOptions{Name: name})
	}

	for name, handler := range e.OwnActivities() {
		w.RegisterActivityWithOptions(internal.ActivityFunc(e.Service, name, handler),
			tactivity.RegisterOptions{Name: name})
	}

	for _, register := range e.WorkerRegistrations() {
		register(w)
	}

	m, err := e.Manifest.Build()
	if err != nil {
		tb.Fatalf("backplanetest: manifest: %v", err)
	}

	if len(m.GetHooks()) > 0 {
		w.RegisterNexusService(hooks(tb, e, m.GetHooks()))
	}

	return w
}

// hooks is the Nexus service <service>.Hooks of the environment: one sync
// operation per declared hook, answered by the recorder. Failures are what
// backplane fails a hook call with.
func hooks(tb testing.TB, e *env.Env, declared []*backplanev1.Hook) *nexus.Service {
	tb.Helper()

	s := nexus.NewService(e.Service + internal.HooksSuffix)

	for _, d := range declared {
		full := e.Service + "." + d.GetName()
		operation := nexus.NewSyncOperation(d.GetName(), func(
			ctx context.Context, call *backplanev1.HookCall, _ nexus.StartOperationOptions,
		) (*backplanev1.HookResult, error) {
			out, err := e.Caller().Call(ctx, full, call.GetPayload())

			switch {
			case errors.Is(err, env.ErrUnavailable):
				return nil, temporal.NewNonRetryableApplicationError("no binding", internal.NoBindingType, nil)
			case err != nil:
				return nil, temporal.NewNonRetryableApplicationError(err.Error(), internal.HookFailedType, nil)
			}

			return &backplanev1.HookResult{Payload: out}, nil
		})

		if err := s.Register(operation); err != nil {
			tb.Fatalf("backplanetest: hook %s: %v", full, err)
		}
	}

	return s
}

// WorkflowActivity runs the workflow-backed activity name (workflows.Activity)
// in w as a binding step would start it, and returns its result. w is
// from New; it runs this one workflow.
func WorkflowActivity[Req, Res any](w *testsuite.TestWorkflowEnvironment, name string, in Req) (Res, error) {
	var out Res

	raw, err := decl.Encode(in)
	if err != nil {
		return out, fmt.Errorf("backplanetest: encode: %w", err)
	}

	w.ExecuteWorkflow(name, &backplanev1.ActivityCall{Activity: name, Payload: raw})

	if err := w.GetWorkflowError(); err != nil {
		return out, fmt.Errorf("backplanetest: %s: %w", name, err)
	}

	var res backplanev1.ActivityResult
	if err := w.GetWorkflowResult(&res); err != nil {
		return out, fmt.Errorf("backplanetest: %s result: %w", name, err)
	}

	if err := decl.Decode(res.GetPayload(), &out); err != nil {
		return out, fmt.Errorf("backplanetest: decode: %w", err)
	}

	return out, nil
}
