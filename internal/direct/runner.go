package direct

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/protobuf/proto"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/wire"
)

// Kind is what a run executes.
type Kind string

// Kinds of runs.
const (
	KindBinding Kind = "binding"
	KindRule    Kind = "rule"
)

// Run is one execution of a program.
type Run struct {
	// ID of the run: the key of every call (activity.Info.Key is
	// "<id>/<label>").
	ID   string
	Kind Kind
	// Program compiled for the direct runtime.
	Program *bindings.Program
	// Payload is the hook's input (`req`) or the event's payload
	// (`event`): JSON; empty is {}.
	Payload []byte
	// Meta are the event's CloudEvents attributes; rules only.
	Meta bindings.Meta
	// Identity names what runs: ActivityCall.binding ("<hook>@<version>").
	Identity string
	// Trace is the caller's W3C trace context: ActivityCall.trace of every
	// step when the run's context has no span of its own.
	Trace map[string]string
	// Metadata of the hook call: ActivityCall.metadata of every step.
	Metadata map[string]string
}

// Recorder is told of every call of a run, for the console. Calls come
// from several goroutines.
type Recorder interface {
	Scheduled(label, step, parent string, item int, activity string, input []byte, at time.Time)
	Completed(label string, output []byte, at time.Time)
	Failed(label, msg, typ string, at time.Time)
}

// NopRecorder records nothing.
type NopRecorder struct{}

// Scheduled implements Recorder.
func (NopRecorder) Scheduled(string, string, string, int, string, []byte, time.Time) {}

// Completed implements Recorder.
func (NopRecorder) Completed(string, []byte, time.Time) {}

// Failed implements Recorder.
func (NopRecorder) Failed(string, string, string, time.Time) {}

// Runner executes programs: the steps in dependency order, each as a
// goroutine calling its activity, for-each items at most Concurrency at
// once. The first failure cancels the calls in flight and is the run's
// error; nothing is compensated.
type Runner struct {
	acts Caller
}

// NewRunner creates a runner calling activities through acts.
func NewRunner(acts Caller) *Runner { return &Runner{acts: acts} }

// Execute runs one program on its input within ctx and is the hook's
// answer (JSON; {} for a rule). The error is a *Failure, or ctx's.
func (r *Runner) Execute(ctx context.Context, run Run, rec Recorder) ([]byte, error) {
	if rec == nil {
		rec = NopRecorder{}
	}

	payload := run.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}

	var (
		scope bindings.Scope
		err   error
	)

	switch {
	case run.Program.Kind() == bindings.KindRule && run.Kind == KindRule:
		scope, err = run.Program.StartEvent(payload, run.Meta)
	case run.Program.Kind() == bindings.KindBinding && run.Kind == KindBinding:
		scope, err = run.Program.Start(payload)
	default:
		return nil, failure(fmt.Sprintf("transform failed at program: a %s program in a %s run",
			kindName(run.Program.Kind()), run.Kind), wire.TransformFailedType)
	}

	if err != nil {
		return nil, transform(err)
	}

	// The run's own span, when it has one, is what the calls continue;
	// else the caller's trace travels on.
	trace := run.Trace

	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	if len(carrier) > 0 {
		trace = carrier
	}

	ex := &execution{r: r, run: run, rec: rec, trace: trace}

	top := &flow{ex: ex, prog: run.Program, scope: scope}
	if err := top.steps(ctx); err != nil {
		return nil, err
	}

	if run.Program.Kind() == bindings.KindRule {
		return []byte("{}"), nil
	}

	out, err := run.Program.Result(top.scope)
	if err != nil {
		return nil, transform(err)
	}

	return out, nil
}

func kindName(k bindings.Kind) string {
	if k == bindings.KindRule {
		return "rule"
	}

	return "binding"
}

// execution is the state shared by a run's flows.
type execution struct {
	r     *Runner
	run   Run
	rec   Recorder
	trace map[string]string
}

// flow runs the steps of one program — the definition's, or a for-each
// body's for one item — on its own scope. Only its own goroutine touches
// the scope: calls run elsewhere and report back.
type flow struct {
	ex     *execution
	prog   *bindings.Program
	scope  bindings.Scope
	prefix string // call labels: "" at the top, "<step>[<i>]." in a body
}

// settled is a finished step: its output to bind, or its error.
type settled struct {
	plan bindings.StepPlan
	bind func() error
	err  error
}

