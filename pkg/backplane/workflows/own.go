package workflows

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
)

// ActivityRef is an activity of HandleActivity, for the service's own
// workflows to run.
type ActivityRef[Req, Res any] struct{ name string }

// HandleActivity registers fn as an activity only the service's own
// workflows run (a saga's steps): on the service's worker, absent from the
// manifest, so no binding or rule can reach it. Otherwise it is
// activity.Handle's: payloads encoded as everywhere, fn's ctx carries
// activity.InfoOf and takes activity.Heartbeat, activity.NonRetryable
// stops the retries and activity.RetryAfter sets the next delay, a panic
// is a retried failure. name is CamelCase and shares the queue's activity
// names with activity.Handle: one taken panics.
//
//	charge := workflows.HandleActivity(root, "ChargeCard", billing.Charge)
//	// in a workflow, with workflow.WithActivityOptions on ctx:
//	receipt, err := charge.Execute(ctx, card)
func HandleActivity[Req, Res any](
	scope deps.Scope, name string, fn func(ctx context.Context, in Req) (Res, error),
) ActivityRef[Req, Res] {
	e, _ := decl.Env(scope, "activity "+name)
	if e.Manifest.Sealed() {
		panic("backplane: activity " + name + " registered after Run")
	}

	decl.CheckName("activity", name)

	e.OwnActivity(name, func(ctx context.Context, in []byte) ([]byte, error) {
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

	return ActivityRef[Req, Res]{name: name}
}

// Name is the activity's Temporal name.
func (r ActivityRef[Req, Res]) Name() string { return r.name }

// Execute runs the activity from workflow code under ctx's activity
// options (workflow.WithActivityOptions: its timeouts and retry policy).
// The error keeps the Temporal error in its chain; a timeout matches
// context.DeadlineExceeded.
func (r ActivityRef[Req, Res]) Execute(ctx workflow.Context, in Req) (Res, error) {
	var out Res

	payload, err := decl.Encode(in)
	if err != nil {
		return out, fmt.Errorf("activity %s: encode: %w", r.name, err)
	}

	var res backplanev1.ActivityResult

	call := &backplanev1.ActivityCall{Activity: r.name, Payload: payload, Trace: temporal.WorkflowTrace(ctx)}
	if err := workflow.ExecuteActivity(ctx, r.name, call).Get(ctx, &res); err != nil {
		return out, fmt.Errorf("activity %s: %w", r.name, temporal.Local(err))
	}

	if err := decl.Decode(res.GetPayload(), &out); err != nil {
		return out, fmt.Errorf("activity %s: decode: %w", r.name, err)
	}

	return out, nil
}
