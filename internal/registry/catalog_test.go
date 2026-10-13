package registry_test

import (
	"context"
	"testing"
	"time"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/internal/registry"
)

func TestLatest(t *testing.T) {
	t.Parallel()

	manifests := map[string]*backplanev1.Manifest{
		"1.9.0": manifest("s", "1.9.0"), "1.10.0": manifest("s", "1.10.0"), "2.0.0": manifest("s", "2.0.0"),
	}

	cases := map[string]struct {
		svc  registry.Service
		want string
	}{
		"no manifests":                    {registry.Service{}, ""},
		"no live instance: highest known": {registry.Service{Manifests: manifests}, "2.0.0"},
		"highest live, semver order": {registry.Service{Manifests: manifests, Instances: []registry.Instance{
			{ID: "a", State: state("s", "a", "1.9.0")}, {ID: "b", State: state("s", "b", "1.10.0")},
		}}, "1.10.0"},
		"live without manifest: fallback": {registry.Service{Manifests: manifests, Instances: []registry.Instance{
			{ID: "a", State: state("s", "a", "3.0.0")}, {ID: "b", Registered: true},
		}}, "2.0.0"},
		"dev builds": {registry.Service{Manifests: map[string]*backplanev1.Manifest{
			"0.0.0+aaa": manifest("s", "0.0.0+aaa"), "0.0.0+bbb": manifest("s", "0.0.0+bbb"),
		}, Instances: []registry.Instance{{ID: "a", State: state("s", "a", "0.0.0+aaa")}}}, "0.0.0+aaa"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := c.svc.Latest().GetVersion(); got != c.want {
				t.Fatalf("latest %q, want %q", got, c.want)
			}
		})
	}
}

func TestAvailableDuringRollingUpdate(t *testing.T) {
	t.Parallel()

	old, next := manifest("s", "1.0.0"), manifest("s", "2.0.0")
	ready := registry.Instance{ID: "old", State: state("s", "old", "1.0.0"), Registered: true, Healthy: true}

	ready.State.Phase = backplanev1.InstancePhase_INSTANCE_PHASE_SERVING
	for _, phase := range []backplanev1.InstancePhase{
		backplanev1.InstancePhase_INSTANCE_PHASE_STARTING,
		backplanev1.InstancePhase_INSTANCE_PHASE_STOPPING,
		backplanev1.InstancePhase_INSTANCE_PHASE_SERVING,
	} {
		for _, registered := range []bool{false, true} {
			for _, healthy := range []bool{false, true} {
				in := registry.Instance{State: state("s", "next", "2.0.0"), Registered: registered, Healthy: healthy}
				in.State.Phase = phase
				svc := registry.Service{Manifests: map[string]*backplanev1.Manifest{"1.0.0": old, "2.0.0": next}, Instances: []registry.Instance{ready, in}}

				want := "1.0.0"
				if registered && healthy && phase == backplanev1.InstancePhase_INSTANCE_PHASE_SERVING {
					want = "2.0.0"
				}

				if svc.Available().GetVersion() != want || svc.Latest().GetVersion() != want {
					t.Fatalf("phase=%s registered=%v healthy=%v: available=%s latest=%s", phase, registered, healthy, svc.Available().GetVersion(), svc.Latest().GetVersion())
				}

				svc.Instances = nil
				if svc.Available() != nil || svc.Latest().GetVersion() != "2.0.0" {
					t.Fatal("offline inspection lost known manifest")
				}
			}
		}
	}
}

func TestAvailableBuildMetadataIsDeterministic(t *testing.T) {
	t.Parallel()

	for _, versions := range [][]string{
		{"0.0.0+dev.202610130001", "0.0.0+dev.202610130002"},
		{"0.0.0+dev.202610130002", "0.0.0+dev.202610130001"},
	} {
		svc := registry.Service{Manifests: map[string]*backplanev1.Manifest{}}
		for _, version := range versions {
			svc.Manifests[version] = manifest("s", version)
			in := registry.Instance{State: state("s", version, version), Registered: true, Healthy: true}
			in.State.Phase = backplanev1.InstancePhase_INSTANCE_PHASE_SERVING
			svc.Instances = append(svc.Instances, in)
		}

		if got := svc.Available().GetVersion(); got != "0.0.0+dev.202610130002" {
			t.Fatalf("available build: %s", got)
		}
	}
}

func TestHub(t *testing.T) {
	t.Parallel()

	h := registry.NewHub()
	if c := h.Current(); c.Index != 0 || c.Services != nil {
		t.Fatalf("zero hub %+v", c)
	}

	ctx, cancel := context.WithCancel(t.Context())
	changes := h.Changes(ctx)

	svc := func(v string) map[string]registry.Service {
		return map[string]registry.Service{"s": {
			Name: "s", Manifests: map[string]*backplanev1.Manifest{v: manifest("s", v)},
		}}
	}

	c1, ok := h.Publish(svc("1.0.0"))
	if !ok || c1.Index != 1 {
		t.Fatalf("first publish %d %v", c1.Index, ok)
	}

	// Equal content (another message, same fields) is not a new snapshot.
	if c, again := h.Publish(svc("1.0.0")); again || c.Index != 1 {
		t.Fatalf("equal publish %d %v", c.Index, again)
	}

	if c, changed := h.Publish(svc("1.1.0")); !changed || c.Index != 2 {
		t.Fatalf("second publish %d %v", c.Index, changed)
	}

	// Two snapshots, one pending signal.
	<-changes

	select {
	case <-changes:
		t.Fatal("signals piled up")
	default:
	}

	cancel()

	select {
	case _, open := <-changes:
		if open {
			t.Fatal("signal after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("changes not closed with its context")
	}
}
