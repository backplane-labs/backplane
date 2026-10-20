package registry_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

const waitFor = 5 * time.Second

func manifest(svc, version string) *backplanev1.Manifest {
	return &backplanev1.Manifest{Service: svc, Version: version}
}

func state(svc, id, version string) *backplanev1.InstanceState {
	return &backplanev1.InstanceState{
		Id: id, Service: svc, Version: version, Phase: backplanev1.InstancePhase_INSTANCE_PHASE_SERVING,
	}
}

// start runs a registry against client with fast queries.
func start(t *testing.T, client *api.Client) *registry.Registry {
	t.Helper()

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	r := registry.New(h.Root(), client,
		registry.WaitTime(200*time.Millisecond), registry.Backoff(10*time.Millisecond, 50*time.Millisecond))
	h.Start()

	return r
}

func synced(t *testing.T, r *registry.Registry) registry.Catalog {
	t.Helper()

	select {
	case <-r.Synced():
	case <-time.After(waitFor):
		t.Fatalf("not synced: %v", r.Ready())
	}

	return r.Current()
}

// until waits for a snapshot cond accepts, checking after every change.
func until(t *testing.T, r *registry.Registry, what string, cond func(c registry.Catalog) bool) registry.Catalog {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), waitFor)
	defer cancel()

	changes := r.Changes(ctx)

	for {
		if c := r.Current(); cond(c) {
			return c
		}

		select {
		case <-changes:
		case <-ctx.Done():
			t.Fatalf("%s: last snapshot %+v", what, r.Current())
		}
	}
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	f := newFakeConsul(t)
	f.put(t, "backplane/services/hello/manifests/1.0.0", manifest("hello", "1.0.0"))
	f.put(t, "backplane/services/hello/manifests/1.1.0", manifest("hello", "1.1.0"))
	f.put(t, "backplane/services/hello/instances/hello-a", state("hello", "hello-a", "1.0.0"))
	f.put(t, "backplane/services/hello/instances/hello-b", state("hello", "hello-b", "1.1.0"))
	f.putRaw("backplane/services/hello/instances/broken", []byte{0xff, 0xff})
	f.putRaw("backplane/services/hello/other/x", []byte("ignored"))
	f.register("hello",
		entry("hello", "hello-a", "10.0.0.5", 8080, api.HealthPassing, "blue"),
		entry("hello", "hello-b", "", 8080, api.HealthCritical),
		entry("hello", "hello-c", "10.0.0.7", 8080, api.HealthPassing))
	f.register("legacy", entry("legacy", "legacy-1", "10.0.0.9", 9000, api.HealthPassing))

	r := start(t, f.client(t))
	c := synced(t, r)

	if r.Ready() != nil || c.Index != 1 {
		t.Fatalf("ready %v index %d", r.Ready(), c.Index)
	}

	if _, ok := c.Services["consul"]; ok || len(c.Services) != 2 {
		t.Fatalf("services %v", c.Services)
	}

	hello := c.Services["hello"]
	if len(hello.Manifests) != 2 || hello.Manifest("1.1.0").GetVersion() != "1.1.0" {
		t.Fatalf("manifests %v", hello.Manifests)
	}

	if got := hello.Latest().GetVersion(); got != "1.0.0" {
		t.Fatalf("latest %q", got)
	}

	want := []registry.Instance{
		{ID: "hello-a", Registered: true, Healthy: true, Address: "10.0.0.5", Port: 8080, Tags: []string{"blue"}},
		{ID: "hello-b", Registered: true, Address: "10.0.0.1", Port: 8080},
		{ID: "hello-c", Registered: true, Healthy: true, Address: "10.0.0.7", Port: 8080},
	}
	if len(hello.Instances) != len(want) {
		t.Fatalf("instances %+v", hello.Instances)
	}

	for i, w := range want {
		got := hello.Instances[i]
		if got.ID != w.ID || got.Registered != w.Registered || got.Healthy != w.Healthy ||
			got.Address != w.Address || got.Port != w.Port || len(got.Tags) != len(w.Tags) {
			t.Fatalf("instance %d: %+v, want %+v", i, got, w)
		}
	}

	if hello.Instances[0].State.GetVersion() != "1.0.0" || hello.Instances[2].State != nil {
		t.Fatalf("states %+v", hello.Instances)
	}

	if healthy := hello.Healthy(); len(healthy) != 2 || healthy[1].ID != "hello-c" {
		t.Fatalf("healthy %+v", healthy)
	}

	if legacy := c.Services["legacy"]; legacy.Latest() != nil || len(legacy.Instances) != 1 {
		t.Fatalf("legacy %+v", legacy)
	}
}

