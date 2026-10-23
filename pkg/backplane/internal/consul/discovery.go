package consul

import (
	"bytes"
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
	return d.watch(ctx, boundPrefix+service, func(raw []byte) {
		if raw == nil {
			raw = []byte("[]")
		}

		hooks := []string{}
		if json.Unmarshal(raw, &hooks) == nil {
			fn(hooks)
		}
	})
}

// sectionsPrefix mirrors backplane's internal/sections.Prefix: the
// published sections by name.
const sectionsPrefix = "backplane/sections/"

// WatchSection calls fn with section name's published set (nil when there
// is none) now and after every change, until ctx ends.
func (d *Discovery) WatchSection(ctx context.Context, name string, fn func(raw []byte)) error {
	return d.watch(ctx, sectionsPrefix+name, fn)
}

// watch calls fn with key's value (nil when absent) now and after every
// change, until ctx ends. A failed query is retried after boundRetry.
func (d *Discovery) watch(ctx context.Context, key string, fn func(raw []byte)) error {
	var (
		index uint64
		last  []byte
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
		if meta.LastIndex < index {
			index = 1
		} else {
			index = max(meta.LastIndex, 1)
		}

		var raw []byte
		if pair != nil {
			raw = pair.Value
		}

		if seen && bytes.Equal(raw, last) && (raw == nil) == (last == nil) {
			continue
		}

		seen, last = true, raw

		fn(raw)
	}

	return nil
}
