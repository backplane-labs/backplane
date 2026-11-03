package workflows

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"

	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
)

// ActivityOf is activity name of service, another service of the same
// product, for workflow code to run on service's task queue. Its Execute is
// HandleActivity's: the owner registers name with activity.Handle or
// HandleActivity, so the payloads match. Services of one product are
// released together and may call each other this way; a product reaches an
// integration (courier, templates, kratos, ...) only through hooks, which
// the installation binds. A malformed service or name panics.
//
//	charge := workflows.ActivityOf[billing.ChargeIn, billing.ChargeOut]("billing", "ChargeCard")
//	receipt, err := charge.Execute(ctx, in) // ctx with workflow.WithActivityOptions
func ActivityOf[Req, Res any](service, name string) ActivityRef[Req, Res] {
	decl.CheckService("activity "+name, service)
	decl.CheckName("activity", name)

	return ActivityRef[Req, Res]{name: name, queue: wire.Queue(service)}
}

// WorkflowRef is workflow name of another service of the same product
// (WorkflowOf), run on that service's task queue.
type WorkflowRef[Req, Res any] struct {
	name  string
	queue string
}

// WorkflowOf is workflow name of service, another service of the same
// product, which declares it (Declare): its input and output go through
// Temporal's default converter, as there. A malformed service or name
// panics.
//
//	ship := workflows.WorkflowOf[orders.ShipIn, orders.ShipOut]("orders", "Ship")
//	out, err := ship.ExecuteChild(ctx, in)               // from workflow code
//	run, err := ship.Start(ctx, c, in, client.StartWorkflowOptions{ID: "ship-" + id}) // from elsewhere
func WorkflowOf[Req, Res any](service, name string) WorkflowRef[Req, Res] {
	decl.CheckService("workflow "+name, service)
	decl.CheckName("workflow", name)

	return WorkflowRef[Req, Res]{name: name, queue: wire.Queue(service)}
}

// Name is the workflow's Temporal type.
func (r WorkflowRef[Req, Res]) Name() string { return r.name }

// Queue is the owner's task queue.
func (r WorkflowRef[Req, Res]) Queue() string { return r.queue }

// ExecuteChild runs the workflow as a child of the calling workflow, on the
// owner's queue, under ctx's child options (workflow.WithChildOptions: its
// id, timeouts, parent close policy), and waits for its result.
func (r WorkflowRef[Req, Res]) ExecuteChild(ctx workflow.Context, in Req) (Res, error) {
	var out Res

	opts := workflow.GetChildWorkflowOptions(ctx)
	opts.TaskQueue = r.queue

	ctx = workflow.WithChildOptions(ctx, opts)
	if err := workflow.ExecuteChildWorkflow(ctx, r.name, in).Get(ctx, &out); err != nil {
		return out, fmt.Errorf("workflow %s: %w", r.name, temporal.Local(err))
	}

	return out, nil
}

// Start starts the workflow on the owner's queue with c (Client), from
// outside workflow code; opts.TaskQueue is set to it. Read the result with
// the run's Get.
//
//nolint:ireturn // Temporal's run handle is an interface
func (r WorkflowRef[Req, Res]) Start(
	ctx context.Context, c client.Client, in Req, opts client.StartWorkflowOptions,
) (client.WorkflowRun, error) {
	opts.TaskQueue = r.queue

	run, err := c.ExecuteWorkflow(ctx, opts, r.name, in)
	if err != nil {
		return nil, fmt.Errorf("workflow %s: start: %w", r.name, err)
	}

	return run, nil
}
