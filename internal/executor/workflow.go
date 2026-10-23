package executor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	otelsdk "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/wire"
)

// maxAttempts bounds a step's retry attempts so they convert to int32.
const maxAttempts = 1 << 30

// RegisterWorkflow adds wire.BindingWorkflow to r without metrics: tests
// and tools. backplane's worker takes Executor.RegisterWorkflows.
func RegisterWorkflow(r worker.Registry) { registerWorkflow(r, metrics{}) }

func registerWorkflow(r worker.Registry, m metrics) {
	r.RegisterWorkflowWithOptions(bindingWorkflow(m), workflow.RegisterOptions{Name: wire.BindingWorkflow})
}

// bindingWorkflow is wire.BindingWorkflow: it runs a compiled binding or
// rule; m records runs and steps.
//
// A step starts as soon as every step it depends on is done (ready steps
// by name); a step whose when is false is skipped. A step is its activity
// executed by name on the
// owning service's queue with the ActivityCall envelope (or, for an
// activity of kind WORKFLOW, a child workflow by name there), with the
// step's retry policy and timeouts; its output is ActivityResult.payload.
// When a step fails — after its retries — or an expression fails, the
// steps already done are compensated: each one's undo, in reverse order of
// completion; then the run fails with "step <name>: <service>.<Activity>:
// <message>" (wire.StepFailedType) or "transform failed at ..."
// (wire.TransformFailedType), non-retryable. Canceled, it compensates the
// same way and ends canceled. A binding's result is HookResult.payload; a
// rule's run returns an empty HookResult.
//
// Evaluation is the Program's: pure and deterministic, so replays agree.
func bindingWorkflow(m metrics) func(ctx workflow.Context, in Input) (*backplanev1.HookResult, error) {
	return func(ctx workflow.Context, in Input) (*backplanev1.HookResult, error) {
		start := workflow.Now(ctx)
		r := &runner{m: m, in: in, ctx: ctx}

		out, err := r.run()

		if !workflow.IsReplaying(ctx) {
			outcome := outcomeOK

			switch {
			case temporal.IsCanceledError(err):
				outcome = outcomeCanceled
			case err != nil:
				outcome = outcomeFailed
			}

			m.run(in.Kind, r.source(), outcome, workflow.Now(ctx).Sub(start))
		}

		return out, err
	}
}

// runner is one run.
type runner struct {
	m     metrics
	in    Input
	ctx   workflow.Context
	prog  *bindings.Program
	trace map[string]string
}

func (r *runner) source() string {
	if r.prog != nil {
		return r.prog.Source()
	}

	if r.in.Identity.Rule != "" {
		return "rule:" + r.in.Identity.Rule
	}

	return r.in.Identity.Hook
}

func (r *runner) run() (*backplanev1.HookResult, error) {
	scope, err := r.start()
	if err != nil {
		return nil, err
	}

	r.trace = workflowTrace(r.ctx, r.in.Trace)
	top := &flow{r: r, ctx: r.ctx, prog: r.prog, scope: scope}

	if err := top.steps(); err != nil {
		return nil, r.fail(top, err)
	}

	if r.prog.Kind() == bindings.KindRule {
		return &backplanev1.HookResult{}, nil
	}

	out, err := r.prog.Result(top.scope)
	if err != nil {
		return nil, r.fail(top, transform(err))
	}

	return &backplanev1.HookResult{Payload: out}, nil
}

// start reads the program and the input into the first scope.
func (r *runner) start() (bindings.Scope, error) {
	prog, err := programs.of(r.in.Program)
	if err != nil {
		return bindings.Scope{}, failure(fmt.Sprintf("transform failed at program: %v", err), wire.TransformFailedType)
	}

	r.prog = prog

	payload := []byte(r.in.Payload)
	if len(payload) == 0 {
		payload = []byte("{}")
	}

	var scope bindings.Scope

	switch {
	case prog.Kind() == bindings.KindRule && r.in.Kind == KindRule:
		scope, err = prog.StartEvent(payload, r.in.Meta.meta())
	case prog.Kind() == bindings.KindBinding && r.in.Kind == KindBinding:
		scope, err = prog.Start(payload)
	default:
		return bindings.Scope{}, failure(fmt.Sprintf("transform failed at program: a %s program in a %s run",
			kindName(prog.Kind()), r.in.Kind), wire.TransformFailedType)
	}

	if err != nil {
		return bindings.Scope{}, transform(err)
	}

	return scope, nil
}

func kindName(k bindings.Kind) string {
	if k == bindings.KindRule {
		return string(KindRule)
	}

	return string(KindBinding)
}

