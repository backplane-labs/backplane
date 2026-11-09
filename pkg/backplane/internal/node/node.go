// Package node is the service's one lifecycle model: a tree of nodes, each
// with a name, a logger, an instrumentation scope, start and stop hooks, its
// own goroutines and an optional readiness probe.
//
// The SDK's own parts (config, listeners, presence) and the author's
// components and dependencies are nodes of the same tree. Start walks the
// tree depth-first in creation order; Stop walks it back in exact reverse, so
// a node always stops before anything created ahead of it — a component
// before the dependency it uses, a child before its parent.
//
// Stopping a node cancels its context, runs its stop hooks in reverse, then
// waits for its goroutines. A node whose start failed is stopped too: stop
// hooks must tolerate a partial start.
package node

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xprobe/pkg/probe"
	"github.com/gopherex/xshutdown/lifecycle"
	"github.com/gopherex/xtrace"
)

// Lifecycle errors are shared with xshutdown.
var (
	ErrStopTimeout = lifecycle.ErrStopTimeout
	ErrPanic       = lifecycle.ErrPanic
)

// Kind of node.
type Kind int

// Node kinds. System nodes are the SDK's own; Root is the author's tree.
const (
	System Kind = iota
	Root
	Component
	Dependency
	Singleton
)

// Tree is shared by every node of one service.
type Tree struct {
	service string
	log     *xlog.Logger
	env     any

	mu       sync.Mutex
	interval time.Duration
}

// DefaultProbeInterval is the ProbeInterval of a tree that sets none.
const DefaultProbeInterval = 5 * time.Second

// Node is one element of the tree.
type Node struct {
	tree     *Tree
	parent   *Node
	ns       *namespace
	name     string
	path     string
	kind     Kind
	optional bool
	log      *xlog.Logger
	scope    *xtrace.Scope

	mu        sync.Mutex
	children  []*Node
	ready     probe.Probe
	live      probe.Probe
	condition func() Condition
	life      *lifecycle.Node
}

// namespace keeps paths unique under a Root (or under the service root for
// system nodes).
type namespace struct {
	mu    sync.Mutex
	paths map[string]bool
}

// New creates a tree and returns its service root. env is service-wide data
// any node can reach through Env.
func New(service string, log *xlog.Logger, env any) *Node {
	t := &Tree{service: service, log: log, env: env}

	return &Node{
		tree: t, ns: &namespace{paths: map[string]bool{}}, name: service, kind: System,
		log: log, scope: xtrace.New(service), life: lifecycle.New(service),
	}
}

// Child creates a node under n. Names are unique within a namespace: a
// duplicate path gets a numeric suffix. After Start only a starting parent
// may create children (a dependency building its internals); anything else
// is a programming error and panics.
func (n *Node) Child(name string, kind Kind, optional bool) *Node {
	t := n.tree

	n.mu.Lock()
	defer n.mu.Unlock()

	ns, path := n.ns, name
	switch {
	case kind == Root:
		ns, path = &namespace{paths: map[string]bool{}}, ""
	case n.kind != System && n.kind != Root:
		path = n.path + "/" + name
	}

	path = ns.claim(path)

	scope := t.service
	if path != "" {
		scope += "/" + path
	}

	child := &Node{
		tree: t, parent: n, ns: ns, name: name, path: path, kind: kind, optional: optional,
		scope: xtrace.New(scope),
	}

	switch kind {
	case System:
		child.log = t.log.With(xlog.String("component", name))
	case Root:
		child.log = t.log
	default:
		child.log = t.log.With(xlog.String("node", path))
	}

	child.life = n.life.Child(child.label())
	n.children = append(n.children, child)

	return child
}

func (ns *namespace) claim(path string) string {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	for i, base := 2, path; ns.paths[path]; i++ {
		path = base + "-" + strconv.Itoa(i)
	}

	ns.paths[path] = true

	return path
}

// SetProbeInterval sets how often the service evaluates its health, for
// nodes that check something periodically; d <= 0 keeps the default.
func (n *Node) SetProbeInterval(d time.Duration) {
	n.tree.mu.Lock()
	defer n.tree.mu.Unlock()

	n.tree.interval = d
}

// ProbeInterval is how often the service evaluates its health.
func (n *Node) ProbeInterval() time.Duration {
	n.tree.mu.Lock()
	defer n.tree.mu.Unlock()

	if n.tree.interval <= 0 {
		return DefaultProbeInterval
	}

	return n.tree.interval
}

