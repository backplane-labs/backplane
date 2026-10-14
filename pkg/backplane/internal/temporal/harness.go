package temporal

import (
	"context"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// ActivityFunc adapts a declared activity's handler to the Temporal
// activity the worker registers: the envelope, the handler's
// env.ActivityInfo and the error mapping of the worker, without the
// worker's tracing, metrics and panic recovery. backplanetest registers it
// on a testsuite environment.
func ActivityFunc(
	service, name string, h env.Handler,
) func(ctx context.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
	full := service + "." + name

	return func(ctx context.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		out, err := h(env.WithActivityInfo(ctx, activityInfo(ctx, call)), call.GetPayload())
		if err != nil {
			return nil, activityError(full, err)
		}

		return &backplanev1.ActivityResult{Payload: out}, nil
	}
}
