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

	h.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), h.budget)
		defer cancel()

		if err := h.svc.Stop(ctx); err != nil {
			h.t.Errorf("backplanetest: stop: %v", err)
		}
	})

	if err := h.svc.Start(h.t.Context()); err != nil {
		h.t.Fatalf("backplanetest: start: %v", err)
	}
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
