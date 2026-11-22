package consul_test

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/consul"
)

type replicaConfig struct {
	config.Backplane `json:"backplane"`
}

func TestWatchReplicasWithoutDiscovery(t *testing.T) {
	t.Parallel()

	var root backplane.Root[replicaConfig]
	if err := root.WatchReplicas(t.Context(), "sync", "", func([]backplane.Replica) {
		t.Error("unexpected snapshot")
	}); !errors.Is(err, backplane.ErrNoReplicaDiscovery) {
		t.Fatalf("WatchReplicas: %v", err)
	}
}

func TestWatchReplicas(t *testing.T) {
	const token = "test-acl"
	const entries = `[{"Node":{"Address":"127.0.0.1"},"Service":{"ID":"b","Port":9090}},` +
		`{"Service":{"ID":"a","Address":"127.0.0.2","Port":9091,` +
		`"TaggedAddresses":{"listener-admin":{"Address":"::1","Port":9092}}}}]`

	var requests atomic.Int32

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Consul-Token") != token {
			w.WriteHeader(http.StatusForbidden)

			return
		}

		if r.URL.Path == "/v1/health/service/denied" {
			w.WriteHeader(http.StatusForbidden)

			return
		}

		if r.URL.Path != "/v1/health/service/sync" || r.URL.Query().Get("passing") != "1" {
			t.Errorf("unexpected lookup: %s", r.URL)
		}

		switch requests.Add(1) {
		case 1:
			w.Header().Set("X-Consul-Index", "10")
			_, _ = w.Write([]byte(entries))
		case 2:
			if got := r.URL.Query().Get("index"); got != "10" {
				t.Errorf("blocking lookup index: %q", got)
			}

			w.WriteHeader(http.StatusServiceUnavailable)
		case 3:
			w.Header().Set("X-Consul-Index", "11")
			_, _ = w.Write([]byte(entries))
		default:
			w.Header().Set("X-Consul-Index", "12")
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	t.Setenv("BACKPLANE_CONSUL_ADDR", strings.TrimPrefix(srv.URL, "https://"))
	t.Setenv("BACKPLANE_CONSUL_TOKEN", token)
	t.Setenv("BACKPLANE_CONSUL_TLS_ENABLED", "true")
	t.Setenv("BACKPLANE_CONSUL_TLS_CA", string(pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: srv.Certificate().Raw,
	})))

	var root backplane.Root[replicaConfig]

	svc, err := backplane.Open(t.Context(), func(r backplane.Root[replicaConfig]) (*struct{}, error) {
		root = r

		return &struct{}{}, nil
	}, backplane.Name("watch-test"), consul.Driver(), backplane.ConfigOptions(config.WithoutFile(), config.WithoutConsul()))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	var snapshots [][]backplane.Replica

	if err := root.WatchReplicas(ctx, "sync", "admin", func(replicas []backplane.Replica) {
		snapshots = append(snapshots, slices.Clone(replicas))
		if len(replicas) == 0 {
			cancel()
		} else {
			replicas[0].ID = "mutated by caller"
		}
	}); err != nil {
		t.Fatal(err)
	}

	if len(snapshots) != 2 || !slices.Equal(snapshots[0], []backplane.Replica{{ID: "a", Address: "[::1]:9092"}}) ||
		len(snapshots[1]) != 0 || requests.Load() != 4 {
		t.Fatalf("snapshots = %v; requests = %d", snapshots, requests.Load())
	}

	denied, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()

	err = root.WatchReplicas(denied, "denied", "", func([]backplane.Replica) {
		t.Error("snapshot after ACL refusal")
	})

	var status api.StatusError
	if !errors.As(err, &status) || status.Code != http.StatusForbidden {
		t.Fatalf("want ACL refusal, got %v", err)
	}
}
