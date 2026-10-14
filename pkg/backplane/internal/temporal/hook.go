package temporal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	otelsdk "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/durationpb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// Contract with backplane, the Nexus handler of hooks.
const (
	// CallHookWorkflow runs one hook call raised outside workflow code, on
	// the hooks queue. The version is in the name: a change of its code
	// that is not replay-compatible is a new name (.v2), registered next
	// to the old one until runs of the old one are gone.
	CallHookWorkflow = wire.CallHookWorkflow
	// HooksSuffix: the Nexus service of <service>'s hooks is
	// <service>.Hooks on endpoint <service>.
	HooksSuffix = wire.HooksSuffix
	// NoBindingType is the application error type backplane fails a hook
	// call with when the hook has no binding.
	NoBindingType = wire.NoBindingType
	// HookFailedType: the hook call failed; the message says why.
	HookFailedType = wire.HookFailedType
	// TimeoutType: the hook call ran out of its deadline.
	TimeoutType = wire.TimeoutType
	// NonRetryableType: an activity handler marked its error final.
	NonRetryableType = wire.NonRetryableType
)

var errBadHook = errors.New("bad hook name, want <service>.<Name>")

// w3c is the envelope's trace format (HookCall.trace, ActivityCall.trace).
type w3c struct {
	propagation.TraceContext
	propagation.Baggage
}

func (w w3c) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	w.TraceContext.Inject(ctx, carrier)
	w.Baggage.Inject(ctx, carrier)
}

func (w w3c) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return w.Baggage.Extract(w.TraceContext.Extract(ctx, carrier), carrier)
}

// final is a non-retryable application error of type typ.
func final(msg, typ string) error {
	return temporal.NewNonRetryableApplicationError(msg, typ, nil) //nolint:wrapcheck // the error Temporal carries
}

// ExecuteHook raises a hook from workflow code: Nexus operation <Name> of
// service <service>.Hooks on endpoint <service>, where call.hook is
// "<service>.<Name>". call.deadline, when set, is the operation's
// schedule-to-close timeout. The error is a non-retryable application error
// with a readable message: type NoBindingType, TimeoutType or HookFailedType.
func ExecuteHook(ctx workflow.Context, call *backplanev1.HookCall) (*backplanev1.HookResult, error) {
	service, name, ok := strings.Cut(call.GetHook(), ".")
	if !ok || service == "" || name == "" {
		return nil, final(fmt.Sprintf("%v: %q", errBadHook, call.GetHook()), HookFailedType)
	}

	var opts workflow.NexusOperationOptions
	if d := call.GetDeadline().AsDuration(); d > 0 {
		opts.ScheduleToCloseTimeout = d
	}

	var res backplanev1.HookResult

	err := workflow.NewNexusClient(service, service+HooksSuffix).
		ExecuteOperation(ctx, name, call, opts).
		Get(ctx, &res)
	if err != nil {
		return nil, hookFailure(err)
	}

	return &res, nil
}

// callHook is the CallHookWorkflow.
func callHook(ctx workflow.Context, call *backplanev1.HookCall) (*backplanev1.HookResult, error) {
	return ExecuteHook(ctx, call)
}

// hookFailure turns a Nexus failure into one application error whose
// message reads without the Temporal wrapping.
func hookFailure(err error) error {
	var (
		timeout  *temporal.TimeoutError
		canceled *temporal.CanceledError
	)

	switch {
	case hasType(err, NoBindingType):
		return final(readable(err), NoBindingType)
	case errors.As(err, &timeout):
		return final("timed out", TimeoutType)
	case errors.As(err, &canceled):
		return final("canceled", HookFailedType)
	default:
		return final(readable(err), HookFailedType)
	}
}

// hasType reports whether err's chain has an application error of type typ.
func hasType(err error, typ string) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if app, ok := e.(*temporal.ApplicationError); ok && app.Type() == typ { //nolint:errorlint // walking the chain
			return true
		}
	}

	return false
}

// readable is what the handler said: the deepest application error message
// in err's chain, under the wrappers Nexus and workflows add; else the
// deepest other message.
func readable(err error) string {
	var app, other string

	for e := err; e != nil; e = errors.Unwrap(e) {
		switch typed := e.(type) { //nolint:errorlint // walking the chain
		case *temporal.ApplicationError:
			if typed.Message() != "" {
				app = typed.Message()
			}
		case *temporal.NexusOperationError, *temporal.WorkflowExecutionError:
		case *nexus.HandlerError:
			if typed.Message != "" {
				other = typed.Message
			}
		default:
			if m := e.Error(); m != "" {
				other = m
			}
		}
	}

	switch {
	case app != "":
		return app
	case other != "":
		return other
	default:
		return "operation failed"
	}
}

// Local maps a hook failure to what the caller sees: env.ErrNoBinding,
// context.DeadlineExceeded, or an error with the handler's message whose
// chain keeps the Temporal error.
func Local(err error) error {
	var timeout *temporal.TimeoutError

	switch {
	case hasType(err, NoBindingType):
		return env.ErrNoBinding
	case hasType(err, TimeoutType), errors.As(err, &timeout):
		return fmt.Errorf("timed out: %w", context.DeadlineExceeded)
	default:
		return remote{msg: readable(err), err: err}
	}
}

// remote is a failure reported by the other side.
type remote struct {
	msg string
	err error
}

func (r remote) Error() string { return r.msg }

func (r remote) Unwrap() error { return r.err }

// Inject is the W3C trace context of ctx for an envelope.
func Inject(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	w3c{}.Inject(ctx, carrier)

	if len(carrier) == 0 {
		return nil
	}

	return carrier
}

// WorkflowTrace is the trace context of the workflow's current span (set by
// the tracing interceptor), for an envelope; nil without one.
func WorkflowTrace(ctx workflow.Context) map[string]string {
	span, ok := otelsdk.SpanFromWorkflowContext(ctx)
	if !ok {
		return nil
	}

	return Inject(trace.ContextWithSpanContext(context.Background(), span.SpanContext()))
}

// HookDeadline is the deadline of a hook call from workflow code: d, cut
// to what is left of the current run's timeout when the run has one. d is
// the call's timeout, the declared one or the platform default — never
// zero, so the call is never unbounded.
func HookDeadline(ctx workflow.Context, d time.Duration) *durationpb.Duration {
	info := workflow.GetInfo(ctx)

	limit := info.WorkflowRunTimeout
	if limit <= 0 {
		limit = info.WorkflowExecutionTimeout
	}

	if limit > 0 {
		left := info.WorkflowStartTime.Add(limit).Sub(workflow.Now(ctx))
		d = min(d, max(left, time.Millisecond))
	}

	return durationpb.New(d)
}
