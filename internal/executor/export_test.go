package executor

import (
	"context"

	historypb "go.temporal.io/api/history/v1"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/registry"
)

// ErrDefault is errDefault: an optional hook answers {}.
var ErrDefault = errDefault

// Detached is an Executor without a node: no goroutines, no worker —
// Prepare, Required, Hooks only.
func Detached(b Bindings, src registry.Source) *Executor {
	return &Executor{
		bindings: b, src: src, ns: defaultNamespace, queue: "backplane", author: sessionAuthor,
		programs: map[string]compiled{}, warned: map[string]bool{},
	}
}

// Prepare is prepare.
func (x *Executor) Prepare(ctx context.Context, hook string, call *backplanev1.HookCall) (Input, error) {
	return x.prepare(ctx, hook, call)
}

// Required is required.
func (x *Executor) Required(hook, instance string) bool { return x.required(hook, instance) }

// Hooks is hooksOf as a plain map.
func Hooks(cat registry.Catalog) map[string][]string { return hooksOf(cat) }

// TimelineOf is timeline over a marshaled program.
func TimelineOf(
	program []byte, events []*historypb.HistoryEvent, pending []*consolev1.PendingActivity,
) []*consolev1.StepRun {
	return timeline(programSteps(program), events, pending)
}
