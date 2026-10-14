package registry_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"

	"github.com/backplane-labs/backplane/internal/registry"
)

// liveConsul is the Consul of platform-in-a-box (BACKPLANE_TEST_CONSUL,
// e.g. localhost:8500); the test is skipped without it.
func liveConsul(t *testing.T) *api.Client {
	t.Helper()

	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	c, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestLiveConsul(t *testing.T) {
	t.Parallel()

	c := liveConsul(t)
	name := fmt.Sprintf("regtest-%d", time.Now().UnixNano())
	id := name + "-1"
	prefix := registry.Prefix + name + "/"

	t.Cleanup(func() {
		_, _ = c.KV().DeleteTree(prefix, nil)
		_ = c.Agent().ServiceDeregister(id)
	})

	r := start(t, c)
	synced(t, r)

	ctx, cancel := context.WithTimeout(t.Context(), waitFor)
	defer cancel()

	changes := r.Changes(ctx)

	put := func(key string, m proto.Message) {
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := c.KV().Put(&api.KVPair{Key: prefix + key, Value: b}, nil); err != nil {
			t.Fatal(err)
		}
	}

	put("manifests/1.0.0", manifest(name, "1.0.0"))
	put("instances/"+id, state(name, id, "1.0.0"))

	select {
	case <-changes:
	case <-ctx.Done():
		t.Fatal("no change notification")
	}

	until(t, r, "kv", func(cat registry.Catalog) bool {
		s := cat.Services[name]

		return s.Latest().GetVersion() == "1.0.0" && len(s.Instances) == 1 && s.Instances[0].State != nil
	})

	err := c.Agent().ServiceRegister(&api.AgentServiceRegistration{
		ID: id, Name: name, Address: "10.1.2.3", Port: 8080,
		Check: &api.AgentServiceCheck{TTL: "1m", Status: api.HealthPassing},
	})
	if err != nil {
		t.Fatal(err)
	}

	cat := until(t, r, "registered", func(cat registry.Catalog) bool { return len(cat.Services[name].Healthy()) == 1 })
	if in := cat.Services[name].Instances[0]; in.Address != "10.1.2.3" || in.Port != 8080 || in.ID != id {
		t.Fatalf("instance %+v", in)
	}

	if err := c.Agent().ServiceDeregister(id); err != nil {
		t.Fatal(err)
	}

	if _, err := c.KV().DeleteTree(prefix, nil); err != nil {
		t.Fatal(err)
	}

	until(t, r, "gone", func(cat registry.Catalog) bool {
		_, ok := cat.Services[name]

		return !ok
	})
}
