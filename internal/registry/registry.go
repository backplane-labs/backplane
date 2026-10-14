package registry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	cenkalti "github.com/cenkalti/backoff/v5"
	"github.com/hashicorp/consul/api"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// ErrNotSynced: the first snapshot is not built yet.
var ErrNotSynced = errors.New("registry: not synced with consul yet")

const (
	defaultWait     = 5 * time.Minute
	defaultMinDelay = 500 * time.Millisecond
	defaultMaxDelay = 30 * time.Second
	jitter          = 0.2
	multiplier      = 2
)

// Option configures New.
type Option func(*Registry)

// WaitTime bounds one blocking query (default 5m; Consul caps it at 10m).
func WaitTime(d time.Duration) Option { return func(r *Registry) { r.wait = d } }

// Backoff bounds the delay between failed queries (default 500ms..30s,
// jittered).
func Backoff(minDelay, maxDelay time.Duration) Option {
	return func(r *Registry) { r.minDelay, r.maxDelay = minDelay, maxDelay }
}

// Registry follows Consul with blocking queries — the catalog's service
// list, the health of every followed service and the KV prefix
// backplane/services/ — and publishes a Catalog snapshot after every
// change. The first snapshot is published once all of them answered
// (Synced); until then Current is the zero Catalog. While Consul is down
// the last snapshot stays and every query retries with backoff.
//
// It is a component: the queries run under its node, from start to stop.
type Registry struct {
	deps.Component

	hub *Hub

	client   *api.Client
	wait     time.Duration
	minDelay time.Duration
	maxDelay time.Duration

	synced chan struct{}

	mu       sync.Mutex
	catalog  map[string][]string // nil until loaded
	kv       map[string]*kvService
	kvLoaded bool
	cache    map[string]parsed
	bad      map[string]uint64              // undecodable keys, by ModifyIndex: warned once
	health   map[string][]*api.ServiceEntry // loaded services only
	watchers map[string]context.CancelFunc  // health, by service
	spawn    func(name string)              // starts a health watcher; set by run
	isSynced bool
	failure  error // the last query error while Consul is failing
}

var _ Source = (*Registry)(nil)

// New creates the registry under parent, reading Consul through client.
func New(parent deps.Scope, client *api.Client, opts ...Option) *Registry {
	r := &Registry{
		Component: deps.NewComponent(parent, "registry"),
		hub:       NewHub(),
		client:    client,
		wait:      defaultWait, minDelay: defaultMinDelay, maxDelay: defaultMaxDelay,
		synced:   make(chan struct{}),
		cache:    map[string]parsed{},
		bad:      map[string]uint64{},
		health:   map[string][]*api.ServiceEntry{},
		watchers: map[string]context.CancelFunc{},
	}
	for _, o := range opts {
		o(r)
	}

	r.Go(r.run)

	return r
}

// Current is the latest snapshot; the zero Catalog before Synced. It is
// shared: never modify it.
func (r *Registry) Current() Catalog { return r.hub.Current() }

// Changes signals every new snapshot until ctx ends (see Hub.Changes).
func (r *Registry) Changes(ctx context.Context) <-chan struct{} { return r.hub.Changes(ctx) }

// Synced is closed once the first snapshot is published.
func (r *Registry) Synced() <-chan struct{} { return r.synced }

// Ready is nil once the first snapshot is published: backplane serves
// nothing useful without it. A later Consul outage keeps it nil — the last
// snapshot stays valid — and shows in Err.
func (r *Registry) Ready() error {
	select {
	case <-r.synced:
		return nil
	default:
	}

	if err := r.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrNotSynced, err)
	}

	return ErrNotSynced
}

// Err is the last Consul error while queries are failing, nil otherwise.
func (r *Registry) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.failure
}

// run starts the catalog and KV watches; health watches come and go with
// the followed services. It returns when the node stops.
func (r *Registry) run(ctx context.Context) error {
	var watches sync.WaitGroup

	r.mu.Lock()
	// Called by rebuild from a running watch, so watches is never zero
	// here. The cancel is kept in r.watchers and called when the service
	// is dropped; ctx ends the rest.
	r.spawn = func(name string) {
		wctx, cancel := context.WithCancel(ctx) //nolint:gosec // cancelled through r.watchers or ctx
		r.watchers[name] = cancel

		watches.Go(func() { r.watch(wctx, "health "+name, r.healthQuery(name)) })
	}
	r.mu.Unlock()

	watches.Go(func() { r.watch(ctx, "catalog", r.catalogQuery) })
	watches.Go(func() { r.watch(ctx, "kv", r.kvQuery) })

	<-ctx.Done()

	watches.Wait()

	return nil
}

