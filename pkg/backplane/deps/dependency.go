package deps

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

// Errors.
var (
	// ErrNotReady: the dependency has not been provided (yet).
	ErrNotReady = errors.New("deps: not ready")
	// ErrClosed: the node has stopped.
	ErrClosed = errors.New("deps: closed")
)

const (
	defaultMinDelay       = time.Second
	defaultMaxDelay       = 30 * time.Second
	defaultProvideTimeout = 30 * time.Second
	lateCloseBudget       = 5 * time.Second
)

// Dependency is a required external resource: provided when its node
// starts (the start retries until it succeeds), part of readiness through
// its probe, closed at stop. Public and internal traffic starts only after
// every required dependency is ready and stops before they close, so Get
// always holds the value where handlers run.
type Dependency[T any] struct {
	c *cell[T]
}

// Optional is a dependency the service works without: provided in the
// background with retries, never part of readiness. Get reports whether it
// is there right now: provided and, with ProbeOptional, passing its probe.
type Optional[T any] struct {
	c *cell[T]
}

type cell[T any] struct {
	node     *node.Node
	provider Provider[T]
	opts     nodeOptions

	mu      sync.RWMutex
	value   T
	ready   bool
	failing error // ProbeOptional: the last probe failed; the value is hidden
	probed  error // required: the last readiness probe's error
	closed  bool
	err     error
}

// Option configures NewDependency.
type Option interface{ applyDependency(o *nodeOptions) }

// OptionalOption configures NewOptional.
type OptionalOption interface{ applyOptional(o *nodeOptions) }

// SingletonOption configures NewSingleton.
type SingletonOption interface{ applySingleton(o *nodeOptions) }

// DependencyOption applies to required and optional dependencies.
type DependencyOption interface {
	Option
	OptionalOption
}

type nodeOptions struct {
	name           string
	policy         backoff.Policy
	provideTimeout time.Duration
	probeTimeout   time.Duration
	probeOptional  bool
}

type nameOption string

func (n nameOption) applyDependency(o *nodeOptions) { o.name = string(n) }
func (n nameOption) applyOptional(o *nodeOptions)   { o.name = string(n) }
func (n nameOption) applySingleton(o *nodeOptions)  { o.name = string(n) }

// NameOption applies to dependencies and singletons.
type NameOption interface {
	Option
	OptionalOption
	SingletonOption
}

// Name overrides the provider's name: two databases of one kind.
func Name(name string) NameOption { return nameOption(name) }

// setter is an option that only sets a field.
type setter func(o *nodeOptions)

func (s setter) applyDependency(o *nodeOptions) { s(o) }
func (s setter) applyOptional(o *nodeOptions)   { s(o) }

// Backoff bounds the delay between provide attempts (default 1s..30s,
// jittered). It panics unless 0 < minDelay <= maxDelay.
func Backoff(minDelay, maxDelay time.Duration) DependencyOption {
	p := backoff.Policy{Min: minDelay, Max: maxDelay}
	if err := p.Validate(); err != nil {
		panic("deps: " + err.Error())
	}

	return setter(func(o *nodeOptions) { o.policy = p })
}

// ProvideTimeout bounds one provide attempt (default 30s); a timed-out
// attempt is retried like a failed one. The context Provide gets ends with
// the attempt: a provider must not keep it. It panics unless d > 0.
func ProvideTimeout(d time.Duration) DependencyOption {
	if d <= 0 {
		panic("deps: ProvideTimeout must be positive")
	}

	return setter(func(o *nodeOptions) { o.provideTimeout = d })
}

// ProbeTimeout bounds one probe (default: the service's health timeout for
// a required dependency, its health interval under ProbeOptional). It
// panics unless d > 0.
func ProbeTimeout(d time.Duration) DependencyOption {
	if d <= 0 {
		panic("deps: ProbeTimeout must be positive")
	}

	return setter(func(o *nodeOptions) { o.probeTimeout = d })
}

type probeOptional struct{}

