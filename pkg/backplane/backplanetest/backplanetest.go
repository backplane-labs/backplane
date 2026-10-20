// Package backplanetest runs components without Open, Run, ports or
// Consul: build them under Root, Start, and inspect what they published,
// answer their hooks, invoke their activities and reactors, and check their
// readiness. Native workflow tests use the optional workflows/workflowtest package.
//
//	h := backplanetest.New(t, backplanetest.Name("hello"))
//	g, _ := greeter.New(h.Root(), &cfg, deps.Static(db))
//	h.Start()
//	backplanetest.Events(h, g.Greeted()) // what g published
//
// Transports are replaced by a recorder: Publish is recorded (FailPublish
// and Unavailable make it fail), Call is answered by Answer, reactors run
// through React and activities through Activity, both with the metadata a
// transport puts on the handler's context (event.DeliveryOf,
// activity.InfoOf). The optional workflows/workflowtest package builds a Temporal
// testsuite environment with the same registrations and hook answers.
package backplanetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gopherex/xprobe/pkg/probe"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

const (
	defaultService    = "test"
	defaultStopBudget = 10 * time.Second
	readyTimeout      = 5 * time.Second
)

// Errors.
var (
	// ErrNotDeclared: no activity or reactor is declared under that name.
	ErrNotDeclared = errors.New("backplanetest: not declared")
	// ErrNotReady: the tree's readiness is down (Ready).
	ErrNotReady = errors.New("backplanetest: not ready")
)

// Option configures New.
type Option func(o *options)

type options struct {
	service string
	budget  time.Duration
}

// Name is the service's name (default "test"): events are published as
// <name>.<Event>, hooks called as <name>.<Name>, and a reactor to the
// service's own event subscribes to "<name>.<Event>". It panics on a name
// that is not a service name ([a-z][a-z0-9-]*).
func Name(service string) Option {
	if !validService(service) {
		panic(fmt.Sprintf("backplanetest: service name %q must match [a-z][a-z0-9-]*", service))
	}

	return func(o *options) { o.service = service }
}

// StopBudget bounds the stop at test cleanup (default 10s): a component
// whose goroutines outlive it fails the test.
func StopBudget(d time.Duration) Option {
	if d <= 0 {
		panic(fmt.Sprintf("backplanetest: StopBudget must be positive, got %v", d))
	}

	return func(o *options) { o.budget = d }
}

func validService(s string) bool {
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && (r >= '0' && r <= '9' || r == '-'):
		default:
			return false
		}
	}

	return s != ""
}

// Harness is one service's tree, started by Start and stopped at cleanup.
type Harness struct {
	t      testing.TB
	svc    *node.Node
	app    *node.Node
	env    *env.Env
	rec    *recorder
	budget time.Duration

	mu    sync.Mutex
	bound []string // answered hooks, short names

	stopOnce sync.Once
}

// New creates a harness for a service named "test", or as Name says.
func New(tb testing.TB, opts ...Option) *Harness {
	tb.Helper()

	o := options{service: defaultService, budget: defaultStopBudget}
	for _, opt := range opts {
		opt(&o)
	}

	m := manifest.New(o.service, "0.0.0")
	e := env.New(o.service, m)
	rec := newRecorder()
	e.SetBroker(rec)
	e.SetCaller(rec)
	e.SetBound([]string{})

	svc := node.New(o.service, testlog.Discard(), e)

	return &Harness{
		t: tb, svc: svc, app: svc.Child(o.service, node.Root, false), env: e, rec: rec, budget: o.budget,
	}
}

// bind marks the hook (full name) bound.
func (h *Harness) bind(full string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.bound = append(h.bound, strings.TrimPrefix(full, h.env.Service+"."))
	h.env.SetBound(slices.Clone(h.bound))
}

// Service is the harness's service name.
func (h *Harness) Service() string { return h.env.Service }

// Root is where components and dependencies under test are created.
func (h *Harness) Root() deps.Component {
	return link.Scope(h.app).(deps.Component) //nolint:forcetypeassert,errcheck // deps installs it
}

// Start starts the tree (dependencies are provided, goroutines run) and
// stops it at test cleanup within the StopBudget; a failure fails the
// test.
func (h *Harness) Start() {
	h.t.Helper()

	h.t.Cleanup(h.stopTree)

	if err := h.svc.Start(h.t.Context()); err != nil {
		h.t.Fatalf("backplanetest: start: %v", err)
	}
}

