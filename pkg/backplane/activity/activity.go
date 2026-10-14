// Package activity declares operations a service implements ("умею"): the
// targets of bindings and rules. Handle declares one run as a Temporal
// activity through the installed driver; workflows.Activity declares a child
// workflow through the native extension. Both are named as
// declared on the service's task queue, input and output enveloped in
// backplane.v1.ActivityCall / ActivityResult.
//
// Inputs and outputs evolve additively: a reader ignores fields it does not
// know (protojson with DiscardUnknown), a missing field reads as its zero
// value, and renaming a field means adding a new one and keeping the old
// until every reader uses the new.
//
//	activity.Handle(root, "Send", smtp.Send, activity.StartToClose(10*time.Second),
//	    activity.Retry(activity.RetryHint{Attempts: 5}), activity.Describe("sends a mail"))
package activity

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// Option sets what the manifest tells a binding about the activity: its
// defaults for a step calling it. The binding may override them; the
// handler does not enforce them.
type Option = decl.ActivityOption

type optionFunc = decl.ActivityOptionFunc

// StartToClose is how long one attempt may take (for a workflow-backed
// activity, one run).
func StartToClose(d time.Duration) Option {
	return optionFunc(func(a *backplanev1.Activity) { a.StartToClose = positive(d) })
}

// HeartbeatTimeout: an attempt that does not Heartbeat within d is
// considered lost and retried. Only for activities that call Heartbeat.
func HeartbeatTimeout(d time.Duration) Option {
	return optionFunc(func(a *backplanev1.Activity) { a.Heartbeat = positive(d) })
}

// RetryHint is the retry a binding step should use by default.
type RetryHint struct {
	// Attempts in total, the first included; 0 is the platform's default.
	Attempts uint32
}

// Retry sets the default retry of a step calling the activity.
func Retry(h RetryHint) Option {
	return optionFunc(func(a *backplanev1.Activity) {
		a.Retry = &backplanev1.ActivityRetry{Attempts: h.Attempts}
	})
}

// Describe is the activity's description for the console.
func Describe(s string) Option {
	return optionFunc(func(a *backplanev1.Activity) { a.Description = s })
}

func positive(d time.Duration) *durationpb.Duration {
	if d <= 0 {
		return nil
	}

	return durationpb.New(d)
}

// Handle declares an activity and its implementation. name is CamelCase
// ([A-Z][A-Za-z0-9]*), anything else panics. fn's ctx carries InfoOf.
func Handle[Req, Res any](
	scope deps.Scope, name string, fn func(ctx context.Context, in Req) (Res, error), opts ...Option,
) {
	e := decl.Activity[Req, Res](scope, name, backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY, opts)
	e.Activity(name, func(ctx context.Context, in []byte) ([]byte, error) {
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
}

// NonRetryable marks err as final: the binding does not retry the activity
// (on Temporal, a non-retryable application error). Any other error is
// retried by the binding's retry policy; a *temporal.ApplicationError is
// passed as is, so its own retryability applies. nil stays nil.
func NonRetryable(err error) error {
	if err == nil {
		return nil
	}

	return env.NonRetryableError{Err: err}
}
