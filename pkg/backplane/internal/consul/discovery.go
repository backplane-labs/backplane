package consul

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// BackplaneService is the service name backplane runs under.
const BackplaneService = "backplane"

// discoveryTTL is how long a list of backplane replicas is reused.
const discoveryTTL = 5 * time.Second

// Discovery finds backplane's replicas: the instance states the SDK of
// each one writes to KV (phase serving, with a platform port), as the
// registry reads them. Lists are cached for a few seconds.
type Discovery struct {
	client *api.Client

	mu      sync.Mutex
	addrs   []string
	fetched time.Time
}

// NewDiscovery creates the discovery over client.
func NewDiscovery(client *api.Client) *Discovery { return &Discovery{client: client} }

// Backplanes is the platform addresses (host:port) of the serving replicas.
func (d *Discovery) Backplanes(ctx context.Context) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if time.Since(d.fetched) < discoveryTTL && d.addrs != nil {
		return append([]string(nil), d.addrs...), nil
	}

	prefix := servicesPrefix + BackplaneService + "/instances/"

	pairs, _, err := d.client.KV().List(prefix, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("consul: list %s: %w", prefix, err)
	}

	addrs := make([]string, 0, len(pairs))

	for _, kv := range pairs {
		var st backplanev1.InstanceState
		if proto.Unmarshal(kv.Value, &st) != nil {
			continue
		}

		if st.GetPhase() != backplanev1.InstancePhase_INSTANCE_PHASE_SERVING || st.GetAddress() == "" ||
			st.GetPlatformPort() == 0 {
			continue
		}

		addrs = append(addrs, net.JoinHostPort(st.GetAddress(), strconv.FormatUint(uint64(st.GetPlatformPort()), 10)))
	}

	d.addrs, d.fetched = addrs, time.Now()

	return append([]string(nil), addrs...), nil
}

// boundPrefix mirrors backplane's internal/bindings.BoundPrefix: the
// published bound hooks of each service.
const boundPrefix = "backplane/bindings/"

// boundRetry bounds the wait after a failed watch query.
const boundRetry = 5 * time.Second

// WatchBound calls fn with service's bound hooks (short names; empty when
// none) now and after every change, until ctx ends. A failed query is
// retried; fn keeps the last set meanwhile.
func (d *Discovery) WatchBound(ctx context.Context, service string, fn func(hooks []string)) error {
	key := boundPrefix + service

	var (
		index uint64
		last  string
		seen  bool
	)

	for ctx.Err() == nil {
		q := (&api.QueryOptions{WaitIndex: index, WaitTime: time.Minute}).WithContext(ctx)

		pair, meta, err := d.client.KV().Get(key, q)
		if err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(boundRetry):
			}

			continue
		}

		// A lower index means the KV store was reset: start over; an index
		// must stay above zero or the query never blocks.
		index = max(meta.LastIndex, 1)
		if meta.LastIndex < index {
			index = 1
		}

		raw := "[]"
		if pair != nil {
			raw = string(pair.Value)
		}

		if seen && raw == last {
			continue
		}

		hooks := []string{}
		if err := json.Unmarshal([]byte(raw), &hooks); err != nil {
			continue
		}

		seen, last = true, raw

		fn(hooks)
	}

	return nil
}
