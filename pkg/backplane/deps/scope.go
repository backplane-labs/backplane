// Package deps is how a service is composed: a tree of components,
// dependencies and singletons built explicitly by the author's constructor.
// Every node is a Scope with its own name, logger and instrumentation.
//
// Nodes start depth-first in creation order and stop in exact reverse, so a
// component (and its goroutines) always stops before the dependencies it
// was given. Create nodes before Run; a dependency may create child nodes
// while it is being provided.
//
// It does not depend on the backplane package, so contrib providers need
// only this one.
package deps

import (
	"context"
	"io"
	"sync"

	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xtrace"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

//nolint:gochecknoinits // installs the private accessor for SDK packages
func init() {
	link.NodeOf = func(s any) *node.Node {
		if sc, ok := s.(Scope); ok {
			return sc.node()
		}

		return nil
	}
	link.Scope = func(n *node.Node) any { return Component{n: n} }
}

// Scope is a node of the service tree.
type Scope interface {
	// Name of the node; Path is its place in the tree ("users/templates").
	Name() string
	Path() string
	// Log carries node=<path>.
	Log() *xlog.Logger
	Tracer() trace.Tracer
	Meter() metric.Meter
	// Span runs fn in a span of this node's tracer.
	Span(ctx context.Context, name string, fn func(ctx context.Context) error, opts ...xtrace.SpanOption) error
	// Go runs fn once the node has started, on a context cancelled when the
	// node stops; the stop waits for it. An error stops the service.
	Go(fn func(ctx context.Context) error)
	// OnStart and OnStop hook the node into the lifecycle. Stop hooks run
	// in reverse and must tolerate a start that failed half way.
	OnStart(fn func(ctx context.Context) error)
	OnStop(fn func(ctx context.Context) error)

	node() *node.Node
}

// Component is a named node of the author's code. Embed it:
//
//	type UserRepo struct {
//	    deps.Component
//	    pool deps.Dependency[*pgxpool.Pool]
//	}
//
// A zero Component logs nowhere and traces nothing; hooking it into the
// lifecycle panics.
type Component struct {
	n *node.Node
}

// NewComponent creates a component under parent.
func NewComponent(parent Scope, name string) Component {
	return Component{n: nodeOf(parent).Child(name, node.Component, false)}
}

// IsZero reports whether c was not created by NewComponent.
func (c Component) IsZero() bool { return c.n == nil }

// Name of the component.
func (c Component) Name() string {
	if c.n == nil {
		return ""
	}

	return c.n.Name()
}

// Path of the component in the tree.
func (c Component) Path() string {
	if c.n == nil {
		return ""
	}

	return c.n.Path()
}

// Log is the component's logger.
func (c Component) Log() *xlog.Logger {
	if c.n == nil {
		return discard()
	}

	return c.n.Log()
}

// Tracer of the component.
func (c Component) Tracer() trace.Tracer {
	if c.n == nil {
		return tracenoop.NewTracerProvider().Tracer("")
	}

	return c.n.Scope().Tracer()
}

// Meter of the component.
func (c Component) Meter() metric.Meter {
	if c.n == nil {
		return metricnoop.NewMeterProvider().Meter("")
	}

	return c.n.Scope().Meter()
}

// Span runs fn in a span named name.
func (c Component) Span(
	ctx context.Context, name string, fn func(ctx context.Context) error, opts ...xtrace.SpanOption,
) error {
	if c.n == nil {
		return fn(ctx)
	}

	//nolint:wrapcheck // fn's error is returned as is
	return c.n.Scope().Run(ctx, name, func(ctx context.Context, _ trace.Span) error { return fn(ctx) }, opts...)
}

// Go runs fn under the component's lifecycle.
func (c Component) Go(fn func(ctx context.Context) error) { c.must().Go(fn) }

// OnStart hooks fn into the component's start.
func (c Component) OnStart(fn func(ctx context.Context) error) { c.must().OnStart(fn) }

// OnStop hooks fn into the component's stop.
func (c Component) OnStop(fn func(ctx context.Context) error) { c.must().OnStop(fn) }

func (c Component) node() *node.Node { return c.n }

func (c Component) must() *node.Node {
	if c.n == nil {
		panic("deps: zero Component: create it with deps.NewComponent")
	}

	return c.n
}

// SpanValue runs fn in a span of s and returns its value.
func SpanValue[T any](
	ctx context.Context, s Scope, name string, fn func(ctx context.Context) (T, error), opts ...xtrace.SpanOption,
) (T, error) {
	var out T

	err := s.Span(ctx, name, func(ctx context.Context) error {
		v, err := fn(ctx)
		out = v

		return err
	}, opts...)

	return out, err //nolint:wrapcheck // fn's error is returned as is
}

func nodeOf(s Scope) *node.Node {
	if s == nil || s.node() == nil {
		panic("deps: parent scope is zero: create nodes under the Root or a component")
	}

	return s.node()
}

//nolint:gochecknoglobals // one shared sink for zero scopes
var discard = sync.OnceValue(func() *xlog.Logger { return xlog.NewJSON(xlog.WithWriter(io.Discard)) })
