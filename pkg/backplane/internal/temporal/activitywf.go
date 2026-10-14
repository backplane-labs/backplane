package temporal

import (
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// WorkflowHandler is a workflow-backed activity over JSON payloads.
type WorkflowHandler func(ctx workflow.Context, in []byte) ([]byte, error)

// WorkflowActivity adapts h to the workflow a binding step runs as a child
// workflow by name on the service's queue: input ActivityCall, output
// ActivityResult. Errors map as for activities: an application error
// passes as is, env.NonRetryable becomes a non-retryable one, messages are
// prefixed with <service>.<Name>. A panic is Temporal's: the workflow task
// fails and is retried, the run waits for a fixed worker.
func WorkflowActivity(
	service, name string, h WorkflowHandler,
) func(ctx workflow.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
	full := service + "." + name

	return func(ctx workflow.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		out, err := h(ctx, call.GetPayload())
		if err != nil {
			return nil, activityError(full, err)
		}

		return &backplanev1.ActivityResult{Payload: out}, nil
	}
}
