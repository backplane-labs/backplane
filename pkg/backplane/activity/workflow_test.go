package activity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	inftemporal "github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal/temporaltest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
)

// ship is a workflow-backed activity: it sleeps (durable timer), then
// answers; an empty order is final.
func ship(ctx workflow.Context, in Order) (Receipt, error) {
	if in.Items == 0 {
		return Receipt{}, activity.NonRetryable(errEmpty)
	}

	if err := workflow.Sleep(ctx, 10*time.Millisecond); err != nil {
		return Receipt{}, err
	}

	return Receipt{Total: in.Items * 7}, nil
}

// TestWorkflowActivity: a binding step runs the workflow-backed activity
// as a child workflow by name on the service's queue.
func TestWorkflowActivity(t *testing.T) {
	t.Parallel()

	svc := temporaltest.Name("wfact")
	e := env.New(svc, manifest.New(svc, "0.0.0"))
	root := link.Scope(node.New(svc, testlog.Discard(), e).Child(svc, node.Root, false)).(deps.Component)
	workflows.Activity(root, "Ship", ship)

	c := temporal.New(temporal.Params{
		Conn: inftemporal.Config{Addr: temporaltest.Addr(t)}, Service: svc, Instance: svc + "-1", Log: testlog.Discard(), Env: e,
	})
	g := temporaltest.NewGroup(t)

	if err := c.Connect(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	if err := c.StartWorker(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = c.StopWorker(context.Background())
		_ = c.Close(context.Background())
	})

	// The binding: a child workflow by name on the target's queue.
	tc := temporaltest.Dial(t)
	queue := "binding-" + svc
	binding := func(ctx workflow.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			TaskQueue: svc, WorkflowExecutionTimeout: 10 * time.Second,
		})

		var res backplanev1.ActivityResult

		err := workflow.ExecuteChildWorkflow(ctx, call.GetActivity(), call).Get(ctx, &res)

		return &res, err
	}

	temporaltest.Serve(t, tc, queue, func(w worker.Worker) {
		w.RegisterWorkflowWithOptions(binding, workflow.RegisterOptions{Name: "binding"})
	})

	run := func(payload string) (*backplanev1.ActivityResult, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()

		r, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{TaskQueue: queue}, "binding",
			&backplanev1.ActivityCall{Activity: "Ship", Payload: []byte(payload), Binding: svc + ".Deliver", Step: "ship"})
		if err != nil {
			t.Fatal(err)
		}

		var res backplanev1.ActivityResult

		err = r.Get(ctx, &res)

		return &res, err
	}

	if res, err := run(`{"items":3}`); err != nil || string(res.GetPayload()) != `{"total":21}` {
		t.Fatalf("ship: %s %v", res.GetPayload(), err)
	}

	_, err := run(`{"items":0}`)

	var app *sdktemporal.ApplicationError
	if !errors.As(err, &app) || !app.NonRetryable() || app.Message() != svc+".Ship: empty order" ||
		app.Type() != temporal.NonRetryableType {
		t.Fatalf("final error: %v", err)
	}

	if _, err := run(`"not an order"`); !errors.As(err, &app) || !app.NonRetryable() {
		t.Fatalf("bad payload: %v", err)
	}
}