// flow runs one frame of steps: the run's, or the body of one item of a
// for-each step, whose steps see the item and everything the step sees.
type flow struct {
	r     *runner
	ctx   workflow.Context
	prog  *bindings.Program
	scope bindings.Scope
	// prefix labels the frame's calls: "" at the top, "<step>[<item>]."
	// in a body.
	prefix string
	// done is what completed, in completion order: what undo compensates.
	done []undoEntry
}

// undoEntry is a completed call with an undo: its undo input is evaluated
// on scope, or on its flow's scope when compensating (a step at the top).
type undoEntry struct {
	flow  *flow
	plan  bindings.StepPlan
	label string
	scope *bindings.Scope
}

// launched is a step in flight: bind takes its outcome into the scope.
type launched struct {
	plan bindings.StepPlan
	fut  workflow.Future
	bind func(f workflow.Future) error
}

// steps runs every step once its dependencies are settled: the ready ones
// start at once, by name (a skip settles a step at once and may make
// others ready), then the frame waits for any step in flight. After a
// failure nothing new starts and the steps in flight are awaited, so every
// step that did its work is known to undo. The error is the first in
// completion order (an expression's before the calls it prevents).
func (f *flow) steps() error {
	plans := f.prog.Steps()
	settled := make(map[string]bool, len(plans))
	started := make(map[string]bool, len(plans))
	sel := workflow.NewSelector(f.ctx)

	var (
		first    error
		inFlight int
	)

	for {
		for ready := first == nil; ready; {
			ready = false

			for i := range plans {
				plan := plans[i]
				if started[plan.Name] || !allSettled(plan.Deps, settled) {
					continue
				}

				started[plan.Name] = true

				l, err := f.launch(plan)
				if err != nil {
					first = err

					break
				}

				if l == nil {
					settled[plan.Name], ready = true, true

					continue
				}

				inFlight++

				sel.AddFuture(l.fut, func(fut workflow.Future) {
					inFlight--
					settled[l.plan.Name] = true

					if err := l.bind(fut); err != nil && first == nil {
						first = err
					}
				})
			}

			ready = ready && first == nil
		}

		if inFlight == 0 {
			break
		}

		sel.Select(f.ctx)
	}

	if first == nil && len(settled) < len(plans) {
		return failure("transform failed at program: steps wait for each other", wire.TransformFailedType)
	}

	return first
}

func allSettled(deps []string, settled map[string]bool) bool {
	for _, dep := range deps {
		if !settled[dep] {
			return false
		}
	}

	return true
}

// launch starts a step: its call, or its items; nil when its when skips
// it.
func (f *flow) launch(plan bindings.StepPlan) (*launched, error) {
	if plan.ForEach != nil {
		return f.launchItems(plan)
	}

	run, err := f.prog.When(plan.Name, f.scope)
	if err != nil {
		return nil, transform(err)
	}

	if !run {
		if f.scope, err = f.prog.Skip(f.scope, plan.Name); err != nil {
			return nil, transform(err)
		}

		return nil, nil //nolint:nilnil // skipped: nothing to await
	}

	input, err := f.prog.Input(plan.Name, f.scope)
	if err != nil {
		return nil, transform(err)
	}

	label, start := f.prefix+plan.Name, workflow.Now(f.ctx)

	fut := f.r.call(f.ctx, plan, label, plan.Activity, plan.Service, plan.Kind, input, false)

	return &launched{
		plan: plan, fut: fut,
		bind: func(fut workflow.Future) error {
			out, err := f.r.result(f.ctx, fut, plan, label, start)
			if err != nil {
				return err
			}

			if plan.Undo != "" {
				f.done = append(f.done, undoEntry{flow: f, plan: plan, label: label})
			}

			scope, err := f.prog.Bind(f.scope, plan.Name, out)
			if err != nil {
				return transform(err)
			}

			f.scope = scope

			return nil
		},
	}, nil
}

// result is a finished call's output: ActivityResult.payload, {} when
// empty; a failure is the step's.
func (r *runner) result(
	ctx workflow.Context, fut workflow.Future, plan bindings.StepPlan, label string, start time.Time,
) ([]byte, error) {
	var res backplanev1.ActivityResult

	err := fut.Get(ctx, &res)
	r.observe(plan.Activity, false, err, start)

	if err != nil {
		return nil, stepFailure(label, plan.Activity, err)
	}

	if len(res.GetPayload()) == 0 {
		return []byte("{}"), nil
	}

	return res.GetPayload(), nil
}

// itemsOutcome is what a for-each step's items produced.
type itemsOutcome struct {
	outputs [][]byte
	failed  []bindings.ItemError
}