// steps runs the flow's steps: a step starts once its dependencies are
// settled; a failure stops new starts, cancels the calls in flight and,
// once they report, is the flow's error (the first in completion order).
func (f *flow) steps(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	plans := f.prog.Steps()
	done := make(map[string]bool, len(plans))
	started := make(map[string]bool, len(plans))
	results := make(chan settled)

	var (
		first    error
		inFlight int
	)

	for {
		for progress := first == nil; progress; {
			progress = false

			for i := range plans {
				plan := plans[i]
				if started[plan.Name] || !allDone(plan.Deps, done) {
					continue
				}

				started[plan.Name] = true

				launched, err := f.launch(ctx, plan, results)
				if err != nil {
					first = err

					cancel()

					break
				}

				if !launched {
					done[plan.Name], progress = true, true

					continue
				}

				inFlight++
			}

			progress = progress && first == nil
		}

		if inFlight == 0 {
			break
		}

		res := <-results
		inFlight--
		done[res.plan.Name] = true

		err := res.err
		if err == nil {
			err = res.bind()
		}

		if err != nil && first == nil {
			first = err

			cancel()
		}
	}

	if first == nil && len(done) < len(plans) {
		return failure("transform failed at program: steps wait for each other", wire.TransformFailedType)
	}

	return first
}

func allDone(deps []string, done map[string]bool) bool {
	for _, dep := range deps {
		if !done[dep] {
			return false
		}
	}

	return true
}

// launch starts a step: its call or its items in a goroutine reporting to
// results; false when its when skips it (nothing to await).
func (f *flow) launch(ctx context.Context, plan bindings.StepPlan, results chan<- settled) (bool, error) {
	if plan.ForEach != nil {
		return f.launchItems(ctx, plan, results)
	}

	run, err := f.prog.When(plan.Name, f.scope)
	if err != nil {
		return false, transform(err)
	}

	if !run {
		if f.scope, err = f.prog.Skip(f.scope, plan.Name); err != nil {
			return false, transform(err)
		}

		return false, nil
	}

	input, err := f.prog.Input(plan.Name, f.scope)
	if err != nil {
		return false, transform(err)
	}

	label := f.prefix + plan.Name

	go func() {
		out, err := f.ex.call(ctx, plan, label, input)
		results <- settled{plan: plan, err: err, bind: func() error {
			scope, err := f.prog.Bind(f.scope, plan.Name, out)
			if err != nil {
				return transform(err)
			}

			f.scope = scope

			return nil
		}}
	}()

	return true, nil
}

// call runs one activity call, retried by the step's policy while the
// failure is retryable, and records it. Every attempt has the same key;
// the wait between attempts grows by the policy's backoff up to its
// maximum, and is at least what the handler asked for (RetryAfter).
func (e *execution) call(ctx context.Context, plan bindings.StepPlan, label string, input []byte) ([]byte, error) {
	parsed := bindings.ParseCallLabel(label)
	now := time.Now()

	e.rec.Scheduled(label, parsed.Step, parsed.Parent, parsed.Item, plan.Activity, input, now)

	call := &backplanev1.ActivityCall{
		Activity: plan.Activity, Payload: input, Trace: e.trace, Binding: e.run.Identity, Step: label,
		Attempt: 1, Key: e.run.ID + "/" + label, Metadata: e.run.Metadata,
	}

	res, err := e.attempts(ctx, plan, call)
	if err != nil {
		err = stepFailure(parsed.Step, plan.Activity, err)
		msg, typ := Readable(err)
		e.rec.Failed(label, msg, typ, time.Now())

		return nil, err
	}

	out := res.GetPayload()
	if len(out) == 0 {
		out = []byte("{}")
	}

	e.rec.Completed(label, out, time.Now())

	return out, nil
}

// attempts calls until success, a final failure, the policy's last
// attempt or ctx's end.
func (e *execution) attempts(
	ctx context.Context, plan bindings.StepPlan, call *backplanev1.ActivityCall,
) (*backplanev1.ActivityResult, error) {
	wait := plan.Retry.InitialInterval

	for {
		res, err := e.r.acts.Call(ctx, call, plan.StartToClose)
		if err == nil {
			return res, nil
		}

		failed := callFailure(err)
		if !failed.Retryable || int(call.GetAttempt()) >= max(plan.Retry.Attempts, 1) || ctx.Err() != nil {
			return nil, err //nolint:wrapcheck // read by stepFailure
		}

		// The handler's RetryAfter is honored up to the policy's maximum
		// interval: an activity cannot park a run (and its for-each slot)
		// for longer than the binding allows. A wait that would outlive the
		// run's deadline is not started: the failure stands now.
		delay := min(max(wait, failed.RetryAfter), max(plan.Retry.MaxInterval, wait))
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < delay {
			return nil, err //nolint:wrapcheck // read by stepFailure
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()

			return nil, err //nolint:wrapcheck // read by stepFailure
		case <-timer.C:
		}

		wait = min(time.Duration(float64(wait)*max(plan.Retry.Backoff, 1)), max(plan.Retry.MaxInterval, wait))

		// A fresh envelope per attempt: the transport may still hold the last.
		next, _ := proto.Clone(call).(*backplanev1.ActivityCall)
		next.Attempt++
		call = next
	}
}

