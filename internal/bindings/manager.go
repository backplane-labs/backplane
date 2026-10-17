package bindings

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Errors of the Manager.
var (
	ErrNotSynced = errors.New("bindings: the registry has not synced with consul yet")
	// ErrNoBinding: the hook has no binding in force (never saved or
	// deleted).
	ErrNoBinding = errors.New("bindings: no binding")
	ErrNoVersion = errors.New("bindings: no such version")
	ErrNoRule    = errors.New("bindings: no such rule")
	// ErrDeleted: the rule or binding is deleted already.
	ErrDeleted = errors.New("bindings: deleted")
	// ErrConflict: an edit started from a version that is no longer the
	// current one.
	ErrConflict = errors.New("bindings: changed since the edit started")
	// ErrManaged: the binding or rule is defined in a wiring file, which
	// alone changes it.
	ErrManaged = errors.New("bindings: managed by a wiring file")
)

// DefaultAuthor is the author of a version saved outside a console session.
const DefaultAuthor = "admin"

// defaultPoll is how often the Manager looks for saves of other replicas.
const defaultPoll = 2 * time.Second

// Option configures New.
type Option func(*Manager)

// Author names the author of a version from the request's context;
// default: DefaultAuthor.
func Author(fn func(ctx context.Context) string) Option { return func(m *Manager) { m.author = fn } }

// DefaultRuntime is the runtime of definitions that name none (the
// installation's BACKPLANE_WIRING_RUNTIME); default: Temporal.
func DefaultRuntime(rt consolev1.Runtime) Option { return func(m *Manager) { m.defaults.Runtime = rt } }

// PollInterval is how often saves of other replicas are looked for
// (default 2s): Changes fires after them too.
func PollInterval(d time.Duration) Option { return func(m *Manager) { m.poll = d } }

// Manager keeps bindings and rules: versions in PostgreSQL, validation
// against the registry's latest manifests, the console's BindingService
// and RuleService, and Changes for the executor and the rules engine. It
// holds state and a goroutine: share it by pointer.
type Manager struct {
	deps.Component

	store   deps.Dependency[*store.Store]
	src     registry.Source
	author  func(ctx context.Context) string
	poll    time.Duration
	changes *broadcast
	// defaults of definitions: what Catalog carries to compile.
	defaults Defaults

	// snap is the bindings in force (nil until the store answered once):
	// what Active reads. See snapshot.go.
	snap      atomic.Pointer[snapshot]
	refreshMu sync.Mutex

	mu    sync.Mutex
	stamp string // what the last poll saw
}

// New creates the component under parent: versions in st, manifests from
// src.
func New(parent deps.Scope, st deps.Dependency[*store.Store], src registry.Source, opts ...Option) *Manager {
	m := &Manager{
		Component: deps.NewComponent(parent, "bindings"),
		store:     st, src: src, author: func(context.Context) string { return DefaultAuthor },
		poll: defaultPoll, changes: newBroadcast(),
	}
	for _, o := range opts {
		o(m)
	}

	m.observe(m.Meter())
	m.Go(m.watch)
	m.Go(m.listen)
	m.Go(m.follow)

	return m
}

// Changes signals after every save, delete, pause and resume — here or on
// another replica (told through PostgreSQL's LISTEN/NOTIFY, and seen by
// the poll at the latest) — until ctx ends; one pending signal at most. Manifests change through the registry's own
// Changes.
func (m *Manager) Changes(ctx context.Context) <-chan struct{} { return m.changes.subscribe(ctx) }

// Catalog is the latest manifests as validation sees them.
func (m *Manager) Catalog() (Catalog, error) {
	cat := m.src.Current()
	if cat.Index == 0 {
		return nil, ErrNotSynced
	}

	return WithDefaults(FromRegistry(cat), m.defaults), nil
}

// Defaults are the installation's defaults of definitions.
func (m *Manager) Defaults() Defaults { return m.defaults }

// watch polls the store for writes of other replicas: what the listener
// missed while its connection was down.
func (m *Manager) watch(ctx context.Context) error {
	tick := time.NewTicker(m.poll)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}

		row, err := m.store.Get().Q.BindingsStamp(ctx)
		if err != nil {
			if ctx.Err() == nil {
				m.Log().Debug("bindings poll failed", xlog.Err(err))
			}

			continue
		}

		stamp := fmt.Sprint(row.Bindings, "/", row.Rules, "/", row.RulesUpdatedAt)

		m.mu.Lock()
		changed := m.stamp != "" && m.stamp != stamp
		m.stamp = stamp
		m.mu.Unlock()

		if changed {
			m.changes.notify()
		}
	}
}

// broadcast signals "something changed" to every subscriber: one pending
// signal each, never a backlog.
type broadcast struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newBroadcast() *broadcast { return &broadcast{subs: map[chan struct{}]struct{}{}} }

func (b *broadcast) subscribe(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)

	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	context.AfterFunc(ctx, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
		close(ch)
	})

	return ch
}

func (b *broadcast) notify() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
