// Package wiring checks a wiring directory (BACKPLANE_WIRING_DIR: bindings
// and rules as YAML) against the declarations of the services it wires,
// as backplane does when it keeps the directory in force. A test of an
// installation runs it, so a binding backplane would reject fails CI
// instead of the stand:
//
//	violations, err := wiring.Check("deployments/wiring", []*backplanev1.Manifest{
//		coreHarness.Manifest(), relayHarness.Manifest(),
//	})
package wiring

import (
	"errors"
	"fmt"
	"os"

	"google.golang.org/protobuf/encoding/protojson"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
)

// Violation is what backplane refuses in one binding or rule.
type Violation struct {
	// File is the YAML file, relative to the directory.
	File string
	// Kind is "binding" or "rule"; Name is the binding's hook or the rule's name.
	Kind, Name string
	// Path is the JSON Pointer of the place in the definition
	// ("/steps/send/input/to"); empty for the whole definition.
	Path string
	// Code is the stable code of the violation (backplane's console shows the same).
	Code    string
	Message string
}

// String is the violation as a test failure prints it.
func (v Violation) String() string {
	return fmt.Sprintf("%s: %s %s %s: %s (%s)", v.File, v.Kind, v.Name, v.Path, v.Message, v.Code)
}

// Option configures Check.
type Option func(*settings)

type settings struct{ runtime consolev1.Runtime }

// DirectRuntime checks the definitions that name no runtime as the direct
// runtime's: an installation whose BACKPLANE_WIRING_RUNTIME resolves to
// direct. Without it they are checked as Temporal's.
func DirectRuntime() Option {
	return func(s *settings) { s.runtime = consolev1.Runtime_RUNTIME_DIRECT }
}

// Check validates every binding and rule of dir against manifests, the
// declarations of every service the wiring names (a backplanetest
// Harness's Manifest). A service missing from manifests is a violation, as
// it is a binding waiting for its service on a stand. The error is a
// directory that does not read: a YAML or unknown-field error, a duplicate.
func Check(dir string, manifests []*backplanev1.Manifest, opts ...Option) ([]Violation, error) {
	s := settings{runtime: consolev1.Runtime_RUNTIME_TEMPORAL}
	for _, o := range opts {
		o(&s)
	}

	cat := bindings.WithDefaults(bindings.Manifests(manifests), bindings.Defaults{Runtime: s.runtime})

	found, err := bindings.CheckDir(dir, cat)
	if err != nil {
		return nil, fmt.Errorf("wiring: %w", err)
	}

	out := make([]Violation, len(found))
	for i, f := range found {
		out[i] = Violation{File: f.File, Kind: f.Kind, Name: f.Name, Path: f.Path, Code: f.Code, Message: f.Message}
	}

	return out, nil
}

// ErrNotManifest: a file LoadManifests reads is JSON but no manifest.
var ErrNotManifest = errors.New("wiring: not a manifest")

// LoadManifests reads manifests "<binary> manifest" printed
// (backplane.ManifestCommand): a service's binary or image run with an
// installation's configuration declares what it does there, the manifest to
// check that installation's wiring against.
func LoadManifests(paths ...string) ([]*backplanev1.Manifest, error) {
	out := make([]*backplanev1.Manifest, 0, len(paths))

	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("wiring: %w", err)
		}

		var m backplanev1.Manifest
		if err := protojson.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("wiring: %s: %w", path, err)
		}

		if m.GetService() == "" {
			return nil, fmt.Errorf("%w: %s: no service", ErrNotManifest, path)
		}

		out = append(out, &m)
	}

	return out, nil
}