// launchItems runs a for-each step's items in a coroutine of the run; the
// step settles when they all have.
func (f *flow) launchItems(plan bindings.StepPlan) (*launched, error) {
	items, err := f.prog.Items(plan.Name, f.scope)
	if err != nil {
		return nil, transform(err)
	}

	if len(items) > plan.ForEach.MaxItems {
		return nil, failure(fmt.Sprintf("step %s: %d items, more than its %d", plan.Name, len(items), plan.ForEach.MaxItems),
			wire.StepFailedType)
	}

	base := f.scope
	fut, set := workflow.NewFuture(f.ctx)

	var outcome itemsOutcome

	workflow.Go(f.ctx, func(ctx workflow.Context) {
		var err error

		outcome, err = f.runItems(ctx, plan, base, items)
		set.Set(nil, err)
	})

	return &launched{plan: plan, fut: fut, bind: func(done workflow.Future) error {
		if err := done.Get(f.ctx, nil); err != nil {
			return err //nolint:wrapcheck // the item's own error
		}

		scope, err := f.prog.BindItems(f.scope, plan.Name, outcome.outputs, outcome.failed)
		if err != nil {
			return transform(err)
		}

		f.scope = scope

		return nil
	}}, nil
}

// itemCall is one item in flight: finish is its output once fut is ready.
type itemCall struct {
	fut    workflow.Future
	finish func(ctx workflow.Context, fut workflow.Future) ([]byte, error)
}

// runItems runs the items, at most Concurrency at once, in item order. A
// failed item stops new ones and is the error, unless the step continues
// on errors: then it is collected and its output null.
func (f *flow) runItems(
	ctx workflow.Context, plan bindings.StepPlan, base bindings.Scope, items []any,
) (itemsOutcome, error) {
	out := itemsOutcome{outputs: make([][]byte, len(items))}
	sel := workflow.NewSelector(ctx)

	var (
		first    error
		inFlight int
		next     int
	)

	fail := func(index int, err error) {
		if plan.ForEach.Continue && !temporal.IsCanceledError(err) && ctx.Err() == nil {
			msg, _ := readableFailure(err)
			out.failed = append(out.failed, bindings.ItemError{Index: index, Message: msg})
		} else if first == nil {
			first = err
		}
	}

	for {
		for first == nil && next < len(items) && inFlight < plan.ForEach.Concurrency {
			index := next
			next++

			call, err := f.startItem(ctx, plan, base, index, items[index])
			if err != nil {
				fail(index, err)

				continue
			}

			if call == nil {
				continue
			}

			inFlight++

			sel.AddFuture(call.fut, func(fut workflow.Future) {
				inFlight--

				if output, err := call.finish(ctx, fut); err != nil {
					fail(index, err)
				} else {
					out.outputs[index] = output
				}
			})
		}

		if inFlight == 0 {
			break
		}

		sel.Select(ctx)
	}

	slices.SortFunc(out.failed, func(a, b bindings.ItemError) int { return a.Index - b.Index })

	return out, first
}

// startItem starts one item: its activity call, or its body in a
// coroutine; nil when its when skips it.
func (f *flow) startItem(
	ctx workflow.Context, plan bindings.StepPlan, base bindings.Scope, index int, item any,
) (*itemCall, error) {
	scope, err := f.prog.ItemScope(plan.Name, base, index, item)
	if err != nil {
		return nil, transform(err)
	}

	label := fmt.Sprintf("%s%s[%d]", f.prefix, plan.Name, index)

	run, err := f.prog.When(plan.Name, scope)
	if err != nil {
		return nil, transform(err)
	}

	if !run {
		return nil, nil //nolint:nilnil // skipped: its output is null
	}

	if body, ok := f.prog.Body(plan.Name); ok {
		return f.startBody(ctx, body, scope, label), nil
	}

	input, err := f.prog.Input(plan.Name, scope)
	if err != nil {
		return nil, transform(err)
	}

	start := workflow.Now(ctx)

	return &itemCall{
		fut: f.r.call(ctx, plan, label, plan.Activity, plan.Service, plan.Kind, input, false),
		finish: func(ctx workflow.Context, fut workflow.Future) ([]byte, error) {
			output, err := f.r.result(ctx, fut, plan, label, start)
			if err != nil {
				return nil, err
			}

			if plan.Undo != "" {
				// The item's undo input sees the item and, as the step's output,
				// the item's output.
				bound, err := f.prog.Bind(scope, plan.Name, output)
				if err != nil {
					return nil, transform(err)
				}

				f.done = append(f.done, undoEntry{flow: f, plan: plan, label: label, scope: &bound})
			}

			return output, nil
		},
	}, nil
}

