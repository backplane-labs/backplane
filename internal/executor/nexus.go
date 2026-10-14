package executor

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/nexus-rpc/sdk-go/nexus"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporalnexus"
	"go.temporal.io/sdk/worker"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/direct"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/wire"
)

// hookSet is what the Nexus worker serves: service -> its hooks, sorted.
type hookSet map[string][]string

// hooksOf is every hook any manifest of the snapshot declares — every
// version of every service, not only the latest: an instance of an older
// version still calls its hooks during a rollout.
func hooksOf(cat registry.Catalog) hookSet {
	out := hookSet{}

	for name, svc := range cat.Services {
		seen := map[string]bool{}

		for _, m := range svc.Manifests {
			for _, h := range m.GetHooks() {
				if h.GetName() != "" {
					seen[h.GetName()] = true
				}
			}
		}

		if len(seen) > 0 {
			out[name] = slices.Sorted(maps.Keys(seen))
		}
	}

	return out
}

func (h hookSet) equal(o hookSet) bool { return maps.EqualFunc(h, o, slices.Equal[[]string]) }

// services in name order.
func (h hookSet) services() []string { return slices.Sorted(maps.Keys(h)) }

// added are the services of h that prev does not serve.
func (h hookSet) added(prev hookSet) []string {
	var out []string

	for _, s := range h.services() {
		if _, ok := prev[s]; !ok {
			out = append(out, s)
		}
	}

	return out
}

// serve makes the Nexus worker serve set: a new worker with the new set
// starts first, then the old one stops. It reports the services the
// previous worker did not serve.
func (x *Executor) serve(ctx context.Context, c client.Client, set hookSet) ([]string, error) {
	x.mu.Lock()
	same := !x.stopped && set.equal(x.served) && (x.worker != nil || len(set) == 0)
	stopped := x.stopped
	x.mu.Unlock()

	if same || stopped {
		return nil, nil
	}

	var w worker.Worker

	if len(set) > 0 {
		// Nexus tasks only: the main worker on the same queue polls the
		// workflow and activity tasks with its static registrations.
		w = worker.New(c, x.queue, worker.Options{DisableWorkflowWorker: true, LocalActivityWorkerOnly: true})

		for _, s := range x.nexusServices(set) {
			w.RegisterNexusService(s)
		}

		if err := w.Start(); err != nil {
			w.Stop()

			return nil, err //nolint:wrapcheck // Temporal's message says it
		}
	}

	x.mu.Lock()
	if x.stopped {
		x.mu.Unlock()

		if w != nil {
			go w.Stop()
		}

		return nil, nil
	}

	old, prev := x.worker, x.served
	x.worker, x.served = w, set
	x.mu.Unlock()

	if old != nil {
		go old.Stop()
	}

	x.metrics.hookServices(ctx, len(set))
	x.Log().Info("nexus worker serves hooks", xlog.String("task_queue", x.queue),
		xlog.String("services", strings.Join(set.services(), ",")))

	return set.added(prev), nil
}

// nexusServices are <service>.Hooks with an operation per hook.
func (x *Executor) nexusServices(set hookSet) []*nexus.Service {
	out := make([]*nexus.Service, 0, len(set))

	for _, name := range set.services() {
		s := nexus.NewService(wire.NexusService(name))
		for _, h := range set[name] {
			s.MustRegister(x.operation(name + "." + h))
		}

		out = append(out, s)
	}

	return out
}

// hookOp is the operation of one hook: HookCall in, HookResult out.
type hookOp = temporalnexus.TemporalOperationResult[*backplanev1.HookResult]

// operation handles hook full ("<service>.<Hook>").
func (x *Executor) operation(full string) nexus.RegisterableOperation {
	_, name := bindings.SplitName(full)

	return temporalnexus.MustNewTemporalOperation(temporalnexus.TemporalOperationOptions[
		*backplanev1.HookCall, *backplanev1.HookResult,
	]{
		Name: name,
		Start: func(
			ctx context.Context, nc temporalnexus.NexusClient, call *backplanev1.HookCall,
			opts temporalnexus.StartTemporalOperationOptions,
		) (hookOp, error) {
			return x.startHook(ctx, nc, full, call, opts.RequestID)
		},
	})
}

