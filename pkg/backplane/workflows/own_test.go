package workflows_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows/workflowtest"
)

type (
	stepIn struct {
		Mode string `json:"mode"`
	}
	stepOut struct {
		Key     string        `json:"key"`
		Attempt int32         `json:"attempt"`
		Elapsed time.Duration `json:"elapsed"`
	}
)

var errStep = errors.New("step failed")

// saga runs the own activity once with three attempts and reports how long
// it took in workflow time.
func saga(ref workflows.ActivityRef[stepIn, stepOut]) func(workflow.Context, stepIn) (stepOut, error) {
	return func(ctx workflow.Context, in stepIn) (stepOut, error) {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: time.Minute,
			RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 3},
		})
		start := workflow.Now(ctx)

		out, err := ref.Execute(ctx, in)
		out.Elapsed = workflow.Now(ctx).Sub(start)

		return out, err
	}
}

// TestHandleActivity: an own activity has activity.Handle's semantics and
// stays out of the manifest.
func TestHandleActivity(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mode     string
		attempts int32
		check    func(t *testing.T, out stepOut, err error)
	}{
		{"info", 1, func(t *testing.T, out stepOut, err error) {
			t.Helper()

			if err != nil || out.Attempt != 1 || !strings.Contains(out.Key, "/") {
				t.Fatalf("info: %+v %v", out, err)
			}
		}},
		{"final", 1, func(t *testing.T, _ stepOut, err error) {
			t.Helper()

			if err == nil || !strings.Contains(err.Error(), errStep.Error()) {
				t.Fatalf("non-retryable: %v", err)
			}
		}},
		{"later", 2, func(t *testing.T, out stepOut, err error) {
			t.Helper()

			if err != nil || out.Attempt != 2 || out.Elapsed < 10*time.Minute {
				t.Fatalf("retry after: %+v %v", out, err)
			}
		}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()

			var attempts atomic.Int32

			h := backplanetest.New(t)
			ref := workflows.HandleActivity(h.Root(), "Step", func(ctx context.Context, in stepIn) (stepOut, error) {
				attempts.Add(1)

				info, ok := activity.InfoOf(ctx)
				if !ok {
					return stepOut{}, activity.NonRetryable(errors.New("no info"))
				}

				activity.Heartbeat(ctx, "beat", info.Attempt)

				switch {
				case in.Mode == "final":
					return stepOut{}, activity.NonRetryable(errStep)
				case in.Mode == "later" && info.Attempt == 1:
					return stepOut{}, activity.RetryAfter(errStep, 10*time.Minute)
				}

				return stepOut{Key: info.Key, Attempt: info.Attempt}, nil
			})
			workflows.Declare(h.Root(), "Saga", saga(ref))

			if got := h.Manifest().GetActivities(); len(got) != 0 {
				t.Fatalf("own activity in the manifest: %v", got)
			}

			w := workflowtest.New(t, h)

			var beats atomic.Int32

			w.SetOnActivityHeartbeatListener(func(_ *tactivity.Info, details converter.EncodedValues) {
				var (
					what    string
					attempt int32
				)

				if err := details.Get(&what, &attempt); err == nil && what == "beat" && attempt >= 1 {
					beats.Add(1)
				}
			})

			w.ExecuteWorkflow("Saga", stepIn{Mode: tc.mode})

			var out stepOut

			err := w.GetWorkflowError()
			if err == nil {
				err = w.GetWorkflowResult(&out)
			}

			tc.check(t, out, err)

			if attempts.Load() != tc.attempts || beats.Load() != tc.attempts {
				t.Fatalf("%d attempts, %d heartbeats, want %d", attempts.Load(), beats.Load(), tc.attempts)
			}
		})
	}
}

func TestHandleActivityNameTaken(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	fn := func(context.Context, stepIn) (stepOut, error) { return stepOut{}, nil }

	activity.Handle(h.Root(), "Step", fn)

	defer func() {
		if recover() == nil {
			t.Fatal("no panic for a taken name")
		}
	}()

	workflows.HandleActivity(h.Root(), "Step", fn)
}
