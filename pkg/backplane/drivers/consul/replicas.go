package consul

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	internal "github.com/backplane-labs/backplane/pkg/backplane/internal/consul"
)

const (
	replicaWait         = time.Minute
	replicaQueryTimeout = 75 * time.Second
	replicaRetry        = time.Second
)

func newReplicaWatch(cfg config.Consul) (backplane.ReplicaWatch, error) {
	client, err := newClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("replica watch: %w", err)
	}

	return replicaWatch(client.Health()), nil
}

func replicaWatch(health *api.Health) backplane.ReplicaWatch {
	return func(ctx context.Context, service, listener string, onChange func([]backplane.Replica)) error {
		var (
			index uint64
			last  []backplane.Replica
		)

		for ctx.Err() == nil {
			query, cancel := context.WithTimeout(ctx, replicaQueryTimeout)
			entries, meta, err := health.Service(service, "", true,
				(&api.QueryOptions{WaitIndex: index, WaitTime: replicaWait}).WithContext(query))

			cancel()

			if err != nil {
				if terminalReplicaLookup(err) {
					return fmt.Errorf("consul: watch replicas of %q: %w", service, err)
				}

				select {
				case <-ctx.Done():
				case <-time.After(replicaRetry):
				}

				continue
			}

			if meta.LastIndex < index {
				index = 0
			} else {
				index = max(meta.LastIndex, 1)
			}

			next := healthyReplicas(entries, listener)
			if last == nil || !slices.Equal(last, next) {
				last = next
				onChange(slices.Clone(next))
			}
		}

		return nil
	}
}

func terminalReplicaLookup(err error) bool {
	var status api.StatusError
	if !errors.As(err, &status) {
		return false
	}

	switch status.Code {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return true
	default:
		return false
	}
}

func healthyReplicas(entries []*api.ServiceEntry, listener string) []backplane.Replica {
	out := make([]backplane.Replica, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.Service == nil || entry.Service.ID == "" {
			continue
		}

		address, port := entry.Service.Address, entry.Service.Port
		if listener != "" {
			tag := entry.Service.TaggedAddresses[internal.ListenerAddress(listener)]
			address, port = tag.Address, tag.Port
		} else if address == "" && entry.Node != nil {
			address = entry.Node.Address
		}

		if address != "" && port > 0 {
			out = append(out, backplane.Replica{ID: entry.Service.ID, Address: net.JoinHostPort(address, strconv.Itoa(port))})
		}
	}

	slices.SortFunc(out, func(a, b backplane.Replica) int {
		return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Address, b.Address))
	})

	return slices.Compact(out)
}
