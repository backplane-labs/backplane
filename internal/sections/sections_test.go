package sections_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/consul/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/sections"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
)

func section(t *testing.T, version, value string) *backplanev1.Manifest {
	t.Helper()

	v, err := anypb.New(wrapperspb.String(value))
	if err != nil {
		t.Fatal(err)
	}

	return &backplanev1.Manifest{Version: version, Sections: map[string]*anypb.Any{"komeet.module": v}}
}

func serving(version string, healthy bool) registry.Instance {
	return registry.Instance{
		ID: version, Registered: true, Healthy: healthy,
		State: &backplanev1.InstanceState{Version: version, Phase: backplanev1.InstancePhase_INSTANCE_PHASE_SERVING},
	}
}

// A name's set is each serving service's highest serving version's
// section; a service with no healthy serving instance is not in it.
func TestSets(t *testing.T) {
	t.Parallel()

	cat := registry.Catalog{Services: map[string]registry.Service{
		"media": {
			Name:      "media",
			Manifests: map[string]*backplanev1.Manifest{"1.0.0": section(t, "1.0.0", "old"), "1.1.0": section(t, "1.1.0", "new")},
			Instances: []registry.Instance{serving("1.0.0", true), serving("1.1.0", true)},
		},
		"down": {
			Name:      "down",
			Manifests: map[string]*backplanev1.Manifest{"1.0.0": section(t, "1.0.0", "x")},
			Instances: []registry.Instance{serving("1.0.0", false)},
		},
		"plain": {
			Name:      "plain",
			Manifests: map[string]*backplanev1.Manifest{"1.0.0": {Version: "1.0.0"}},
			Instances: []registry.Instance{serving("1.0.0", true)},
		},
	}}

	sets := sections.Sets(cat)

	set := sets["komeet.module"]
	if len(sets) != 1 || len(set.GetServices()) != 1 {
		t.Fatalf("sets %v", sets)
	}

	var value wrapperspb.StringValue
	if err := set.GetServices()["media"].UnmarshalTo(&value); err != nil || value.GetValue() != "new" {
		t.Fatalf("media %v %v", value.GetValue(), err)
	}
}

// The publisher writes a name's set and removes the key when no serving
// service offers it any more.
func TestPublisher(t *testing.T) {
	t.Parallel()

	addr := os.Getenv("BACKPLANE_TEST_CONSUL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_CONSUL not set")
	}

	client, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}

	name := "test." + strings.ReplaceAll(uuid.NewString(), "-", "")
	key := sections.Prefix + name

	t.Cleanup(func() { _, _ = client.KV().Delete(key, nil) })

	v, err := anypb.New(wrapperspb.String("m"))
	if err != nil {
		t.Fatal(err)
	}

	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{"media": {
		Name:      "media",
		Manifests: map[string]*backplanev1.Manifest{"1.0.0": {Version: "1.0.0", Sections: map[string]*anypb.Any{name: v}}},
		Instances: []registry.Instance{serving("1.0.0", true)},
	}})

	h := backplanetest.New(t)
	sections.NewPublisher(h.Root(), hub, client)
	h.Start()

	eventually(t, "published", func() bool {
		pair, _, err := client.KV().Get(key, nil)
		if err != nil || pair == nil {
			return false
		}

		var set backplanev1.SectionSet

		return proto.Unmarshal(pair.Value, &set) == nil && set.GetServices()["media"] != nil
	})

	hub.Publish(map[string]registry.Service{})

	eventually(t, "removed", func() bool {
		pair, _, err := client.KV().Get(key, nil)

		return err == nil && pair == nil
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("not reached: %s", what)
		}
	}
}
