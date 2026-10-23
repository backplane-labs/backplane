package temporal

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/exception"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	"github.com/backplane-labs/backplane/pkg/backplane/meta"
)

// StartWorker runs the service's worker on its task queue: every declared
// activity and the workflows registered through Env (the author's own, the
// workflow-backed activities). It does not wait for Temporal: the worker
// starts on g as soon as the connection is up, retrying a failed start.
// Hook calls have their own worker (StartHookWorker).
func (c *Client) StartWorker(_ context.Context, g node.Group) error {
	g.Go(func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return nil
		case <-c.ready:
		}

		_ = backoff.Retry(ctx, c.retry, func(context.Context) error { return c.startWorker() },
			func(err error, in time.Duration) {
				c.log.Warn("temporal worker start failed, retrying", xlog.Err(err), xlog.Duration("retry_in", in))
			})

		return nil
	})

	return nil
}

// startWorker makes one attempt; a worker whose start failed is discarded.
func (c *Client) startWorker() error {
	c.mu.Lock()
	conn, done := c.client, c.closed || c.stopped
	c.mu.Unlock()

	if done || conn == nil {
		return nil
	}

	w := worker.New(conn, c.p.Service, c.workerOptions())

	for name, h := range c.p.Env.Activities() {
		w.RegisterActivityWithOptions(c.activity(name, h), activity.RegisterOptions{Name: name})
	}

	for name, h := range c.p.Env.OwnActivities() {
		w.RegisterActivityWithOptions(c.activity(name, h), activity.RegisterOptions{Name: name})
	}

	for _, register := range c.p.Env.WorkerRegistrations() {
		register(w)
	}

	if err := w.Start(); err != nil {
		w.Stop()

		return fmt.Errorf("temporal: start worker: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || c.stopped {
		go w.Stop()

		return nil
	}

	c.worker = w
	c.log.Info("temporal worker started", xlog.String("task_queue", c.p.Service))

	return nil
}

// workerOptions applies the tuning over Temporal's defaults.
func (c *Client) workerOptions() worker.Options {
	t := c.p.Worker

	return worker.Options{
		MaxConcurrentActivityExecutionSize:     t.MaxConcurrentActivities,
		MaxConcurrentWorkflowTaskExecutionSize: t.MaxConcurrentWorkflowTasks,
		MaxConcurrentActivityTaskPollers:       t.ActivityPollers,
		MaxConcurrentWorkflowTaskPollers:       t.WorkflowPollers,
		WorkerStopTimeout:                      stopGrace,
		OnFatalError: func(err error) {
			c.log.Error("temporal worker stopped on a fatal error", xlog.Err(err))
		},
	}
}

// StopWorker stops polling and lets running activities finish within ctx;
// past it the stop goes on in the background and is logged.
func (c *Client) StopWorker(ctx context.Context) error {
	c.mu.Lock()
	w := c.worker
	c.worker, c.stopped = nil, true
	c.mu.Unlock()

	return c.stop(ctx, w, "worker")
}

// stop stops w (nil: nothing) within ctx.
func (c *Client) stop(ctx context.Context, w worker.Worker, what string) error {
	if w == nil {
		return nil
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		w.Stop()
	}()

	select {
	case <-done:
	case <-ctx.Done():
		c.log.Warn("temporal " + what + " still stopping at the end of the stop budget: work in flight is abandoned")
	}

	return nil
}

// activity adapts a JSON handler to a Temporal activity over the envelope.
// The trace of the envelope parents the handler when Temporal headers did
// not carry the same trace; the handler's ctx carries env.ActivityInfo.
// Errors: an application error passes as is; env.NonRetryable becomes a
// non-retryable one; anything else — a panic too — is retried by the
// caller's policy. Messages are prefixed with <service>.<Name>.
func (c *Client) activity(
	name string, h env.Handler,
) func(ctx context.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
	full := c.p.Service + "." + name
	tracer := otel.GetTracerProvider().Tracer(scopeName)

	return func(ctx context.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		start := time.Now()

		ctx, end := continueTrace(ctx, tracer, full, call.GetTrace())
		defer end()

		ctx = meta.Replace(env.WithActivityInfo(ctx, activityInfo(ctx, call)), call.GetMetadata())
		out, err := c.handle(ctx, full, h, call.GetPayload())
		metrics.ActivityHandled(ctx, full, outcome(err), time.Since(start))

		if err != nil {
			return nil, err
		}

		return &backplanev1.ActivityResult{Payload: out}, nil
	}
}

// handle runs h; its error, or a panic, becomes what Temporal gets.
func (c *Client) handle(ctx context.Context, full string, h env.Handler, in []byte) ([]byte, error) {
	var (
		out []byte
		err error
	)

	func() {
		defer func() {
			if r := recover(); r != nil {
				metrics.Panic(ctx, "activity "+full)
				c.log.Ctx().Error(ctx, "activity panicked",
					append(exception.Panic(r, debug.Stack()), xlog.String("activity", full))...)

				out, err = nil, fmt.Errorf("%s: %w: %v", full, errPanic, r)
			}
		}()

		out, err = h(ctx, in)
		if err != nil {
			err = activityError(full, err)
		}
	}()

	return out, err
}

var errPanic = errors.New("panic")

// activityInfo of the running activity for its handler.
func activityInfo(ctx context.Context, call *backplanev1.ActivityCall) env.ActivityInfo {
	info := activity.GetInfo(ctx)

	return env.ActivityInfo{
		Attempt:   info.Attempt,
		Binding:   call.GetBinding(),
		Step:      call.GetStep(),
		Key:       info.WorkflowExecution.ID + "/" + info.ActivityID,
		Deadline:  info.Deadline,
		Heartbeat: func(details ...any) { activity.RecordHeartbeat(ctx, details...) },
	}
}

func outcome(err error) string {
	if err != nil {
		return metrics.Error
	}

	return metrics.OK
}

func activityError(full string, err error) error {
	var (
		app    *temporal.ApplicationError
		marked env.NonRetryableError
	)

	var later env.RetryAfterError

	switch {
	case errors.As(err, &app):
		return err
	case errors.As(err, &marked):
		return final(full+": "+err.Error(), NonRetryableType)
	case errors.As(err, &later):
		return temporal.NewApplicationErrorWithOptions(full+": "+err.Error(), "", //nolint:wrapcheck // Temporal's own type
			temporal.ApplicationErrorOptions{NextRetryDelay: later.Delay, Cause: err})
	default:
		return fmt.Errorf("%s: %w", full, err)
	}
}

// continueTrace starts a span under the envelope's trace when ctx is not
// already in it (the caller's Temporal headers did not carry it), linked to
// the span ctx has.
func continueTrace(
	ctx context.Context, tracer trace.Tracer, name string, carrier map[string]string,
) (context.Context, func()) {
	if len(carrier) == 0 {
		return ctx, func() {}
	}

	parent := w3c{}.Extract(context.Background(), propagation.MapCarrier(carrier))
	remote, current := trace.SpanContextFromContext(parent), trace.SpanContextFromContext(ctx)

	if !remote.IsValid() || remote.TraceID() == current.TraceID() {
		return ctx, func() {}
	}

	opts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindConsumer)}
	if current.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: current}))
	}

	if b := baggage.FromContext(parent); b.Len() > 0 {
		ctx = baggage.ContextWithBaggage(ctx, b)
	}

	ctx, span := tracer.Start(trace.ContextWithRemoteSpanContext(ctx, remote), "activity "+name, opts...)

	return ctx, func() { span.End() }
}