// query runs one blocking query at index and applies its result; it
// returns the index to wait on next.
type query func(ctx context.Context, index uint64) (uint64, error)

// watch runs q until ctx ends: blocking on the last index, backing off
// after errors.
func (r *Registry) watch(ctx context.Context, what string, run query) {
	b := &cenkalti.ExponentialBackOff{
		InitialInterval: r.minDelay, RandomizationFactor: jitter, Multiplier: multiplier, MaxInterval: r.maxDelay,
	}
	b.Reset()

	var index uint64

	for ctx.Err() == nil {
		next, err := run(ctx, index)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			r.failed(what, err)

			select {
			case <-ctx.Done():
				return
			case <-time.After(b.NextBackOff()):
			}

			continue
		}

		b.Reset()
		r.recovered()

		// A lower index means Consul's index was reset (a snapshot
		// restore): start over. 0 would not block.
		switch {
		case next < index, next == 0:
			index = 0
		default:
			index = next
		}
	}
}

func (r *Registry) options(ctx context.Context, index uint64) *api.QueryOptions {
	return (&api.QueryOptions{WaitIndex: index, WaitTime: r.wait}).WithContext(ctx)
}

func (r *Registry) catalogQuery(ctx context.Context, index uint64) (uint64, error) {
	services, meta, err := r.client.Catalog().Services(r.options(ctx, index))
	if err != nil {
		return 0, fmt.Errorf("catalog services: %w", err)
	}

	if services == nil {
		services = map[string][]string{}
	}

	r.mu.Lock()
	r.catalog = services
	r.rebuild()
	r.mu.Unlock()

	return meta.LastIndex, nil
}

func (r *Registry) kvQuery(ctx context.Context, index uint64) (uint64, error) {
	pairs, meta, err := r.client.KV().List(Prefix, r.options(ctx, index))
	if err != nil {
		return 0, fmt.Errorf("kv %s: %w", Prefix, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	kv, cache, bad := parseKV(pairs, r.cache)
	for key, e := range bad {
		if r.bad[key] != e.modify {
			r.Log().Warn("undecodable value in consul kv: skipped", xlog.String("key", key), xlog.Err(e.err))
		}
	}

	r.bad = make(map[string]uint64, len(bad))
	for key, e := range bad {
		r.bad[key] = e.modify
	}

	r.kv, r.cache, r.kvLoaded = kv, cache, true
	r.rebuild()

	return meta.LastIndex, nil
}

func (r *Registry) healthQuery(name string) query {
	return func(ctx context.Context, index uint64) (uint64, error) {
		entries, meta, err := r.client.Health().Service(name, "", false, r.options(ctx, index))
		if err != nil {
			return 0, fmt.Errorf("health %s: %w", name, err)
		}

		r.mu.Lock()
		defer r.mu.Unlock()

		// The service may have been dropped while the query ran.
		if _, followed := r.watchers[name]; followed && ctx.Err() == nil {
			r.health[name] = entries
			r.rebuild()
		}

		return meta.LastIndex, nil
	}
}

// rebuild reconciles the health watches with the followed services and
// publishes a snapshot once everything answered; r.mu is held.
func (r *Registry) rebuild() {
	if r.catalog == nil || !r.kvLoaded {
		return
	}

	names := serviceNames(r.kv, r.catalog)

	for name, cancel := range r.watchers {
		if !slices.Contains(names, name) {
			cancel()
			delete(r.watchers, name)
			delete(r.health, name)
		}
	}

	for _, name := range names {
		if _, ok := r.watchers[name]; !ok && r.spawn != nil {
			r.spawn(name)
		}
	}

	if !r.isSynced {
		for _, name := range names {
			if _, ok := r.health[name]; !ok {
				return
			}
		}
	}

	prev := r.Current()

	snap, changed := r.hub.Publish(build(names, r.kv, r.health))
	if !changed {
		return
	}

	if !r.isSynced {
		r.isSynced = true
		close(r.synced)
		r.Log().Info("registry synced", xlog.Int("services", len(snap.Services)), xlog.Uint64("index", snap.Index))
	}

	logDiff(r.Log(), prev, snap)
}

// failed records a query error; the first one of an outage is logged.
func (r *Registry) failed(what string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.failure == nil {
		r.Log().Warn("consul unreachable: keeping the last snapshot", xlog.String("query", what), xlog.Err(err))
	}

	r.failure = err
}

// recovered clears the outage; its end is logged.
func (r *Registry) recovered() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.failure != nil {
		r.Log().Info("consul reachable again")
		r.failure = nil
	}
}