// Stop stops the whole tree now, as the cleanup would: goroutines are
// cancelled and waited for and OnStop hooks run in reverse start order,
// within the StopBudget; a failure fails the test. The cleanup then has
// nothing left to stop. A restarted instance is a new Harness.
func (h *Harness) Stop() {
	h.t.Helper()
	h.stopTree()
}

func (h *Harness) stopTree() {
	h.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), h.budget)
		defer cancel()

		if err := h.svc.Stop(ctx); err != nil {
			h.t.Errorf("backplanetest: stop: %v", err)
		}
	})
}

// Stop stops one component's subtree now — a projection, a consumer, a
// worker killed in the middle of a test — while the rest of the tree runs:
// its goroutines are cancelled (their context ends wherever they are) and
// waited for, its children and then its own OnStop hooks run, within the
// StopBudget. A failure fails the test. It stays stopped: what it served is
// gone for the components that use it, as in a crash; the tree's cleanup
// skips it. scope must be a component created under h.Root.
func Stop(h *Harness, scope deps.Scope) {
	h.t.Helper()

	n := link.NodeOf(scope)
	if n == nil || !h.owns(n) {
		h.t.Fatalf("backplanetest: Stop: %s is not a component of this harness", scopeName(scope))
	}

	ctx, cancel := context.WithTimeout(context.Background(), h.budget)
	defer cancel()

	if err := n.Stop(ctx); err != nil {
		h.t.Errorf("backplanetest: stop %s: %v", n.Path(), err)
	}
}

// Find is the node of h's tree named name — its Name, or its Path
// under the root ("sync/fanout") — as a scope Stop takes: a test reaches a component
// the service does not export. It fails the test when no node or more than
// one matches (the failure lists the paths).
func Find(h *Harness, name string) deps.Component {
	h.t.Helper()

	var found []*node.Node

	h.app.Walk(func(n *node.Node) {
		if n != h.app && (n.Name() == name || n.Path() == name) {
			found = append(found, n)
		}
	})

	switch len(found) {
	case 1:
		return link.Scope(found[0]).(deps.Component) //nolint:forcetypeassert,errcheck // deps installs it
	case 0:
		var paths []string

		h.app.Walk(func(n *node.Node) {
			if n != h.app {
				paths = append(paths, n.Path())
			}
		})

		h.t.Fatalf("backplanetest: Find: no node named %q; the tree: %s", name, strings.Join(paths, ", "))
	default:
		paths := make([]string, 0, len(found))
		for _, n := range found {
			paths = append(paths, n.Path())
		}

		h.t.Fatalf("backplanetest: Find: %q names %d nodes, use a path: %s", name, len(found), strings.Join(paths, ", "))
	}

	return deps.Component{}
}

// owns reports whether n is in the tree under Root (Root itself included).
func (h *Harness) owns(n *node.Node) bool {
	found := false

	h.app.Walk(func(c *node.Node) { found = found || c == n })

	return found
}

func scopeName(scope deps.Scope) string {
	if scope == nil {
		return "nil"
	}

	return scope.Path()
}

// Manifest is what the components declared so far.
func (h *Harness) Manifest() *backplanev1.Manifest {
	h.t.Helper()

	m, err := h.env.Manifest.Build()
	if err != nil {
		h.t.Fatalf("backplanetest: manifest: %v", err)
	}

	return m
}

// Ready evaluates the tree's readiness as the service's readiness probe
// does: every required dependency provided and passing its probe. nil when
// ready; otherwise an error matching ErrNotReady that names each
// dependency that is not and why. Optional dependencies never count.
func Ready(h *Harness) error {
	ctx, cancel := context.WithTimeout(h.t.Context(), readyTimeout)
	defer cancel()

	if h.app.Readiness().Check(ctx) == probe.StatusUp {
		return nil
	}

	var errs []error

	h.app.Walk(func(n *node.Node) {
		if n.Optional() {
			return
		}

		if c, ok := n.Condition(); ok && (!c.Ready || c.Err != nil) {
			err := c.Err
			if err == nil {
				err = deps.ErrNotReady
			}

			errs = append(errs, fmt.Errorf("%s: %w", n.Path(), err))
		}
	})

	if len(errs) == 0 {
		return fmt.Errorf("%w: a readiness probe is down", ErrNotReady)
	}

	return fmt.Errorf("%w: %w", ErrNotReady, errors.Join(errs...))
}

// SetLive changes a Live field as the console would; watchers fire.
func SetLive[T any](l *config.Live[T], v T) { link.SetLive(l, v) }