// startBody runs one item's body as its own flow in a coroutine. A failed
// body compensates what it did at once (an item is all or nothing); a
// successful one hands its compensations to the enclosing flow.
func (f *flow) startBody(ctx workflow.Context, body *bindings.Program, scope bindings.Scope, label string) *itemCall {
	fut, set := workflow.NewFuture(ctx)
	child := &flow{r: f.r, prog: body, scope: body.Begin(scope), prefix: label + "."}

	var output []byte

	workflow.Go(ctx, func(ctx workflow.Context) {
		child.ctx = ctx

		err := child.steps()
		if err == nil {
			if output, err = body.ItemOutput(child.scope); err != nil {
				err = transform(err)
			}
		}

		if err != nil {
			// Cancellation stops the item's work, but must not cancel its
			// compensation. Finish it before settling the item's future.
			cleanup, cancel := workflow.NewDisconnectedContext(ctx)
			err = withUndos(err, f.r.compensate(cleanup, child.done))

			cancel()
		}

		set.Set(nil, err)
	})

	return &itemCall{fut: fut, finish: func(ctx workflow.Context, fut workflow.Future) ([]byte, error) {
		if err := fut.Get(ctx, nil); err != nil {
			return nil, err //nolint:wrapcheck // the body's own error
		}

		f.done = append(f.done, child.done...)

		return output, nil
	}}
}

func (r *runner) observe(activity string, undo bool, err error, start time.Time) {
	if workflow.IsReplaying(r.ctx) {
		return
	}

	outcome := outcomeOK
	if err != nil {
		outcome = outcomeError
	}

	r.m.step(activity, undo, outcome, workflow.Now(r.ctx).Sub(start))
}

// call executes activity full (a step's or its undo) on service's queue:
// an activity, or a child workflow for kind WORKFLOW. label names the call
// (bindings.CallLabel): the envelope's step and the child workflow's id.
func (r *runner) call(
	ctx workflow.Context, plan bindings.StepPlan, label, full, service string, kind backplanev1.ActivityKind,
	input []byte, undo bool,
) workflow.Future {
	_, name := bindings.SplitName(full)
	call := &backplanev1.ActivityCall{
		Activity: full, Payload: input, Trace: r.trace, Binding: r.in.Identity.String(), Step: label,
		Metadata: r.in.Metadata,
	}
	retry := &temporal.RetryPolicy{
		InitialInterval: plan.Retry.InitialInterval, BackoffCoefficient: plan.Retry.Backoff,
		MaximumInterval: plan.Retry.MaxInterval,
		MaximumAttempts: int32(min(plan.Retry.Attempts, maxAttempts)), //nolint:gosec // bounded
	}

	if kind == backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW {
		id := workflow.GetInfo(ctx).WorkflowExecution.ID + "/" + label
		if undo {
			id += "/undo"
		}

		cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			WorkflowID: id, TaskQueue: wire.Queue(service), WorkflowRunTimeout: plan.StartToClose, RetryPolicy: retry,
		})

		return workflow.ExecuteChildWorkflow(cctx, name, call)
	}

	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: wire.Queue(service), StartToCloseTimeout: plan.StartToClose, HeartbeatTimeout: plan.Heartbeat,
		RetryPolicy: retry,
	})

	return workflow.ExecuteActivity(actx, name, call)
}

// fail compensates what the run did, newest first, and is the run's
// error: cause's message with the failed undos appended. A canceled run
// ends canceled.
func (r *runner) fail(top *flow, cause error) error {
	canceled := temporal.IsCanceledError(cause) || r.ctx.Err() != nil
	ctx, _ := workflow.NewDisconnectedContext(r.ctx)
	err := withUndos(cause, r.compensate(ctx, top.done))

	if canceled {
		msg, _ := readableFailure(err)

		return temporal.NewCanceledError(msg) //nolint:wrapcheck // the error Temporal carries
	}

	return err
}

// compensate runs the undo of each entry, newest first: what failed.
func (r *runner) compensate(ctx workflow.Context, done []undoEntry) []string {
	var undone []string

	for i := len(done) - 1; i >= 0; i-- {
		e := done[i]

		scope := e.flow.scope
		if e.scope != nil {
			scope = *e.scope
		}

		input, err := e.flow.prog.UndoInput(e.plan.Name, scope)
		if err != nil {
			undone = append(undone, "undo "+e.label+": "+err.Error())

			continue
		}

		start := workflow.Now(ctx)
		err = r.call(ctx, e.plan, e.label, e.plan.Undo, e.plan.UndoService, e.plan.UndoKind, input, true).Get(ctx, nil)
		r.observe(e.plan.Undo, true, err, start)

		if err != nil {
			msg, _ := readable(err)
			undone = append(undone, "undo "+e.label+": "+prefixed(e.plan.Undo, msg))
		}
	}

	return undone
}