// startHook answers one call of hook: the run of its binding, {} for an
// optional hook without one, or a failure.
func (x *Executor) startHook(
	ctx context.Context, nexusClient temporalnexus.NexusClient, hook string, call *backplanev1.HookCall,
	requestID string,
) (hookOp, error) {
	ctx = continueTrace(ctx, call.GetTrace())

	in, prog, err := x.prepare(ctx, hook, call)

	switch {
	case errors.Is(err, errDefault):
		x.metrics.call(ctx, hook, outcomeDefault)

		return temporalnexus.NewSyncResult(&backplanev1.HookResult{Payload: []byte("{}")}), nil
	case err != nil:
		outcome := outcomeError
		if isNoBinding(err) {
			outcome = outcomeNoBinding
		}

		x.metrics.call(ctx, hook, outcome)

		return hookOp{}, err
	}

	// A direct-runtime binding runs here and now; the operation is
	// synchronous: its answer within the Nexus request.
	if prog.Runtime() == consolev1.Runtime_RUNTIME_DIRECT && x.direct != nil {
		res, err := x.runDirect(ctx, hook, prog, in, call)
		if err != nil {
			x.metrics.call(ctx, hook, outcomeError)

			return hookOp{}, err
		}

		x.metrics.call(ctx, hook, outcomeRun)

		return temporalnexus.NewSyncResult(res), nil
	}

	opts := client.StartWorkflowOptions{
		ID: wire.BindingRunID(hook, requestID), TaskQueue: x.queue,
		WorkflowExecutionTimeout: call.GetDeadline().AsDuration(),
		Memo:                     map[string]any{wire.MemoSource: call.GetInstance(), wire.MemoBinding: in.Identity.String()},
	}

	res, err := temporalnexus.StartUntypedWorkflow[*backplanev1.HookResult](
		ctx, nexusClient, opts, wire.BindingWorkflow, in)
	if err != nil {
		x.metrics.call(ctx, hook, outcomeError)
		x.Log().Ctx().Warn(ctx, "binding run did not start", xlog.String("hook", hook), xlog.Err(err))

		return hookOp{}, err
	}

	x.metrics.call(ctx, hook, outcomeRun)

	return res, nil
}

// runDirect runs a direct-runtime binding for a Nexus call: a failed run
// is a non-retryable application error of the run's type (a retryable
// one a handler error, so Temporal retries the operation).
func (x *Executor) runDirect(
	ctx context.Context, hook string, prog *bindings.Program, in Input, call *backplanev1.HookCall,
) (*backplanev1.HookResult, error) {
	if d := call.GetDeadline().AsDuration(); d > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	id := wire.DirectRunID(hook, uuid.NewString())
	payload := []byte(in.Payload)

	out, err := x.direct.Execute(ctx, direct.Start{
		ID: id, Kind: direct.KindBinding, Source: hook, Version: in.Identity.Version, Caller: call.GetInstance(),
		Input: payload,
	}, direct.Run{
		ID: id, Kind: direct.KindBinding, Program: prog, Payload: payload, Identity: in.Identity.String(),
		Trace: call.GetTrace(),
	}, false)
	if err != nil {
		msg, typ := direct.Readable(err)
		if typ == "" {
			typ = wire.HookFailedType
		}

		if direct.Retryable(err) && ctx.Err() == nil {
			return nil, unavailable("binding run of "+hook, err)
		}

		return nil, failure(msg, typ)
	}

	return &backplanev1.HookResult{Payload: out}, nil
}

// errDefault: an optional hook without a binding answers {}.
var errDefault = errors.New("executor: default answer")

func isNoBinding(err error) bool {
	msg, typ := readable(err)

	return typ == wire.NoBindingType && msg != ""
}

