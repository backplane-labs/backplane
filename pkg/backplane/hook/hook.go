// Package hook declares operations a service calls without implementing
// them ("нужно"). Who answers is a binding in backplane; the call is a
// Temporal Nexus operation <Name> of service <service>.Hooks on endpoint
// <service>. Call works from any Go code; workflows.CallHook is the same call from
// workflow code. Without Temporal Call returns ErrUnavailable.
//
// Inputs and outputs evolve additively: a reader ignores fields it does not
// know (protojson with DiscardUnknown), a missing field reads as its zero
// value, and renaming a field means adding a new one and keeping the old
// until every reader uses the new.
//
//	price := hook.Declare[Quote, Price](root, "Price", hook.DefaultTimeout(5*time.Second),
//	    hook.Describe("price of a quote"))
//	out, err := price.Call(ctx, q, hook.Key(q.ID), hook.Timeout(2*time.Second))
package hook

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
)

var (
	// ErrUnavailable is returned while no transport carries hook calls.
	ErrUnavailable = env.ErrUnavailable
	// ErrNoBinding: backplane has no binding for the hook.
	ErrNoBinding = env.ErrNoBinding
)

// Ref is a declared hook. The zero Ref is undeclared: Call fails.
type Ref[Req, Res any] struct {
	env     *env.Env
	name    string
	timeout time.Duration // declared default; 0: the platform's
}

// Option configures Declare.
type Option interface{ apply(o *options) }

type options struct {
	required    bool
	timeout     time.Duration
	description string
}

type optionFunc func(o *options)

func (f optionFunc) apply(o *options) { f(o) }

// Required marks the hook as one the service cannot work without.
func Required() Option { return optionFunc(func(o *options) { o.required = true }) }

// DefaultTimeout is the deadline of a call that sets none with Timeout;
// without it the platform default applies (backplane.temporal.hook_timeout,
// 30s). The manifest carries it for the console. d <= 0 is ignored.
func DefaultTimeout(d time.Duration) Option {
	return optionFunc(func(o *options) {
		if d > 0 {
			o.timeout = d
		}
	})
}

// Describe is the hook's description for the console.
func Describe(s string) Option { return optionFunc(func(o *options) { o.description = s }) }

// Declare registers a hook in the manifest. name is CamelCase
// ([A-Z][A-Za-z0-9]*), anything else panics. Schemas are reflected from Req
// and Res when possible; without them the payload is plain JSON (protojson
// for proto messages).
func Declare[Req, Res any](scope deps.Scope, name string, opts ...Option) Ref[Req, Res] {
	e, _ := decl.Env(scope, "hook "+name)
	decl.CheckName("hook", name)

	var o options
	for _, opt := range opts {
		opt.apply(&o)
	}

	h := &backplanev1.Hook{
		Name:        name,
		Input:       decl.Schema[Req](e.Service, "hook_"+name+"_in"),
		Output:      decl.Schema[Res](e.Service, "hook_"+name+"_out"),
		Required:    o.required,
		Description: o.description,
	}
	if o.timeout > 0 {
		h.Timeout = durationpb.New(o.timeout)
	}

	e.Manifest.Hook(h)

	return Ref[Req, Res]{env: e, name: name, timeout: o.timeout}
}

// CallOption tunes one call.
type CallOption interface{ applyCall(o *callOptions) }

type callOptions struct {
	key     string
	timeout time.Duration
}

type callFunc func(o *callOptions)

func (f callFunc) applyCall(o *callOptions) { f(o) }

// Key makes the call idempotent under k: calls with the same key run the
// binding once. A call while the first runs waits for it; a call after it
// succeeded gets its result (the input of the later call is not looked at);
// a call after it failed runs again. The workflow id is
// hook/<service>/<Name>/<k>; Temporal remembers it for the namespace's
// retention. workflows.CallHook ignores the key: a workflow is durable already.
func Key(k string) CallOption { return callFunc(func(o *callOptions) { o.key = k }) }

// Timeout is the call's deadline, over the declared DefaultTimeout; the
// context's own deadline still applies when earlier. d <= 0 is ignored.
func Timeout(d time.Duration) CallOption {
	return callFunc(func(o *callOptions) {
		if d > 0 {
			o.timeout = d
		}
	})
}

// Name is the full hook name <service>.<Name>.
func (r Ref[Req, Res]) Name() string {
	if r.env == nil {
		return ""
	}

	return r.env.Service + "." + r.name
}

// Call raises the hook and waits for the bound implementation. The
// deadline is the earliest of ctx's and the call's own: Timeout, else the
// declared DefaultTimeout, else — when ctx has none — the platform default.
// Outside workflow code only: in a workflow use workflows.CallHook.
func (r Ref[Req, Res]) Call(ctx context.Context, in Req, opts ...CallOption) (Res, error) {
	var out Res

	if r.env == nil {
		return out, fmt.Errorf("hook: call on an undeclared Ref: %w", ErrUnavailable)
	}

	o := r.callOptions(opts)
	start := time.Now()

	out, err := r.call(ctx, in, o)
	metrics.HookCall(ctx, r.Name(), outcome(err), time.Since(start))

	return out, err
}

func (r Ref[Req, Res]) call(ctx context.Context, in Req, o callOptions) (Res, error) {
	var out Res

	t := r.env.Caller()
	if t == nil {
		return out, fmt.Errorf("hook %s: %w", r.Name(), ErrUnavailable)
	}

	payload, err := decl.Encode(in)
	if err != nil {
		return out, fmt.Errorf("hook %s: encode: %w", r.Name(), err)
	}

	if o.timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}

	res, err := t.Call(env.WithCallKey(ctx, o.key), r.Name(), payload)
	if err != nil {
		return out, fmt.Errorf("hook %s: %w", r.Name(), err)
	}

	if err := decl.Decode(res, &out); err != nil {
		return out, fmt.Errorf("hook %s: decode: %w", r.Name(), err)
	}

	return out, nil
}

// callOptions applies opts over the declared default timeout.
func (r Ref[Req, Res]) callOptions(opts []CallOption) callOptions {
	o := callOptions{timeout: r.timeout}
	for _, opt := range opts {
		opt.applyCall(&o)
	}

	return o
}

// outcome of a call for the metric.
func outcome(err error) string {
	switch {
	case err == nil:
		return metrics.OK
	case errors.Is(err, ErrNoBinding):
		return metrics.NoBinding
	case errors.Is(err, ErrUnavailable):
		return metrics.Unavailable
	case errors.Is(err, context.DeadlineExceeded):
		return metrics.Timeout
	default:
		return metrics.Error
	}
}

// CallTimeout resolves the declared, per-call and transport default timeout.
// Native workflow adapters use it without importing a backend into hook.
func (r Ref[Req, Res]) CallTimeout(opts ...CallOption) time.Duration {
	o := r.callOptions(opts)
	if o.timeout > 0 {
		return o.timeout
	}

	if r.env != nil {
		return r.env.HookTimeout()
	}

	return env.DefaultHookTimeout
}
