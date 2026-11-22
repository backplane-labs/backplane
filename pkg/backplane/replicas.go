package backplane

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// Replica is a healthy instance at the requested listener.
type Replica struct {
	ID      string
	Address string // host:port
}

// ReplicaWatch follows healthy instances of a service. An empty listener
// selects the registered primary port.
type ReplicaWatch func(ctx context.Context, service, listener string, onChange func([]Replica)) error

// ReplicaWatchFactory builds a watch from the service's discovery configuration.
type ReplicaWatchFactory func(config.Consul) (ReplicaWatch, error)

// WithReplicaWatch installs the catalog driver for WatchReplicas.
func WithReplicaWatch(factory ReplicaWatchFactory) Option {
	return func(o *options) { o.replicas = factory }
}

// ErrNoReplicaDiscovery means no catalog driver is configured for this service.
var ErrNoReplicaDiscovery = errors.New("backplane: replica discovery is not configured")

// WatchReplicas reports the first successful snapshot (including an empty
// one) and subsequent changes, until ctx is canceled. Transient lookup
// failures leave the last successful snapshot in effect. Call it from a
// component's Go function to tie it to the component's lifetime.
func (r Root[C]) WatchReplicas(ctx context.Context, service, listener string, onChange func([]Replica)) error {
	if r.replicas == nil {
		return ErrNoReplicaDiscovery
	}

	watch, err := r.replicas()
	if err != nil {
		return fmt.Errorf("backplane: replica discovery: %w", err)
	}

	return watch(ctx, service, listener, onChange)
}

func (o options) replicaDiscovery(cfg config.Consul) func() (ReplicaWatch, error) {
	if o.replicas == nil || !cfg.Enabled() || o.manifest {
		return nil
	}

	return sync.OnceValues(func() (ReplicaWatch, error) { return o.replicas(cfg) })
}
