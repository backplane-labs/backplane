package decl

import (
	"google.golang.org/protobuf/proto"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// ActivityOption records transport-neutral operation metadata.
type (
	ActivityOption interface {
		apply(activity *backplanev1.Activity)
	}
	// ActivityOptionFunc is the internal option constructor.
	ActivityOptionFunc func(*backplanev1.Activity)
)

func (f ActivityOptionFunc) apply(a *backplanev1.Activity) { f(a) }

// Activity records the activity in the manifest with the schemas of Req
// and Res.
func Activity[Req, Res any](
	scope deps.Scope, name string, kind backplanev1.ActivityKind, opts []ActivityOption,
) *env.Env {
	e, _ := Env(scope, "activity "+name)

	return declare(e, name, kind, Schema[Req](scope, e.Service, "activity_"+name+"_in"),
		Schema[Res](scope, e.Service, "activity_"+name+"_out"), opts)
}

// ActivitySchema records the activity in the manifest with schemas known
// at run time; nil is no schema (plain JSON).
func ActivitySchema(
	scope deps.Scope, name string, kind backplanev1.ActivityKind, in, out *sp.Schema, opts []ActivityOption,
) *env.Env {
	e, _ := Env(scope, "activity "+name)

	return declare(e, name, kind, named(in, e.Service, "activity_"+name+"_in"),
		named(out, e.Service, "activity_"+name+"_out"), opts)
}

// named is a copy of s identified as the service's schema of name, as a
// reflected one would be.
func named(s *sp.Schema, service, name string) *sp.Schema {
	if s == nil {
		return nil
	}

	c, _ := proto.Clone(s).(*sp.Schema)
	c.Id = sp.ID(sp.Namespace(service), sp.SchemaName(name), sp.Ver(1, 0, 0))

	return c
}

func declare(
	e *env.Env, name string, kind backplanev1.ActivityKind, in, out *sp.Schema, opts []ActivityOption,
) *env.Env {
	CheckName("activity", name)

	a := &backplanev1.Activity{Name: name, Kind: kind, Input: in, Output: out}
	for _, opt := range opts {
		opt.apply(a)
	}

	e.Manifest.Activity(a)

	return e
}