func TestChanges(t *testing.T) {
	t.Parallel()

	f := newFakeConsul(t)
	f.put(t, "backplane/services/hello/manifests/1.0.0", manifest("hello", "1.0.0"))
	f.put(t, "backplane/services/hello/instances/hello-a", state("hello", "hello-a", "1.0.0"))

	r := start(t, f.client(t))
	first := synced(t, r)

	// A new instance registers.
	f.register("hello", entry("hello", "hello-a", "10.0.0.5", 8080, api.HealthPassing))

	c := until(t, r, "registered", func(c registry.Catalog) bool {
		return len(c.Services["hello"].Healthy()) == 1
	})

	if c.Index <= first.Index {
		t.Fatalf("index %d after %d", c.Index, first.Index)
	}

	// A new service appears in KV; its health is followed.
	f.put(t, "backplane/services/users/instances/users-1", state("users", "users-1", "2.0.0"))
	f.register("users", entry("users", "users-1", "10.0.0.8", 8080, api.HealthPassing))
	until(t, r, "users", func(c registry.Catalog) bool { return len(c.Services["users"].Healthy()) == 1 })

	// Its keys go and its registration goes: the service is gone.
	f.del("backplane/services/users/instances/users-1")
	f.register("users")
	until(t, r, "users gone", func(c registry.Catalog) bool {
		_, ok := c.Services["users"]

		return !ok
	})

	// An unchanged query result publishes nothing. KV and the catalog are
	// followed separately, so a result of either may still be in flight:
	// settle first.
	before := settled(t, r)

	f.change(func() {})
	time.Sleep(300 * time.Millisecond)

	if after := r.Current(); after.Index != before.Index {
		t.Fatalf("index moved without a change: %d -> %d\nbefore %s\nafter  %s", before.Index, after.Index, dump(before), dump(after))
	}
}

// settled is the snapshot once its index has not moved for 300ms.
func settled(t *testing.T, r *registry.Registry) registry.Catalog {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for {
		before := r.Current()

		time.Sleep(300 * time.Millisecond)

		if r.Current().Index == before.Index {
			return before
		}

		if time.Now().After(deadline) {
			t.Fatal("the registry never settled")
		}
	}
}

// dump is a snapshot's content, one line per service, for a failure.
func dump(c registry.Catalog) string {
	var b strings.Builder

	for _, name := range slices.Sorted(maps.Keys(c.Services)) {
		svc := c.Services[name]
		fmt.Fprintf(&b, "%s manifests=%d instances=", name, len(svc.Manifests))

		for _, in := range svc.Instances {
			fmt.Fprintf(&b, "[%s reg=%t ok=%t %s:%d state=%t] ", in.ID, in.Registered, in.Healthy, in.Address, in.Port, in.State != nil)
		}

		b.WriteString("; ")
	}

	return b.String()
}

func TestConsulDown(t *testing.T) {
	t.Parallel()

	f := newFakeConsul(t)
	f.put(t, "backplane/services/hello/manifests/1.0.0", manifest("hello", "1.0.0"))

	r := start(t, f.client(t))
	c := synced(t, r)

	f.setDown(true)

	deadline := time.Now().Add(waitFor)
	for r.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("outage not noticed")
		}

		time.Sleep(10 * time.Millisecond)
	}

	if r.Ready() != nil || r.Current().Index != c.Index || len(r.Current().Services) != 1 {
		t.Fatalf("outage lost the snapshot: ready %v %+v", r.Ready(), r.Current())
	}

	f.put(t, "backplane/services/hello/manifests/1.1.0", manifest("hello", "1.1.0"))
	f.setDown(false)
	until(t, r, "recovered", func(c registry.Catalog) bool { return c.Services["hello"].Manifest("1.1.0") != nil })

	if r.Err() != nil {
		t.Fatalf("err after recovery: %v", r.Err())
	}
}

func TestNotReadyWithoutConsul(t *testing.T) {
	t.Parallel()

	f := newFakeConsul(t)
	f.setDown(true)

	r := start(t, f.client(t))

	deadline := time.Now().Add(waitFor)
	for r.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("outage not noticed")
		}

		time.Sleep(10 * time.Millisecond)
	}

	if err := r.Ready(); !errors.Is(err, registry.ErrNotSynced) || r.Current().Index != 0 {
		t.Fatalf("ready %v index %d", err, r.Current().Index)
	}

	f.setDown(false)
	synced(t, r)
}
