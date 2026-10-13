// Package registry is backplane's view of the installation, built from
// Consul: the catalog (live instances and their health), the manifests the
// SDK writes per version, and the instance states under their sessions.
// Everything else in backplane — configuration and the console — reads a
// Catalog snapshot and reacts to changes; nothing else talks to Consul for
// discovery.
//
// A consumer takes a Source and follows it from its own goroutine:
//
//	func (x *Consumer) follow(ctx context.Context) error {
//	    changes := x.src.Changes(ctx) // closed when ctx ends
//	    for {
//	        x.apply(x.src.Current()) // the zero Catalog (Index 0) until synced
//	        if _, ok := <-changes; !ok {
//	            return nil
//	        }
//	    }
//	}
//
// Snapshots are shared and immutable: never modify their maps, slices or
// messages. Tests of consumers publish their own snapshots through a Hub.
package registry

import (
	"context"

	"golang.org/x/mod/semver"

	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
)

// Catalog is an immutable snapshot of the installation. A new snapshot is
// built on every change; readers never see one being modified.
type Catalog struct {
	// Index grows with every snapshot; equal indexes mean equal content.
	Index    uint64
	Services map[string]Service
}

// Service is one service of the installation.
type Service struct {
	Name string
	// Manifests by version, as written by the SDK.
	Manifests map[string]*backplanev1.Manifest
	// Instances with a published state or a catalog registration, by ID.
	Instances []Instance
}

// Instance is one running process.
type Instance struct {
	ID string
	// State is the SDK's instance state; nil when only the catalog knows
	// the instance (it registered but its state expired or is not written).
	State *backplanev1.InstanceState
	// Registered: in the Consul catalog (serving phase).
	Registered bool
	// Healthy: the catalog health check passes.
	Healthy bool
	// Address and Port are what traffic goes to (catalog registration);
	// Port is the public port, 0 without managed routes.
	Address string
	Port    uint32
	// Tags of the catalog registration (BACKPLANE_CONSUL_TAGS).
	Tags []string
}

// Manifest is the manifest of the instance's version, nil when missing.
func (s Service) Manifest(version string) *backplanev1.Manifest { return s.Manifests[version] }

// Latest is the manifest of the highest version any live instance runs,
// falling back to any known manifest; nil when there is none.
func (s Service) Latest() *backplanev1.Manifest {
	var best *backplanev1.Manifest

	for _, in := range s.Instances {
		if in.State == nil {
			continue
		}

		if m := s.Manifests[in.State.GetVersion()]; m != nil && (best == nil || newer(m.GetVersion(), best.GetVersion())) {
			best = m
		}
	}

	if best != nil {
		return best
	}

	for _, m := range s.Manifests {
		if best == nil || newer(m.GetVersion(), best.GetVersion()) {
			best = m
		}
	}

	return best
}

// Healthy lists the instances traffic may go to: registered and passing.
func (s Service) Healthy() []Instance {
	var out []Instance

	for _, in := range s.Instances {
		if in.Registered && in.Healthy {
			out = append(out, in)
		}
	}

	return out
}

// Source is what provides snapshots: the Consul-backed Registry, or a fake
// in tests.
type Source interface {
	// Current is the latest snapshot; the zero Catalog before the first.
	Current() Catalog
	// Changes delivers a signal after every new snapshot until ctx ends;
	// slow readers get one pending signal, never a backlog.
	Changes(ctx context.Context) <-chan struct{}
}

// newer reports whether version a sorts above b as semver ("1.2.3",
// "0.0.0+abc"); versions that are not semver compare as strings.
func newer(a, b string) bool {
	va, vb := "v"+a, "v"+b
	if semver.IsValid(va) && semver.IsValid(vb) {
		return semver.Compare(va, vb) > 0
	}

	return a > b
}
