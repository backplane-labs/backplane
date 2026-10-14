package flows_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/examples/hello/internal/flows"
	"github.com/backplane-labs/backplane/examples/hello/internal/hellotest"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows/workflowtest"
)

func setup(t *testing.T) (*hellotest.Tree, *flows.Flows) {
	t.Helper()

	tree := hellotest.New(t, nil)
	f := flows.New(tree.H.Root(), tree.Greeter, tree.Store)
	tree.H.Start()

	return tree, f
}

func TestEcho(t *testing.T) {
	t.Parallel()

	tree, _ := setup(t)

	out, err := backplanetest.Activity[flows.EchoIn, flows.EchoOut](t.Context(), tree.H, "Echo", flows.EchoIn{Text: "hi"})
	if err != nil || out.Text != "hi" || !strings.HasPrefix(out.Key, "backplanetest/Echo/") {
		t.Fatalf("echo: %+v %v", out, err)
	}

	long := flows.EchoIn{Text: strings.Repeat("x", 2<<10)}
	if _, err := backplanetest.Activity[flows.EchoIn, flows.EchoOut](t.Context(), tree.H, "Echo", long); !errors.Is(err, flows.ErrTooLong) {
		t.Fatalf("too long: %v", err)
	}
}

// Welcome greets through the hook when it is answered, locally otherwise.
func TestWelcome(t *testing.T) {
	t.Parallel()

	tree, f := setup(t)

	out, err := workflowtest.WorkflowActivity[flows.GreetIn, flows.GreetOut](workflowtest.New(t, tree.H), "Welcome",
		flows.GreetIn{Name: "ann"})
	if err != nil || out.Text != "Hello, ann!" {
		t.Fatalf("without a binding: %+v %v", out, err)
	}

	backplanetest.Answer(tree.H, f.Greet, func(_ context.Context, in flows.GreetIn) (flows.GreetOut, error) {
		return flows.GreetOut{Text: "Bound hello, " + in.Name}, nil
	})

	out, err = workflowtest.WorkflowActivity[flows.GreetIn, flows.GreetOut](workflowtest.New(t, tree.H), "Welcome",
		flows.GreetIn{Name: "ann"})
	if err != nil || out.Text != "Bound hello, ann" {
		t.Fatalf("bound: %+v %v", out, err)
	}

	// Both local and bound greetings publish the text actually returned.
	if got := backplanetest.Events(tree.H, tree.Greeter.Greeted()); len(got) != 2 || got[0].Name != "ann" || got[1].Text != "Bound hello, ann" || got[1].Count != 2 {
		t.Fatalf("events: %+v", got)
	}
}

func TestGreetManyAndReport(t *testing.T) {
	t.Parallel()

	tree, f := setup(t)

	w := workflowtest.New(t, tree.H)
	w.ExecuteWorkflow("GreetMany", flows.ManyIn{Names: []string{"ann", "bob"}})

	var many flows.ManyOut
	if err := w.GetWorkflowResult(&many); err != nil || !slices.Equal(many.Texts, []string{"Hello, ann!", "Hello, bob!"}) {
		t.Fatalf("greet many: %+v %v", many, err)
	}

	w = workflowtest.New(t, tree.H)
	w.ExecuteWorkflow(f.Report, struct{}{})

	var total uint64
	if err := w.GetWorkflowResult(&total); err != nil || total != 2 {
		t.Fatalf("report: %d %v", total, err)
	}
}

func TestManifest(t *testing.T) {
	t.Parallel()

	tree, _ := setup(t)
	m := tree.H.Manifest()

	hooks := m.GetHooks()
	if len(hooks) != 1 || hooks[0].GetName() != "Greet" || !hooks[0].GetRequired() ||
		hooks[0].GetTimeout().AsDuration() != 5*time.Second {
		t.Errorf("hooks: %v", hooks)
	}

	kinds := map[string]backplanev1.ActivityKind{}
	for _, a := range m.GetActivities() {
		kinds[a.GetName()] = a.GetKind()
	}

	if len(kinds) != 2 || kinds["Echo"] != backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY ||
		kinds["Welcome"] != backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW {
		t.Errorf("activities: %v", kinds)
	}

	if w := m.GetWorkflows(); len(w) != 2 || w[0].GetName() != "GreetMany" || w[1].GetName() != "Report" {
		t.Errorf("workflows: %v", w)
	}

	if s := m.GetSchedules(); len(s) != 0 {
		t.Errorf("SDK declared administrative schedules: %v", s)
	}
}