// withUndos is cause with the failed undos appended to its message, as a
// non-retryable failure of cause's type.
func withUndos(cause error, undone []string) error {
	if len(undone) == 0 {
		return cause
	}

	msg, typ := readableFailure(cause)

	return failure(msg+" ("+strings.Join(undone, "; ")+")", typ)
}

// failure is a non-retryable application error of type typ.
func failure(msg, typ string) error {
	return temporal.NewNonRetryableApplicationError(msg, typ, nil) //nolint:wrapcheck // the error Temporal carries
}

// transform is an expression's failure on the run's values.
func transform(err error) error {
	var eval *bindings.EvalError
	if errors.As(err, &eval) {
		return failure(eval.Error(), wire.TransformFailedType)
	}

	return failure("transform failed: "+err.Error(), wire.TransformFailedType)
}

// stepFailure is "step <name>: <service>.<Activity>: <message>". The
// SDK already prefixes a handler's message with the activity's name.
// A canceled step stays a cancellation.
func stepFailure(step, activity string, err error) error {
	msg, _ := readable(err)
	msg = "step " + step + ": " + prefixed(activity, msg)

	if temporal.IsCanceledError(err) {
		return temporal.NewCanceledError(msg) //nolint:wrapcheck // the error Temporal carries
	}

	return failure(msg, wire.StepFailedType)
}

func prefixed(activity, msg string) string {
	if strings.HasPrefix(msg, activity+": ") {
		return msg
	}

	return activity + ": " + msg
}

// readableFailure is the message and type of one of the run's own errors.
func readableFailure(err error) (string, string) {
	var app *temporal.ApplicationError
	if errors.As(err, &app) {
		return app.Message(), app.Type()
	}

	var canceled *temporal.CanceledError
	if errors.As(err, &canceled) {
		var msg string
		if canceled.HasDetails() {
			_ = canceled.Details(&msg)
		}

		if msg == "" {
			msg = "canceled"
		}

		return msg, ""
	}

	msg, typ := readable(err)

	return msg, typ
}

// workflowTrace is the trace context for the steps' envelopes: the
// workflow's own span (the tracing interceptor's) when it has one, else
// the caller's.
func workflowTrace(ctx workflow.Context, fallback map[string]string) map[string]string {
	span, ok := otelsdk.SpanFromWorkflowContext(ctx)
	if !ok || !span.SpanContext().IsValid() {
		return fallback
	}

	carrier := propagation.MapCarrier{}
	w3c{}.Inject(trace.ContextWithSpanContext(context.Background(), span.SpanContext()), carrier)

	if len(carrier) == 0 {
		return fallback
	}

	return carrier
}

// w3c is the envelopes' trace format: traceparent, tracestate, baggage.
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

// programCacheSize bounds the programs kept built: the bindings and rules
// a worker runs, not every version ever.
const programCacheSize = 512

// programs are the programs built from their marshaled form, by content:
// a workflow run and every replay of it rebuild the same program (every
// expression compiled again) from the same bytes, so one build serves
// them all. A Program is immutable and its evaluation pure: sharing it
// across workflows changes nothing of what a run computes.
//
//nolint:gochecknoglobals // one cache per worker process
var programs = &programCache{byHash: map[[sha256.Size]byte]*bindings.Program{}}

type programCache struct {
	mu     sync.Mutex
	byHash map[[sha256.Size]byte]*bindings.Program
	// order is the keys oldest first: what goes when the cache is full.
	order [][sha256.Size]byte
}

func (c *programCache) of(raw []byte) (*bindings.Program, error) {
	key := sha256.Sum256(raw)

	c.mu.Lock()
	prog, ok := c.byHash[key]
	c.mu.Unlock()

	if ok {
		return prog, nil
	}

	prog = new(bindings.Program)
	if err := json.Unmarshal(raw, prog); err != nil {
		return nil, err //nolint:wrapcheck // prefixed by bindings
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if have, built := c.byHash[key]; built {
		return have, nil
	}

	if len(c.order) >= programCacheSize {
		delete(c.byHash, c.order[0])
		c.order = c.order[1:]
	}

	c.byHash[key] = prog
	c.order = append(c.order, key)

	return prog, nil
}
