package direct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Defaults of the runtime.
const (
	// DefaultRunTimeout bounds a rule's run (a hook call is bounded by its
	// deadline).
	DefaultRunTimeout = 5 * time.Minute
	// DefaultRetention of recorded runs.
	DefaultRetention = 7 * 24 * time.Hour
	// retentionTick is how often expired runs are deleted.
	retentionTick = time.Hour
)

// Errors.
var (
	// ErrNotSynced: the registry has no snapshot yet.
	ErrNotSynced = errors.New("direct: registry not synced yet")
	// ErrTemporalRuntime: the definition runs on Temporal, which this
	// backplane does not have (or does not route hook calls to).
	ErrTemporalRuntime = errors.New("direct: the definition runs on the temporal runtime, which is not available")
)

// Bindings is what the runtime reads of bindings: *bindings.Manager.
type Bindings interface {
	Active(ctx context.Context, hook string) (bindings.Binding, int64, error)
	Catalog() (bindings.Catalog, error)
}

// TemporalRunner runs a hook call's binding on Temporal: the executor,
// when the installation has it. Without one, a Temporal-runtime binding
// called through HookService fails with ErrTemporalRuntime.
type TemporalRunner interface {
	RunBinding(ctx context.Context, hook string, prog *bindings.Program, version int64, call *backplanev1.HookCall,
	) (*backplanev1.HookResult, error)
}

// Option configures New.
type Option func(*Service)

// RunTimeout bounds a rule's run; default DefaultRunTimeout.
func RunTimeout(d time.Duration) Option { return func(s *Service) { s.runTimeout = d } }

// Retention of recorded runs; 0 keeps them.
func Retention(d time.Duration) Option { return func(s *Service) { s.retention = d } }

// WithCaller replaces the activity transport (tests).
func WithCaller(c Caller) Option { return func(s *Service) { s.caller = c } }

// WithTemporal installs how Temporal-runtime bindings run when called
// through HookService.
func WithTemporal(t TemporalRunner) Option { return func(s *Service) { s.temporal = t } }

// Author names who made a console call from its context.
func Author(fn func(ctx context.Context) string) Option { return func(s *Service) { s.author = fn } }

// Service is the runtime: the component owning the activity transport,
// the run recorder and the runs in flight.
type Service struct {
	deps.Component

	bindings   Bindings
	src        registry.Source
	acts       *Activities
	caller     Caller
	runner     *Runner
	runs       *Runs
	temporal   TemporalRunner
	author     func(ctx context.Context) string
	runTimeout time.Duration
	retention  time.Duration
	metrics    metrics

	mu       sync.Mutex
	programs map[string]compiled // by hook
	running  map[string]context.CancelFunc
}

// compiled is a program of one version against one registry snapshot.
type compiled struct {
	version int64
	index   uint64
	prog    *bindings.Program
}

// New creates the runtime under parent: bindings from b, instances from
// src, runs recorded in st, activities called with the internal secret.
func New(
	parent deps.Scope, b Bindings, src registry.Source, st deps.Dependency[*store.Store], secret string, opts ...Option,
) *Service {
	s := &Service{
		Component: deps.NewComponent(parent, "direct"),
		bindings:  b, src: src, runTimeout: DefaultRunTimeout, retention: DefaultRetention,
		author:   func(context.Context) string { return "admin" },
		programs: map[string]compiled{}, running: map[string]context.CancelFunc{},
	}

	for _, o := range opts {
		o(s)
	}

	s.metrics = newMetrics(s.Meter())

	if s.caller == nil {
		s.acts = NewActivities(src, secret)
		s.caller = s.acts
	}

	s.runner = NewRunner(s.caller)
	s.runs = NewRuns(st, s.Log(), s.metrics.dropped)

	s.Go(s.runs.Write)
	s.Go(s.expire)
	s.Go(s.prune)
	s.OnStop(func(context.Context) error {
		if s.acts != nil {
			s.acts.Close()
		}

		return nil
	})

	return s
}