func (probeOptional) applyOptional(o *nodeOptions) { o.probeOptional = true }

// ProbeOptional probes a provided optional dependency every health interval
// of the service: while its probe fails, Get reports it absent and Err says
// why; the value is kept and comes back when the probe passes. It never
// enters readiness.
func ProbeOptional() OptionalOption { return probeOptional{} }

func newCell[T any](parent Scope, p Provider[T], optional bool, o nodeOptions) *cell[T] {
	c := &cell[T]{
		node:     nodeOf(parent).Child(o.name, node.Dependency, optional),
		provider: p, opts: o, err: ErrNotReady,
	}
	c.node.OnStop(c.close)
	c.node.SetCondition(c.condition)

	return c
}

// condition is the dependency's state for the instance state: provided and
// passing its last probe, or why not.
func (c *cell[T]) condition() node.Condition {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, err := range []error{c.err, c.failing, c.probed} {
		if err != nil {
			return node.Condition{Err: err}
		}
	}

	return node.Condition{Ready: c.ready}
}

func defaults(name string) nodeOptions {
	return nodeOptions{
		name:           name,
		policy:         backoff.Policy{Min: defaultMinDelay, Max: defaultMaxDelay},
		provideTimeout: defaultProvideTimeout,
	}
}

// NewDependency adds a required dependency under parent: the start waits
// for it, retrying with backoff.
func NewDependency[T any](parent Scope, p Provider[T], opts ...Option) Dependency[T] {
	o := defaults(p.Name())
	for _, opt := range opts {
		opt.applyDependency(&o)
	}

	c := newCell(parent, p, false, o)
	c.node.OnStart(func(ctx context.Context) error {
		return backoff.Retry(ctx, c.opts.policy, c.provide, c.warn)
	})
	c.node.Ready(probe.FromError(c.probe))

	return Dependency[T]{c: c}
}

// NewOptional adds an optional dependency under parent: the start does not
// wait; providing retries in the background until it succeeds or the
// service stops. With ProbeOptional it is then probed periodically.
func NewOptional[T any](parent Scope, p Provider[T], opts ...OptionalOption) Optional[T] {
	o := defaults(p.Name())
	for _, opt := range opts {
		opt.applyOptional(&o)
	}

	c := newCell(parent, p, true, o)
	c.node.Go(func(ctx context.Context) error {
		if backoff.Retry(ctx, c.opts.policy, c.provide, c.warn) == nil && c.opts.probeOptional {
			c.watch(ctx)
		}

		return nil
	})

	return Optional[T]{c: c}
}

// Static is a dependency that is always ready with v: tests, and values
// the service owns itself.
func Static[T any](v T) Dependency[T] {
	return Dependency[T]{c: &cell[T]{value: v, ready: true}}
}

// StaticOptional is an optional dependency that is present with v; the
// zero Optional is one that is absent.
func StaticOptional[T any](v T) Optional[T] {
	return Optional[T]{c: &cell[T]{value: v, ready: true}}
}

// Get returns the value. It is set wherever handlers run: they are served
// only while every required dependency is ready.
func (d Dependency[T]) Get() T {
	v, _ := d.c.get()

	return v
}

// Ready reports whether the value is provided.
func (d Dependency[T]) Ready() bool { return d.c.isReady() }

// Err is why the dependency is not ready; nil when it is.
func (d Dependency[T]) Err() error { return d.c.lastErr() }

// IsZero reports whether d was not created by NewDependency or Static.
func (d Dependency[T]) IsZero() bool { return d.c == nil }

// Get returns the value and whether it is there right now.
func (o Optional[T]) Get() (T, bool) { return o.c.get() }

// Err is why the dependency is absent; nil when it is there.
func (o Optional[T]) Err() error { return o.c.lastErr() }

// IsZero reports whether o was not created by NewOptional or
// StaticOptional (an absent dependency).
func (o Optional[T]) IsZero() bool { return o.c == nil }