// Name of the node.
func (n *Node) Name() string { return n.name }

// Path of the node within its namespace ("" for a root).
func (n *Node) Path() string { return n.path }

// Kind of the node.
func (n *Node) Kind() Kind { return n.kind }

// Optional reports whether the node is an optional dependency.
func (n *Node) Optional() bool { return n.optional }

// Log is the node's logger.
func (n *Node) Log() *xlog.Logger { return n.log }

// Scope is the node's instrumentation scope.
func (n *Node) Scope() *xtrace.Scope { return n.scope }

// Env is the service-wide data given to New.
func (n *Node) Env() any { return n.tree.env }

func (n *Node) label() string {
	switch {
	case n.path != "":
		return n.path
	case n.kind == Root:
		return "root"
	default:
		return n.name
	}
}

// OnStart adds a startup hook.
func (n *Node) OnStart(fn func(context.Context) error) { n.life.OnStart(fn) }

// OnStop adds a cleanup hook, invoked in reverse order.
func (n *Node) OnStop(fn func(context.Context) error) { n.life.OnStop(fn) }

// Go registers work owned by this node's lifetime.
func (n *Node) Go(fn func(context.Context) error) { n.life.Go(fn) }

// Failed delivers the first background failure of the tree.
func (n *Node) Failed() <-chan error { return n.life.Failed() }

// Ready makes p part of the service readiness.
func (n *Node) Ready(p probe.Probe) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.ready = p
}

// Live makes p part of the service liveness: it fails only for a state
// the process cannot leave without a restart.
func (n *Node) Live(p probe.Probe) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.live = p
}

// Condition is a node's current state as a dependency: ready, or why not.
type Condition struct {
	Ready bool
	Err   error
}

// SetCondition installs what Condition reports.
func (n *Node) SetCondition(fn func() Condition) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.condition = fn
}

// Condition of the node; false for a node that reports none.
func (n *Node) Condition() (Condition, bool) {
	n.mu.Lock()
	fn := n.condition
	n.mu.Unlock()

	if fn == nil {
		return Condition{}, false
	}

	return fn(), true
}

// Budget narrows this node's shutdown deadline without changing its children.
func (n *Node) Budget(fn func(context.Context) (context.Context, context.CancelFunc)) {
	n.life.Budget(fn)
}

// Start starts the lifecycle tree.
func (n *Node) Start(ctx context.Context) error {
	if err := n.life.Start(ctx); err != nil {
		return fmt.Errorf("node: start: %w", err)
	}

	return nil
}

// Stop unwinds the lifecycle tree.
func (n *Node) Stop(ctx context.Context) error {
	if err := n.life.Stop(ctx); err != nil {
		return fmt.Errorf("node: stop: %w", err)
	}

	return nil
}

func (n *Node) snapshot() []*Node {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]*Node(nil), n.children...)
}

// Children are n's direct children in creation (start) order.
func (n *Node) Children() []*Node { return n.snapshot() }

// Walk visits n's subtree depth-first in creation order.
func (n *Node) Walk(fn func(*Node)) {
	fn(n)

	for _, c := range n.snapshot() {
		c.Walk(fn)
	}
}

// Readiness is the readiness of n's subtree, evaluated on every check so
// nodes created during start count too.
func (n *Node) Readiness() probe.Probe {
	return n.composite(func(c *Node) probe.Probe { return c.ready })
}

// Liveness is the liveness of n's subtree, evaluated like Readiness.
func (n *Node) Liveness() probe.Probe {
	return n.composite(func(c *Node) probe.Probe { return c.live })
}

// composite checks the probe pick gives of every node of n's subtree.
func (n *Node) composite(pick func(c *Node) probe.Probe) probe.Probe {
	return probe.ResultFunc(func(ctx context.Context) probe.Result {
		var probes []probe.Probe

		n.Walk(func(c *Node) {
			c.mu.Lock()
			if p := pick(c); p != nil {
				probes = append(probes, probe.WithName(c.label(), p))
			}
			c.mu.Unlock()
		})

		return probe.All(probes...).CheckResult(ctx)
	})
}

// Group runs goroutines owned by a node; *Node is one.
type Group interface {
	Go(fn func(ctx context.Context) error)
}
