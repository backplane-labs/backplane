// Package config is backplane's side of Live configuration (§5.2, §5.3):
// overrides of the Live fields of a service kept as revisions in
// PostgreSQL — the source of truth — and delivered to Consul KV
// config/<service>/, which is only the channel the SDK reads.
//
// An override maps Live paths of the service's manifest (config.live,
// "greeter.suffix") to JSON values; a Live field is replaced whole, and
// only Live paths may be set. Saving validates the override on the
// effective configuration of every live instance by its schema (schemapb,
// CEL included), stores the next revision and writes KV in one Consul
// transaction with config/<service>/_revision. The reconciler keeps KV
// equal to PostgreSQL: at start, on a timer and on every change under
// config/, it rewrites what differs — a lost Consul, a replica that lagged,
// a manual edit in the Consul UI.
//
// Manager is the component: the reconciler runs under its node; its API
// serves backplane.console.v1.ConfigService.
package config

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/grpc/metadata"

	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Settings of the configuration component: BACKPLANE_LIVE_CONFIG_*.
type Settings struct {
	// How often the reconciler compares KV with PostgreSQL besides at
	// start and on KV changes.
	ReconcileInterval time.Duration `json:"reconcile_interval" schemapb:"default=30s"`
}

// ErrInterval: the reconcile interval is not positive.
var ErrInterval = errors.New("live_config.reconcile_interval must be positive")

// Validate checks what the schema does not.
func (s Settings) Validate() error {
	if s.ReconcileInterval <= 0 {
		return ErrInterval
	}

	return nil
}

// SessionMetadata is the gRPC metadata the console puts the operator's
// session in (§11.1).
const SessionMetadata = "bp-console-session"

// DefaultAuthor is the author of a revision saved outside a console
// session: v0 has one operator (§11.3).
const DefaultAuthor = "admin"

// Option configures New.
type Option func(*Manager)

// Author names the author of a revision from the request's context;
// default: the console session in SessionMetadata, else DefaultAuthor.
func Author(fn func(ctx context.Context) string) Option { return func(m *Manager) { m.author = fn } }

// WaitTime bounds one blocking query on config/ (default 5m).
func WaitTime(d time.Duration) Option { return func(m *Manager) { m.wait = d } }

// Manager keeps overrides: revisions in PostgreSQL, delivery to Consul KV,
// the reconciler and the console API. It holds state and goroutines: share
// it by pointer.
type Manager struct {
	deps.Component

	settings Settings
	consul   *api.Client
	store    deps.Dependency[*store.Store]
	src      registry.Source
	author   func(ctx context.Context) string
	wait     time.Duration
	engines  engines
	metrics  metrics
	changes  *broadcast
	kick     chan struct{}

	mu      sync.Mutex
	failing error // the reconciler's last error while it keeps failing
}

const defaultWait = 5 * time.Minute

// New creates the component under parent: revisions in st, delivery
// through client, instances and manifests from src.
func New(
	parent deps.Scope, s Settings, client *api.Client, st deps.Dependency[*store.Store], src registry.Source,
	opts ...Option,
) *Manager {
	m := &Manager{
		Component: deps.NewComponent(parent, "config"),
		settings:  s, consul: client, store: st, src: src,
		author: sessionAuthor, wait: defaultWait,
		changes: newBroadcast(), kick: make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(m)
	}

	m.metrics = newMetrics(m.Meter())

	m.Go(m.reconcileLoop)
	m.Go(m.watchKV)

	return m
}

// sessionAuthor: the console session of the request, else DefaultAuthor.
func sessionAuthor(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(SessionMetadata); len(v) > 0 && v[0] != "" {
			return "console:" + v[0]
		}
	}

	return DefaultAuthor
}

// Kick asks the reconciler for a pass now.
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Err is the reconciler's last error while passes keep failing; nil after
// a pass succeeds.
func (m *Manager) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.failing
}