func (c *cell[T]) get() (T, bool) {
	var zero T
	if c == nil {
		return zero, false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.ready || c.failing != nil {
		return zero, false
	}

	return c.value, true
}

func (c *cell[T]) isReady() bool {
	_, ok := c.get()

	return ok
}

func (c *cell[T]) lastErr() error {
	if c == nil {
		return ErrNotReady
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.err == nil {
		return c.failing
	}

	return c.err
}

func (c *cell[T]) warn(err error, in time.Duration) {
	c.node.Log().Warn("dependency unavailable, retrying", xlog.Err(err), xlog.Duration("in", in))
}

// provide makes one attempt, bounded by the provide timeout.
func (c *cell[T]) provide(ctx context.Context) error {
	attempt, cancel := context.WithTimeout(ctx, c.opts.provideTimeout)
	value, err := c.provider.Provide(attempt, Component{n: c.node})

	cancel()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()

		if err == nil {
			c.closeLate(ctx, value)
		}

		return ErrClosed
	}

	defer c.mu.Unlock()

	if err != nil {
		metrics.DependencyProvide(ctx, c.node.Path(), metrics.Error)

		c.err = fmt.Errorf("provide %s: %w", c.node.Path(), err)

		return c.err
	}

	metrics.DependencyProvide(ctx, c.node.Path(), metrics.OK)
	metrics.DependencyReady(ctx, c.node.Path(), true)

	c.value, c.ready, c.err = value, true, nil
	c.node.Log().Info("dependency ready")

	return nil
}

// closeLate releases a value provided after the node closed.
func (c *cell[T]) closeLate(ctx context.Context, value T) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lateCloseBudget)
	defer cancel()

	if err := c.provider.Close(ctx, value); err != nil {
		c.node.Log().Warn("close after stop", xlog.Err(err))
	}
}

func (c *cell[T]) probe(ctx context.Context) error {
	value, ready := c.get()
	if !ready {
		return c.lastErr()
	}

	err := c.check(ctx, value, c.opts.probeTimeout)

	c.mu.Lock()
	c.probed = err
	c.mu.Unlock()

	return err
}

// check runs the provider's probe, bounded by timeout (0: by ctx only).
func (c *cell[T]) check(ctx context.Context, value T, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if err := c.provider.Probe(ctx, value); err != nil {
		return fmt.Errorf("probe %s: %w", c.node.Path(), err)
	}

	return nil
}

// watch probes a provided optional dependency every probe interval until
// ctx ends, hiding the value while the probe fails.
func (c *cell[T]) watch(ctx context.Context) {
	interval := c.node.ProbeInterval()

	timeout := c.opts.probeTimeout
	if timeout <= 0 {
		timeout = interval
	}

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		c.mu.RLock()
		value, ready := c.value, c.ready
		c.mu.RUnlock()

		if !ready {
			return
		}

		c.setFailing(ctx, c.check(ctx, value, timeout))
	}
}

// setFailing records the outcome of a probe; a change is logged and
// reflected in the ready metric.
func (c *cell[T]) setFailing(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}

	c.mu.Lock()
	was := c.failing
	c.failing = err
	c.mu.Unlock()

	switch {
	case was == nil && err != nil:
		c.node.Log().Warn("optional dependency failing its probe", xlog.Err(err))
		metrics.DependencyReady(ctx, c.node.Path(), false)
	case was != nil && err == nil:
		c.node.Log().Info("optional dependency passing its probe again")
		metrics.DependencyReady(ctx, c.node.Path(), true)
	}
}

func (c *cell[T]) close(ctx context.Context) error {
	var zero T

	c.mu.Lock()
	value, ready := c.value, c.ready
	c.value, c.ready, c.closed, c.err = zero, false, true, ErrClosed
	c.mu.Unlock()

	if !ready {
		return nil
	}

	metrics.DependencyReady(context.WithoutCancel(ctx), c.node.Path(), false)

	if err := c.provider.Close(ctx, value); err != nil {
		return fmt.Errorf("close %s: %w", c.node.Path(), err)
	}

	return nil
}
