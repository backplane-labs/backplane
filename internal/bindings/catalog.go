package bindings

import (
	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/registry"
)

// Catalog resolves full names ("<service>.<Name>") to what the manifests
// declare. Validation and Compile see the installation through it.
type Catalog interface {
	Hook(name string) (*backplanev1.Hook, bool)
	Event(name string) (*backplanev1.Event, bool)
	Activity(name string) (*backplanev1.Activity, bool)
}

// Defaults are what a definition leaves to the installation.
type Defaults struct {
	// Runtime of a definition that names none; unspecified: Temporal.
	Runtime consolev1.Runtime
}

// Defaulter is a Catalog that also carries the installation's defaults;
// compile reads them when it has them.
type Defaulter interface {
	Defaults() Defaults
}

// WithDefaults is cat with the installation's defaults.
func WithDefaults(cat Catalog, d Defaults) Catalog { return defaulted{Catalog: cat, d: d} }

type defaulted struct {
	Catalog

	d Defaults
}

func (c defaulted) Defaults() Defaults { return c.d }

// DefaultsOf are cat's defaults, zero when it carries none.
func DefaultsOf(cat Catalog) Defaults {
	if d, ok := cat.(Defaulter); ok {
		return d.Defaults()
	}

	return Defaults{}
}

// FromRegistry is the catalog of the latest manifest of every service in
// a registry snapshot (registry.Service.Latest: the highest version a live
// instance runs).
func FromRegistry(c registry.Catalog) Catalog { return registryCatalog{c: c} }

type registryCatalog struct{ c registry.Catalog }

func (r registryCatalog) manifest(full string) (*backplanev1.Manifest, string) {
	svc, name := SplitName(full)

	s, ok := r.c.Services[svc]
	if !ok {
		return nil, ""
	}

	return s.Latest(), name
}

func (r registryCatalog) Hook(full string) (*backplanev1.Hook, bool) {
	m, name := r.manifest(full)
	for _, h := range m.GetHooks() {
		if h.GetName() == name {
			return h, true
		}
	}

	return nil, false
}

func (r registryCatalog) Event(full string) (*backplanev1.Event, bool) {
	m, name := r.manifest(full)
	for _, e := range m.GetEvents() {
		if e.GetName() == name {
			return e, true
		}
	}

	return nil, false
}

func (r registryCatalog) Activity(full string) (*backplanev1.Activity, bool) {
	m, name := r.manifest(full)
	for _, a := range m.GetActivities() {
		if a.GetName() == name {
			return a, true
		}
	}

	return nil, false
}

// Manifests is a Catalog over manifests given directly: tests, and tools
// that validate against manifests they hold.
type Manifests []*backplanev1.Manifest

func (ms Manifests) manifest(full string) (*backplanev1.Manifest, string) {
	svc, name := SplitName(full)
	for _, m := range ms {
		if m.GetService() == svc {
			return m, name
		}
	}

	return nil, ""
}

// Hook implements Catalog.
func (ms Manifests) Hook(full string) (*backplanev1.Hook, bool) {
	m, name := ms.manifest(full)
	for _, h := range m.GetHooks() {
		if h.GetName() == name {
			return h, true
		}
	}

	return nil, false
}

// Event implements Catalog.
func (ms Manifests) Event(full string) (*backplanev1.Event, bool) {
	m, name := ms.manifest(full)
	for _, e := range m.GetEvents() {
		if e.GetName() == name {
			return e, true
		}
	}

	return nil, false
}

// Activity implements Catalog.
func (ms Manifests) Activity(full string) (*backplanev1.Activity, bool) {
	m, name := ms.manifest(full)
	for _, a := range m.GetActivities() {
		if a.GetName() == name {
			return a, true
		}
	}

	return nil, false
}
