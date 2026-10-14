package workflows

import (
	"fmt"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
)

// Activity declares a workflow-backed activity: a binding step runs fn as
// a child workflow of type name on the service's queue, so the activity
// may itself wait, sleep, call hooks (workflows.CallHook) and run activities.
// name is CamelCase; errors map as for Handle (NonRetryable included).
func Activity[Req, Res any](
	scope deps.Scope, name string, fn func(ctx workflow.Context, in Req) (Res, error), opts ...activity.Option,
) {
	e := decl.Activity[Req, Res](scope, name, backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW, opts)

	run := temporal.WorkflowActivity(e.Service, name, func(ctx workflow.Context, in []byte) ([]byte, error) {
		var req Req
		if err := decl.Decode(in, &req); err != nil {
			return nil, env.NonRetryableError{Err: fmt.Errorf("activity %s: decode: %w", name, err)}
		}

		res, err := fn(ctx, req)
		if err != nil {
			return nil, err
		}

		return decl.Encode(res)
	})

	e.RegisterWorker(func(registry any) {
		if r, ok := registry.(worker.Registry); ok {
			r.RegisterWorkflowWithOptions(run, workflow.RegisterOptions{Name: name})
		}
	})
}
