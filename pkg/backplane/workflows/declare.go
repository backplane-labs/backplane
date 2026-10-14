package workflows

import (
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	internal "github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
)

// DeclareOption tunes Declare.
type DeclareOption func(w *backplanev1.Workflow)

// Describe is the workflow's description for the console.
func Describe(s string) DeclareOption {
	return func(w *backplanev1.Workflow) { w.Description = s }
}

// Declare registers fn on the service's worker as workflow type name and
// records it in the manifest with the schemas of In and Out, so the
// console can start it with a form. name is CamelCase ([A-Z][A-Za-z0-9]*),
// anything else panics; declaring after Run panics. The input arrives
// through Temporal's default converter: JSON, protojson for proto
// messages. A workflow Declare registered is not registered again with
// Register.
//
//	workflows.Declare(root, "Ship", orders.Ship, workflows.Describe("ships an order"))
func Declare[In, Out any](
	scope deps.Scope, name string, fn func(ctx workflow.Context, in In) (Out, error), opts ...DeclareOption,
) {
	e, _ := decl.Env(scope, "workflow "+name)
	if e.Manifest.Sealed() {
		panic("backplane: workflow " + name + " declared after Run")
	}

	internal.CheckName("workflow", name)

	w := &backplanev1.Workflow{
		Name:   name,
		Input:  decl.Schema[In](scope, e.Service, "workflow_"+name+"_in"),
		Output: decl.Schema[Out](scope, e.Service, "workflow_"+name+"_out"),
	}
	for _, opt := range opts {
		opt(w)
	}

	e.Manifest.Workflow(w)
	e.RegisterWorker(func(registry any) {
		if r, ok := registry.(worker.Registry); ok {
			r.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{Name: name})
		}
	})
}
