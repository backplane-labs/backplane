// Package configrt is what the SDK core needs from a live configuration and
// authors do not: the effective values for instance state, schema walks for
// the manifest and provenance. Backend clients belong to optional drivers.
package configrt

import (
	"strings"

	"google.golang.org/protobuf/proto"

	sp "github.com/gopherex/schemapb/go/schemapb"
	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// RevisionKey, under config/<service>/, holds the console's revision of the
// values next to it; written in one transaction with them.
const RevisionKey = "_revision"

// LiveAnnotation marks a Live field in the configuration schema: the console
// may change it and Consul KV overrides apply only to such fields.
const LiveAnnotation = "backplane.live"

// Source names carry their kind as a prefix; provenance maps back from it.
const (
	SourceFile   = "file:"
	SourceEnv    = "env:"
	SourceConsul = "consul:"
)

// State is the SDK's view of a live configuration.
type State interface {
	// Schema of the configuration; Live fields carry LiveAnnotation.
	Schema() *sp.Schema
	// LivePaths are dot-separated paths of Live fields.
	LivePaths() []string
	Effective() Effective
	// OnChange calls fn whenever Effective or Degraded may have changed.
	OnChange(fn func())
	Degraded() error
	// SetLog routes the configuration's own logging (rejected updates,
	// Consul layer transitions) to the service logger; until then it goes
	// to log/slog.
	SetLog(log *xlog.Logger)
	Close() error
}

// Effective is what the instance runs with: masked values, the layer each
// path came from, the console revision they came from, and the last
// rejected update.
type Effective struct {
	Values  []byte
	Sources map[string]backplanev1.ConfigSource
	// Revision of config/<service>/_revision read together with the applied
	// values; 0 when absent.
	Revision uint64
	// Err: why the last update was not applied; nil once a later one is.
	Err error
	// RejectedRevision of the update Err refers to; 0 when none.
	RejectedRevision uint64
}

// SourceOf maps a source name to its kind.
func SourceOf(name string) backplanev1.ConfigSource {
	switch {
	case strings.HasPrefix(name, SourceConsul):
		return backplanev1.ConfigSource_CONFIG_SOURCE_KV
	case strings.HasPrefix(name, SourceEnv):
		return backplanev1.ConfigSource_CONFIG_SOURCE_ENV
	case strings.HasPrefix(name, SourceFile):
		return backplanev1.ConfigSource_CONFIG_SOURCE_FILE
	}

	return backplanev1.ConfigSource_CONFIG_SOURCE_DEFAULT
}

// LivePaths lists the paths of Live fields in schema.
func LivePaths(schema *sp.Schema) [][]string {
	var paths [][]string

	walk(schema, nil, func(f *sp.Schema_Field, path []string) bool {
		if isLive(f) {
			paths = append(paths, path)

			return false
		}

		return true
	})

	return paths
}

// WithoutLiveRequired copies schema with every Live field optional: the
// layers below Consul need not supply what only Consul may.
func WithoutLiveRequired(schema *sp.Schema) *sp.Schema {
	out, _ := proto.Clone(schema).(*sp.Schema)

	walk(out, nil, func(f *sp.Schema_Field, _ []string) bool {
		if isLive(f) {
			f.Required = false

			return false
		}

		return true
	})

	return out
}

func isLive(f *sp.Schema_Field) bool { return f.GetAnnotations()[LiveAnnotation].GetBoolValue() }

// walk visits fields depth-first; visit returns whether to descend.
func walk(s *sp.Schema, prefix []string, visit func(f *sp.Schema_Field, path []string) bool) {
	for _, f := range s.GetFields() {
		path := append(append([]string{}, prefix...), f.GetName())
		if visit(f, path) {
			if obj := f.GetObject(); obj != nil {
				walk(obj.GetSchema(), path, visit)
			}
		}
	}
}