// Runs is the run store, for the console.
func (s *Service) Runs() *Runs { return s.runs }

// RunTimeout bounds a rule's run.
func (s *Service) RunTimeout() time.Duration { return s.runTimeout }

// expire deletes runs past the retention.
func (s *Service) expire(ctx context.Context) error {
	if s.retention <= 0 {
		return nil
	}

	tick := time.NewTicker(retentionTick)
	defer tick.Stop()

	for {
		if err := s.runs.Expire(ctx, time.Now().Add(-s.retention)); err != nil && ctx.Err() == nil {
			s.Log().Warn("direct runs cleanup retry", xlog.Err(err))
		}

		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// prune closes connections to instances that left, on registry changes.
func (s *Service) prune(ctx context.Context) error {
	if s.acts == nil {
		return nil
	}

	changes := s.src.Changes(ctx)

	for {
		if _, ok := <-changes; !ok {
			return nil
		}

		s.acts.Prune()
	}
}

// Program compiles version of hook's binding against the current
// snapshot, once per version and snapshot, with the installation's
// defaults.
func (s *Service) Program(hook string, b bindings.Binding, version int64) (*bindings.Program, error) {
	cat := s.src.Current()
	if cat.Index == 0 {
		return nil, ErrNotSynced
	}

	s.mu.Lock()
	c, ok := s.programs[hook]
	s.mu.Unlock()

	if ok && c.version == version && c.index == cat.Index {
		return c.prog, nil
	}

	full, err := s.bindings.Catalog()
	if err != nil {
		return nil, fmt.Errorf("direct: %w", err)
	}

	prog, err := bindings.CompileBinding(b, full)
	if err != nil {
		return nil, err //nolint:wrapcheck // bindings' error says it
	}

	s.mu.Lock()
	s.programs[hook] = compiled{version: version, index: cat.Index, prog: prog}
	s.mu.Unlock()

	return prog, nil
}

// Required reports whether hook fails without a binding: as declared by
// the manifest of the calling instance's version, else the latest, else
// any; unknown hooks are required.
func (s *Service) Required(hook, instance string) bool {
	svcName, name := bindings.SplitName(hook)

	svc, known := s.src.Current().Services[svcName]
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

// Execute runs one program and records it: the run begins (Begin, or
// Claim when claim is set — then a run already handled is
// ErrAlreadyStarted), runs within ctx, and ends. The run can be canceled
// by id while it runs.
func (s *Service) Execute(ctx context.Context, start Start, run Run, claim bool) ([]byte, error) {
	start.Program = run.Program.Spec()
	masks := run.Program.Masks()

	if claim {
		claimed, err := s.runs.Claim(ctx, start, masks)
		if err != nil {
			return nil, err
		}

		if !claimed {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyStarted, start.ID)
		}
	} else {
		s.runs.Begin(start, masks)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.mu.Lock()
	s.running[start.ID] = cancel
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.running, start.ID)
		s.mu.Unlock()
	}()

	began := time.Now()
	out, err := s.runner.Execute(ctx, run, s.runs.Recorder(start.ID, masks))
	s.runs.Finish(start.ID, masks, out, err)
	s.metrics.run(ctx, start.Kind, err, time.Since(began))

	return out, err
}

// Cancel stops a run in flight on this replica; false when it is not
// running here.
func (s *Service) Cancel(id string) bool {
	s.mu.Lock()
	cancel, ok := s.running[id]
	s.mu.Unlock()

	if ok {
		cancel()
	}

	return ok
}

// CallActivity calls one activity over the direct transport (the SDK's
// ActivityService on the service's platform port), bounded by timeout.
func (s *Service) CallActivity(
	ctx context.Context, call *backplanev1.ActivityCall, timeout time.Duration,
) (*backplanev1.ActivityResult, error) {
	return s.caller.Call(ctx, call, timeout) //nolint:wrapcheck // read by Readable and Retryable
}

// Runtime is the runtime the active binding of hook runs on; a hook
// without a binding answers directly ({} or NoBinding), so it is direct.
func (s *Service) Runtime(ctx context.Context, hook string) (consolev1.Runtime, error) {
	b, version, err := s.bindings.Active(ctx, hook)
	if errors.Is(err, bindings.ErrNoBinding) {
		return consolev1.Runtime_RUNTIME_DIRECT, nil
	}

	if err != nil {
		return consolev1.Runtime_RUNTIME_UNSPECIFIED, fmt.Errorf("binding of %s: %w", hook, err)
	}

	prog, err := s.Program(hook, b, version)
	if err != nil {
		return consolev1.Runtime_RUNTIME_UNSPECIFIED, fmt.Errorf("binding of %s: %w", hook, err)
	}

	return prog.Runtime(), nil
}

// CallHook answers one hook call: the run of its binding on the binding's
// runtime, {} for an optional hook without one, or a failure. The error is
// a *Failure (NoBindingType, HookFailedType, the run's), ErrTemporalRuntime
// or the context's.
func (s *Service) CallHook(ctx context.Context, call *backplanev1.HookCall) (*backplanev1.HookResult, error) {
	res, _, err := s.RaiseHook(ctx, call)

	return res, err
}

// RaiseHook is CallHook that also names the run it made on the direct
// runtime ("" otherwise: no binding, or a Temporal run).
func (s *Service) RaiseHook(ctx context.Context, call *backplanev1.HookCall) (*backplanev1.HookResult, string, error) {
	hook := call.GetHook()

	payload := call.GetPayload()
	if len(payload) > 0 && !json.Valid(payload) {
		return nil, "", failure("input of "+hook+" is not JSON", wire.HookFailedType)
	}

	b, version, err := s.bindings.Active(ctx, hook)

	switch {
	case errors.Is(err, bindings.ErrNoBinding):
		if s.Required(hook, call.GetInstance()) {
			return nil, "", failure("no binding for "+hook, wire.NoBindingType)
		}

		return &backplanev1.HookResult{Payload: []byte("{}")}, "", nil
	case err != nil:
		return nil, "", &Failure{Msg: "binding of " + hook + ": " + err.Error(), Type: wire.HookFailedType, Retryable: true}
	}

	prog, err := s.Program(hook, b, version)
	if err != nil {
		var invalid *bindings.InvalidError
		if errors.As(err, &invalid) {
			return nil, "", failure(fmt.Sprintf("binding %s@%d does not compile against the current manifests: %v",
				hook, version, err), wire.HookFailedType)
		}

		return nil, "", &Failure{Msg: "binding of " + hook + ": " + err.Error(), Type: wire.HookFailedType, Retryable: true}
	}

	if prog.Runtime() != consolev1.Runtime_RUNTIME_DIRECT {
		if s.temporal == nil {
			return nil, "", ErrTemporalRuntime
		}

		res, err := s.temporal.RunBinding(ctx, hook, prog, version, call)

		return res, "", err //nolint:wrapcheck // the runner's error as is
	}

	if d := call.GetDeadline().AsDuration(); d > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	ctx = continueTrace(ctx, call.GetTrace())
	id := wire.DirectRunID(hook, uuid.NewString())

	out, err := s.Execute(ctx, Start{
		ID: id, Kind: KindBinding, Source: hook, Version: version, Caller: call.GetInstance(), Input: payload,
	}, Run{
		ID: id, Kind: KindBinding, Program: prog, Payload: payload, Identity: identity(hook, version),
		Trace: call.GetTrace(),
	}, false)
	if err != nil {
		return nil, id, err
	}

	return &backplanev1.HookResult{Payload: out}, id, nil
}

// identity is "<hook>@<version>" ("@draft" for an unsaved definition).
func identity(source string, version int64) string {
	if version <= 0 {
		return source + "@draft"
	}

	return fmt.Sprintf("%s@%d", source, version)
}

// continueTrace puts the envelope's trace under ctx when it brought none.
func continueTrace(ctx context.Context, carrier map[string]string) context.Context {
	if len(carrier) == 0 {
		return ctx
	}

	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(carrier))
}
