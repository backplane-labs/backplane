// Package direct is backplane's direct runtime: bindings and rules run in
// backplane's own process, without Temporal. Steps and expressions are the
// compiled program's; activities are called over gRPC on the services'
// platform ports (the SDK's ActivityService); a run is recorded in
// PostgreSQL for the console. The run as a whole is retried by its caller
// (a hook's) or by event redelivery (a rule's): there are no retries per
// step, no undo, no heartbeat, no child workflow — the compiler rejects a
// definition using them for this runtime.
package direct

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/wire"
)

// Failure is a run's or a call's error as the console and the caller see
// it: a message, a type the binding knows and whether retrying can help.
//
//nolint:errname // a Failure is what the console and the API call it
type Failure struct {
	Msg  string
	Type string
	// Retryable: a retry of the whole run may succeed (an activity that
	// could not be reached, a retryable handler error); false: the input
	// or the definition is wrong, or the handler said so.
	Retryable bool
	cause     error
}

func (f *Failure) Error() string { return f.Msg }

func (f *Failure) Unwrap() error { return f.cause }

// failure is a final (non-retryable) failure of type typ.
func failure(msg, typ string) error { return &Failure{Msg: msg, Type: typ} }

// transform is an expression's failure on the run's values.
func transform(err error) error {
	var eval *bindings.EvalError
	if errors.As(err, &eval) {
		return failure(eval.Error(), wire.TransformFailedType)
	}

	return failure("transform failed: "+err.Error(), wire.TransformFailedType)
}

// stepFailure is "step <name>: <service>.<Activity>: <message>", retryable
// when the call was. A canceled call stays a cancellation.
func stepFailure(step, activity string, err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}

	call := callFailure(err)

	return &Failure{
		Msg: "step " + step + ": " + prefixed(activity, call.Msg), Type: wire.StepFailedType,
		Retryable: call.Retryable, cause: err,
	}
}

func prefixed(activity, msg string) string {
	if strings.HasPrefix(msg, activity+": ") {
		return msg
	}

	return activity + ": " + msg
}

// callFailure is a call's error as a Failure: the status message and the
// CallFailure detail the SDK attached; a status without one is retryable
// unless its code says the call itself was wrong; a deadline is a timeout.
func callFailure(err error) *Failure {
	var f *Failure
	if errors.As(err, &f) {
		return f
	}

	if errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded {
		return &Failure{Msg: "timed out", Type: wire.TimeoutType, Retryable: true, cause: err}
	}

	st, ok := status.FromError(err)
	if !ok {
		return &Failure{Msg: err.Error(), Retryable: true, cause: err}
	}

	out := &Failure{Msg: st.Message(), Retryable: true, cause: err}

	for _, d := range st.Details() {
		if cf, isFailure := d.(*backplanev1.CallFailure); isFailure {
			out.Type, out.Retryable = cf.GetType(), cf.GetRetryable()

			return out
		}
	}

	switch st.Code() { //nolint:exhaustive // every other code is retryable
	case codes.NotFound, codes.InvalidArgument, codes.PermissionDenied, codes.FailedPrecondition, codes.Unimplemented:
		out.Retryable = false
	}

	return out
}

// Readable is the message and type of a run's error: a Failure's, a
// cancellation's, a deadline's; else the error's text.
func Readable(err error) (string, string) {
	var f *Failure

	switch {
	case err == nil:
		return "", ""
	case errors.As(err, &f):
		return f.Msg, f.Type
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out", wire.TimeoutType
	case errors.Is(err, context.Canceled):
		return canceledMsg, ""
	default:
		return err.Error(), ""
	}
}

// canceledMsg is a canceled run's or step's message.
const canceledMsg = "canceled"

// Retryable reports whether a retry of the run may succeed.
func Retryable(err error) bool {
	var f *Failure
	if errors.As(err, &f) {
		return f.Retryable
	}

	return !errors.Is(err, context.Canceled)
}

// Status is a run's error as a HookService status: the CallFailure detail
// carries the type and whether retrying can help. A timeout is
// DEADLINE_EXCEEDED, a cancellation CANCELED; the rest ABORTED — the call
// reached backplane and the run failed, which UNAVAILABLE would not say.
func Status(err error) error {
	msg, typ := Readable(err)
	code := codes.Aborted

	switch {
	case errors.Is(err, context.DeadlineExceeded) || typ == wire.TimeoutType:
		code = codes.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	case typ == wire.NoBindingType:
		code = codes.FailedPrecondition
	}

	st, detailErr := status.New(code, msg).WithDetails(&backplanev1.CallFailure{Type: typ, Retryable: Retryable(err)})
	if detailErr != nil {
		return status.Error(code, msg) //nolint:wrapcheck // a gRPC status travels as is
	}

	return st.Err() //nolint:wrapcheck // a gRPC status travels as is
}

// ErrAlreadyStarted: a run with the id exists and is running or completed.
var ErrAlreadyStarted = errors.New("direct: run already started")

// errNoInstance: no serving instance runs the activity.
func errNoInstance(service, activity string) error {
	return &Failure{
		Msg: fmt.Sprintf("no serving instance of %s runs %s", service, activity), Type: wire.StepFailedType,
		Retryable: true,
	}
}
