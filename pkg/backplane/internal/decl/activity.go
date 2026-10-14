package decl

import (
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

// declare records the activity in the manifest.
func Activity[Req, Res any](
	scope deps.Scope, name string, kind backplanev1.ActivityKind, opts []ActivityOption,
) *env.Env {
	e, _ := Env(scope, "activity "+name)
	CheckName("activity", name)

	a := &backplanev1.Activity{
		Name:   name,
		Kind:   kind,
		Input:  Schema[Req](e.Service, "activity_"+name+"_in"),
		Output: Schema[Res](e.Service, "activity_"+name+"_out"),
	}
	for _, opt := range opts {
		opt.apply(a)
	}

	e.Manifest.Activity(a)

	return e
}
