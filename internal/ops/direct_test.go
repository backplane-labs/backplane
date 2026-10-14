package ops_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/direct"
	"github.com/backplane-labs/backplane/internal/ops"
	"github.com/backplane-labs/backplane/internal/wire"
)

// fakeDirect answers like the direct runtime: hooks by name, activities
// by a script of outcomes per attempt.
type fakeDirect struct {
	runtime  consolev1.Runtime
	hookOut  []byte
	hookErr  error
	calls    []*backplanev1.ActivityCall
	attempts []int32
	outcomes []error
	timeouts []time.Duration
}

func (f *fakeDirect) Runtime(context.Context, string) (consolev1.Runtime, error) {
	return f.runtime, nil
}

func (f *fakeDirect) RaiseHook(_ context.Context, call *backplanev1.HookCall) (*backplanev1.HookResult, string, error) {
	if f.hookErr != nil {
		return nil, "", f.hookErr
	}

	return &backplanev1.HookResult{Payload: f.hookOut}, wire.DirectRunID(call.GetHook(), "run-1"), nil
}

func (f *fakeDirect) CallActivity(
	_ context.Context, call *backplanev1.ActivityCall, timeout time.Duration,
) (*backplanev1.ActivityResult, error) {
	f.calls, f.timeouts = append(f.calls, call), append(f.timeouts, timeout)
	f.attempts = append(f.attempts, call.GetAttempt())

	if n := len(f.calls); n <= len(f.outcomes) && f.outcomes[n-1] != nil {
		return nil, f.outcomes[n-1]
	}

	return &backplanev1.ActivityResult{Payload: []byte(`{"echo":` + string(call.GetPayload()) + `}`)}, nil
}

func session(t *testing.T) context.Context {
	t.Helper()

	return metadata.NewIncomingContext(t.Context(), metadata.Pairs(ops.SessionMetadata, "s-1"))
}

// An activity runs over the direct transport without Temporal: retried
// while the failure is retryable, up to max_attempts, with the declared
// timeout; a final failure is the result's error, not the call's.
func TestRunActivityDirect(t *testing.T) {
	t.Parallel()

	retryable := &direct.Failure{Msg: "hello.Echo: unreachable", Type: wire.StepFailedType, Retryable: true}
	fake := &fakeDirect{outcomes: []error{retryable, nil}}
	srv := newServers(ops.Detached(fixture(t), ops.WithDirect(fake)))

	res, err := srv.calls.RunActivity(session(t), &consolev1.RunActivityRequest{Activity: "hello.Echo", Input: `{"a":1}`, MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}

	out := res.GetResult()
	if out.GetError() != "" || out.GetOutput() == "" || len(fake.calls) != 2 || fake.attempts[0] != 1 || fake.attempts[1] != 2 ||
		fake.timeouts[0] != 5*time.Second || fake.calls[0].GetBinding() != "console" || out.GetWorkflowId() == "" ||
		out.GetRunId() != out.GetWorkflowId() {
		t.Fatalf("result %v calls %v timeouts %v", out, fake.calls, fake.timeouts)
	}

	final := &direct.Failure{Msg: "hello.Echo: bad input", Type: wire.StepFailedType}
	fake = &fakeDirect{outcomes: []error{final}}
	srv = newServers(ops.Detached(fixture(t), ops.WithDirect(fake)))

	res, err = srv.calls.RunActivity(session(t), &consolev1.RunActivityRequest{Activity: "hello.Echo", Input: "{}", MaxAttempts: 3})
	if err != nil || res.GetResult().GetError() != "hello.Echo: bad input" || res.GetResult().GetErrorType() != wire.StepFailedType ||
		len(fake.calls) != 1 {
		t.Fatalf("final failure: %v %v calls %d", res, err, len(fake.calls))
	}

	// A workflow-kind activity still needs Temporal.
	_, err = srv.calls.RunActivity(session(t), &consolev1.RunActivityRequest{Activity: "hello.Flow"})
	if code(t, err) != codes.FailedPrecondition {
		t.Fatalf("workflow kind without temporal: %v", err)
	}
}

// A hook whose binding runs direct (or has none) is answered by the
// direct runtime; its failure is the result's error; a Temporal-runtime
// binding without Temporal is a precondition failure.
func TestCallHookDirect(t *testing.T) {
	t.Parallel()

	fake := &fakeDirect{runtime: consolev1.Runtime_RUNTIME_DIRECT, hookOut: []byte(`{"text":"hi"}`)}
	srv := newServers(ops.Detached(fixture(t), ops.WithDirect(fake)))

	res, err := srv.calls.CallHook(session(t), &consolev1.CallHookRequest{Hook: "hello.Ask", Input: `{"q":1}`})
	if err != nil || res.GetResult().GetOutput() != `{"text":"hi"}` || res.GetResult().GetWorkflowId() == "" {
		t.Fatalf("direct hook: %v %v", res, err)
	}

	fake.hookErr = &direct.Failure{Msg: "no binding for hello.Ask", Type: wire.NoBindingType}

	res, err = srv.calls.CallHook(session(t), &consolev1.CallHookRequest{Hook: "hello.Ask"})
	if err != nil || res.GetResult().GetErrorType() != wire.NoBindingType {
		t.Fatalf("unbound: %v %v", res, err)
	}

	fake.hookErr = direct.ErrTemporalRuntime

	_, err = srv.calls.CallHook(session(t), &consolev1.CallHookRequest{Hook: "hello.Ask"})
	if code(t, err) != codes.FailedPrecondition {
		t.Fatalf("temporal runtime: %v", err)
	}

	temporal := &fakeDirect{runtime: consolev1.Runtime_RUNTIME_TEMPORAL}
	srv = newServers(ops.Detached(fixture(t), ops.WithDirect(temporal)))

	_, err = srv.calls.CallHook(session(t), &consolev1.CallHookRequest{Hook: "hello.Ask"})
	if code(t, err) != codes.Unavailable {
		t.Fatalf("temporal binding without temporal: %v", err)
	}
}
