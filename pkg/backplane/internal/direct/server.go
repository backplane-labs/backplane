// Package direct is the SDK's side of backplane's direct runtime: the
// ActivityService that lets backplane run the service's activities over
// gRPC on the platform port, and the hook Caller that reaches backplane's
// HookService the same way. Both carry the envelopes Temporal carries;
// neither needs it.
package direct

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/exception"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
)

const scopeName = "github.com/backplane-labs/backplane/pkg/backplane/internal/direct"

// Failure types of a direct call, as the binding knows them.
const (
	NonRetryableType = "backplane.NonRetryable"
	PanicType        = "backplane.Panic"
)

var errPanic = errors.New("handler panicked")

// Server serves ActivityService: the handlers activity.Handle registered
// in the env.
type Server struct {
	backplanev1.UnimplementedActivityServiceServer

	service string
	env     *env.Env
	log     *xlog.Logger
	tracer  trace.Tracer
}

// NewServer serves the activities of env.
func NewServer(service string, e *env.Env, log *xlog.Logger) *Server {
	return &Server{service: service, env: e, log: log, tracer: otel.GetTracerProvider().Tracer(scopeName)}
}

// Run implements ActivityService.
func (s *Server) Run(ctx context.Context, req *backplanev1.RunRequest) (*backplanev1.RunResponse, error) {
	call := req.GetCall()
	full := call.GetActivity()

	name, ok := cutService(full, s.service)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "%q is not an activity of %s", full, s.service)
	}

	h, ok := s.env.ActivityHandler(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "%s declares no activity %s", s.service, name)
	}

	start := time.Now()

	ctx, end := continueTrace(ctx, s.tracer, full, call.GetTrace())
	defer end()

	deadline, _ := ctx.Deadline()
	ctx = env.WithActivityInfo(ctx, env.ActivityInfo{
		Attempt: max(call.GetAttempt(), 1), Binding: call.GetBinding(), Step: call.GetStep(), Key: call.GetKey(),
		Deadline: deadline,
	})

	out, err := s.handle(ctx, full, h, call.GetPayload())
	metrics.ActivityHandled(ctx, full, outcome(err), time.Since(start))

	if err != nil {
		return nil, Status(err)
	}

	return &backplanev1.RunResponse{Result: &backplanev1.ActivityResult{Payload: out}}, nil
}

// handle runs h; its error, or a panic, is the call's.
func (s *Server) handle(ctx context.Context, full string, h env.Handler, in []byte) ([]byte, error) {
	var (
		out []byte
		err error
	)

	func() {
		defer func() {
			if r := recover(); r != nil {
				metrics.Panic(ctx, "activity "+full)
				s.log.Ctx().Error(ctx, "activity panicked",
					append(exception.Panic(r, debug.Stack()), xlog.String("activity", full))...)

				out, err = nil, fmt.Errorf("%s: %w: %v", full, errPanic, r)
			}
		}()

		out, err = h(ctx, in)
	}()

	return out, err
}

// Status is a handler's error as the call's status: a CallFailure detail
// says whether retrying can help. A context error is its own code.
func Status(err error) error {
	var (
		nonRetryable env.NonRetryableError
		code         = codes.Unavailable
		failure      = &backplanev1.CallFailure{Retryable: true}
	)

	switch {
	case errors.As(err, &nonRetryable):
		code, failure.Type, failure.Retryable = codes.FailedPrecondition, NonRetryableType, false
	case errors.Is(err, errPanic):
		failure.Type = PanicType
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	}

	st, detailErr := status.New(code, err.Error()).WithDetails(failure)
	if detailErr != nil {
		return status.Error(code, err.Error()) //nolint:wrapcheck // a gRPC status travels as is
	}

	return st.Err() //nolint:wrapcheck // a gRPC status travels as is
}

// FailureOf reads the CallFailure of a status error: its type and whether
// retrying can help; retryable (and no type) when it has none, as a
// transport error is.
func FailureOf(err error) (string, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return "", true
	}

	for _, d := range st.Details() {
		if f, isFailure := d.(*backplanev1.CallFailure); isFailure {
			return f.GetType(), f.GetRetryable()
		}
	}

	switch st.Code() { //nolint:exhaustive // every other code is retryable
	case codes.NotFound, codes.InvalidArgument, codes.PermissionDenied, codes.FailedPrecondition, codes.Unimplemented:
		return "", false
	}

	return "", true
}

func outcome(err error) string {
	if err != nil {
		return metrics.Error
	}

	return metrics.OK
}

// cutService is the name after "<service>." in full.
func cutService(full, service string) (string, bool) {
	prefix := service + "."
	if len(full) <= len(prefix) || full[:len(prefix)] != prefix {
		return "", false
	}

	return full[len(prefix):], true
}

// continueTrace starts a consumer span under the caller's trace when the
// envelope carries one of another trace.
func continueTrace(
	ctx context.Context, tracer trace.Tracer, name string, carrier map[string]string,
) (context.Context, func()) {
	if len(carrier) == 0 {
		return ctx, func() {}
	}

	parent := otel.GetTextMapPropagator().Extract(context.Background(), propagation.MapCarrier(carrier))
	remote, current := trace.SpanContextFromContext(parent), trace.SpanContextFromContext(ctx)

	if !remote.IsValid() || remote.TraceID() == current.TraceID() {
		return ctx, func() {}
	}

	ctx, span := tracer.Start(trace.ContextWithRemoteSpanContext(ctx, remote), name,
		trace.WithSpanKind(trace.SpanKindConsumer))

	return ctx, func() { span.End() }
}
