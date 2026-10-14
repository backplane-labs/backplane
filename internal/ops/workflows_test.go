package ops_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/ops"
)

// TestRunActivityWorkflow: the console's activity run calls the activity
// by name with the ActivityCall envelope, retries as asked and fails with
// the handler's message and type.
func TestRunActivityWorkflow(t *testing.T) {
	t.Parallel()

	var (
		suite    testsuite.WorkflowTestSuite
		attempts int
	)

	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(ops.RunActivity, workflow.RegisterOptions{Name: ops.RunActivityWorkflow})
	env.RegisterActivityWithOptions(func(_ context.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		attempts++

		if call.GetActivity() != "hello.Echo" || call.GetBinding() != "console" || call.GetStep() != "try" {
			return nil, temporal.NewNonRetryableApplicationError("bad envelope", "test", nil)
		}

		if attempts < 2 {
			return nil, errors.New("hello.Echo: flaky")
		}

		return &backplanev1.ActivityResult{Payload: call.GetPayload()}, nil
	}, activity.RegisterOptions{Name: "Echo"})

	env.ExecuteWorkflow(ops.RunActivityWorkflow, ops.ActivityRun{
		Service: "hello", Name: "Echo", Payload: []byte(`{"a":1}`), Step: "try",
		StartToClose: time.Second, MaxAttempts: 2,
	})

	var res backplanev1.ActivityResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatal(err)
	}

	if string(res.GetPayload()) != `{"a":1}` || attempts != 2 {
		t.Fatalf("result %s after %d attempts", res.GetPayload(), attempts)
	}
}

func TestRunActivityWorkflowFailure(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite

	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(ops.RunActivity, workflow.RegisterOptions{Name: ops.RunActivityWorkflow})
	env.RegisterActivityWithOptions(func(context.Context, *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		return nil, temporal.NewNonRetryableApplicationError("hello.Echo: refused", "backplane.NonRetryable", nil)
	}, activity.RegisterOptions{Name: "Echo"})

	env.ExecuteWorkflow(ops.RunActivityWorkflow, ops.ActivityRun{Service: "hello", Name: "Echo", StartToClose: time.Second})

	var app *temporal.ApplicationError
	if err := env.GetWorkflowError(); !errors.As(err, &app) || app.Message() != "hello.Echo: refused" ||
		app.Type() != "backplane.NonRetryable" {
		t.Fatalf("failure: %v", err)
	}
}

// TestRunActivityWorkflowChild: an activity of kind WORKFLOW runs as a
// child workflow by name with the same envelope.
func TestRunActivityWorkflowChild(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite

	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(ops.RunActivity, workflow.RegisterOptions{Name: ops.RunActivityWorkflow})
	env.RegisterWorkflowWithOptions(func(_ workflow.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		return &backplanev1.ActivityResult{Payload: []byte(`"` + call.GetActivity() + `"`)}, nil
	}, workflow.RegisterOptions{Name: "Flow"})

	env.ExecuteWorkflow(ops.RunActivityWorkflow, ops.ActivityRun{
		Service: "hello", Name: "Flow", Workflow: true, StartToClose: time.Minute,
	})

	var res backplanev1.ActivityResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatal(err)
	}

	if string(res.GetPayload()) != `"hello.Flow"` {
		t.Fatalf("result %s", res.GetPayload())
	}
}
