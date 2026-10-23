package workflows

import (
	"fmt"

	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/hook"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
)

// CallHook raises the hook from workflow code: the Nexus operation
// directly, no extra workflow. The deadline is Timeout, else the declared
// DefaultTimeout, else the platform default — cut to what is left of the
// run's timeout; a call is never unbounded. The error is readable ("hook
// <service>.<Name>: no binding") and matches ErrNoBinding when there is no
// binding.
func CallHook[Req, Res any](ctx workflow.Context, r hook.Ref[Req, Res], in Req, opts ...hook.CallOption) (Res, error) {
	var out Res

	if r.Name() == "" {
		return out, fmt.Errorf("hook: call on an undeclared Ref: %w", hook.ErrUnavailable)
	}

	timeout := r.CallTimeout(opts...)

	payload, err := decl.Encode(in)
	if err != nil {
		return out, fmt.Errorf("hook %s: encode: %w", r.Name(), err)
	}

	res, err := temporal.ExecuteHook(ctx, &backplanev1.HookCall{
		Hook:     r.Name(),
		Payload:  payload,
		Trace:    temporal.WorkflowTrace(ctx),
		Deadline: temporal.HookDeadline(ctx, timeout),
		Metadata: r.CallMetadata(opts...),
	})
	if err != nil {
		return out, fmt.Errorf("hook %s: %w", r.Name(), temporal.Local(err))
	}

	if err := decl.Decode(res.GetPayload(), &out); err != nil {
		return out, fmt.Errorf("hook %s: decode: %w", r.Name(), err)
	}

	return out, nil
}