// launchItems starts a for-each step: its items run in a goroutine of
// their own that reports the step settled.
func (f *flow) launchItems(ctx context.Context, plan bindings.StepPlan, results chan<- settled) (bool, error) {
	items, err := f.prog.Items(plan.Name, f.scope)
	if err != nil {
		return false, transform(err)
	}

	if len(items) > plan.ForEach.MaxItems {
		return false, failure(fmt.Sprintf("step %s: %d items, at most %d", plan.Name, len(items), plan.ForEach.MaxItems),
			wire.TransformFailedType)
	}

	base := f.scope

	go func() {
		outputs, failed, err := f.runItems(ctx, plan, base, items)
		results <- settled{plan: plan, err: err, bind: func() error {
			scope, err := f.prog.BindItems(f.scope, plan.Name, outputs, failed)
			if err != nil {
				return transform(err)
			}

			f.scope = scope

			return nil
		}}
	}()

	return true, nil
}

// itemDone is a finished item: its output or its error.
type itemDone struct {
	index int
	out   []byte
	err   error
}

// runItems runs the items, at most Concurrency at once, in item order. A
// failed item stops new ones and is the error, unless the step continues
// on errors: then it is collected and its output null.
func (f *flow) runItems(
	ctx context.Context, plan bindings.StepPlan, base bindings.Scope, items []any,
) ([][]byte, []bindings.ItemError, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	outputs := make([][]byte, len(items))
	results := make(chan itemDone)

	var (
		first    error
		failed   []bindings.ItemError
		inFlight int
		next     int
	)

	fail := func(index int, err error) {
		if plan.ForEach.Continue && ctx.Err() == nil && !isCanceled(err) {
			msg, _ := Readable(err)
			failed = append(failed, bindings.ItemError{Index: index, Message: msg})

			return
		}

		if first == nil {
			first = err

			cancel()
		}
	}

	for {
		for first == nil && next < len(items) && inFlight < plan.ForEach.Concurrency {
			index := next
			next++

			start, err := f.startItem(ctx, plan, base, index, items[index])
			if err != nil {
				fail(index, err)

				continue
			}

			if start == nil {
				continue
			}

			inFlight++

			go func() {
				out, err := start()
				results <- itemDone{index: index, out: out, err: err}
			}()
		}

		if inFlight == 0 {
			break
		}

		res := <-results
		inFlight--

		if res.err != nil {
			fail(res.index, res.err)
		} else {
			outputs[res.index] = res.out
		}
	}

	slices.SortFunc(failed, func(a, b bindings.ItemError) int { return a.Index - b.Index })

	return outputs, failed, first
}

// startItem prepares one item: a function running its activity call, or
// its body; nil when its when skips it (its output is null).
func (f *flow) startItem(
	ctx context.Context, plan bindings.StepPlan, base bindings.Scope, index int, item any,
) (func() ([]byte, error), error) {
	scope, err := f.prog.ItemScope(plan.Name, base, index, item)
	if err != nil {
		return nil, transform(err)
	}

	label := f.prefix + plan.Name + "[" + strconv.Itoa(index) + "]"

	run, err := f.prog.When(plan.Name, scope)
	if err != nil {
		return nil, transform(err)
	}

	if !run {
		return nil, nil //nolint:nilnil // skipped
	}

	if body, ok := f.prog.Body(plan.Name); ok {
		child := &flow{ex: f.ex, prog: body, scope: body.Begin(scope), prefix: label + "."}

		return func() ([]byte, error) {
			if err := child.steps(ctx); err != nil {
				return nil, err
			}

			out, err := body.ItemOutput(child.scope)
			if err != nil {
				return nil, transform(err)
			}

			return out, nil
		}, nil
	}

	input, err := f.prog.Input(plan.Name, scope)
	if err != nil {
		return nil, transform(err)
	}

	return func() ([]byte, error) { return f.ex.call(ctx, plan, label, input) }, nil
}

func isCanceled(err error) bool { return errors.Is(err, context.Canceled) }
