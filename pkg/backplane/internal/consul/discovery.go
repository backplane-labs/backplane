package consul

import (
	"context"
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