// prepare is the run of a call of hook: errDefault, a non-retryable
// failure (no binding for a required hook, a binding that does not compile
// any more, an input that is not JSON), or a retryable Nexus handler error
// (the store or the registry not ready).
func (x *Executor) prepare(
	ctx context.Context, hook string, call *backplanev1.HookCall,
) (Input, *bindings.Program, error) {
	payload := call.GetPayload()
	if len(payload) > 0 && !json.Valid(payload) {
		return Input{}, nil, failure("input of "+hook+" is not JSON", wire.HookFailedType)
	}

	b, version, err := x.bindings.Active(ctx, hook)

	switch {
	case errors.Is(err, bindings.ErrNoBinding):
		if x.required(hook, call.GetInstance()) {
			return Input{}, nil, failure("no binding for "+hook, wire.NoBindingType)
		}

		return Input{}, nil, errDefault
	case err != nil:
		return Input{}, nil, unavailable("binding of "+hook, err)
	}

	prog, err := x.program(hook, b, version)
	if err != nil {
		var invalid *bindings.InvalidError
		if errors.As(err, &invalid) {
			return Input{}, nil, failure("binding "+hook+"@"+strconv.FormatInt(version, 10)+
				" does not compile against the current manifests: "+err.Error(), wire.HookFailedType)
		}

		return Input{}, nil, unavailable("binding of "+hook, err)
	}

	in, err := BindingInput(prog, Identity{Hook: hook, Version: version}, payload, call.GetTrace())
	if err != nil {
		return Input{}, nil, failure(err.Error(), wire.HookFailedType)
	}

	return in, prog, nil
}

// unavailable is a retryable Nexus handler error: Temporal retries the
// start until the operation's deadline.
func unavailable(what string, err error) error {
	return &nexus.HandlerError{Type: nexus.HandlerErrorTypeUnavailable, Message: what + ": " + err.Error(), Cause: err}
}

// required reports whether hook fails without a binding: as declared by
// the manifest the calling instance runs, else by the service's latest,
// else by any. A hook no manifest declares is required: there is no
// default answer to give.
func (x *Executor) required(hook, instance string) bool {
	svcName, name := bindings.SplitName(hook)

	svc, known := x.src.Current().Services[svcName]
	if !known {
		return true
	}

	find := func(m *backplanev1.Manifest) (bool, bool) {
		for _, h := range m.GetHooks() {
			if h.GetName() == name {
				return h.GetRequired(), true
			}
		}

		return false, false
	}

	for _, in := range svc.Instances {
		if in.ID == instance && in.State != nil {
			if req, ok := find(svc.Manifest(in.State.GetVersion())); ok {
				return req
			}
		}
	}

	if req, ok := find(svc.Latest()); ok {
		return req
	}

	for _, v := range slices.Sorted(maps.Keys(svc.Manifests)) {
		if req, ok := find(svc.Manifests[v]); ok {
			return req
		}
	}

	return true
}

// compiled is a program of one version against one registry snapshot.
type compiled struct {
	version int64
	index   uint64
	prog    *bindings.Program
}

// program compiles version of hook's binding against the current
// snapshot, once per version and snapshot.
func (x *Executor) program(hook string, b bindings.Binding, version int64) (*bindings.Program, error) {
	cat := x.src.Current()
	if cat.Index == 0 {
		return nil, ErrNotSynced
	}

	x.mu.Lock()
	c, ok := x.programs[hook]
	x.mu.Unlock()

	if ok && c.version == version && c.index == cat.Index {
		return c.prog, nil
	}

	full := bindings.FromRegistry(cat)
	if d, hasDefaults := x.bindings.(interface{ Defaults() bindings.Defaults }); hasDefaults {
		full = bindings.WithDefaults(full, d.Defaults())
	}

	prog, err := bindings.CompileBinding(b, full)
	if err != nil {
		return nil, err //nolint:wrapcheck // bindings' error says it
	}

	x.mu.Lock()
	x.programs[hook] = compiled{version: version, index: cat.Index, prog: prog}
	x.mu.Unlock()

	return prog, nil
}

// continueTrace puts the envelope's trace under ctx when Temporal's
// headers did not bring one: the run starts in the caller's trace.
func continueTrace(ctx context.Context, carrier map[string]string) context.Context {
	if len(carrier) == 0 || trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}

	parent := w3c{}.Extract(context.Background(), propagation.MapCarrier(carrier))

	remote := trace.SpanContextFromContext(parent)
	if !remote.IsValid() {
		return ctx
	}

	if b := baggage.FromContext(parent); b.Len() > 0 {
		ctx = baggage.ContextWithBaggage(ctx, b)
	}

	return trace.ContextWithRemoteSpanContext(ctx, remote)
}
