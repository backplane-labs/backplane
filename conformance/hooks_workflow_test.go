package conformance_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/hook"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
)

// TestHookFromWorkflow: the service's own workflow raises its hook with
// WorkflowCall — the Nexus operation directly, no CallHook workflow — and
// gets the binding's answer, or ErrNoBinding's readable error. A
// workflow-backed activity runs by name on the service's queue with the
// ActivityCall envelope, as a binding step runs it.
//
//nolint:paralleltest // sets the environment
func TestHookFromWorkflow(t *testing.T) {
	addr := temporalAddr(t)
	name := uniqueName("wfhook")
	playBackplane(t, addr, name)

	st, _ := runWorkflows(t, addr, name, func(root deps.Scope) {
		greet := hook.Declare[greetIn, greetOut](root, "Greet", hook.Required())
		activity.Handle(root, "Echo", func(_ context.Context, in echoIn) (echoOut, error) { return echoOut(in), nil })

		workflows.Declare(root, "CallGreet", func(ctx workflow.Context, in greetIn) (greetOut, error) {
			return workflows.CallHook(ctx, greet, in)
		})

		workflows.Activity(root, "Shout", func(_ workflow.Context, in echoIn) (echoOut, error) {
			return echoOut{Text: strings.ToUpper(in.Text)}, nil
		})
	})

	temporalClient := workflowClient(t, st.scope)
	opts := func(id string) client.StartWorkflowOptions {
		return client.StartWorkflowOptions{ID: name + "/" + id, TaskQueue: workflows.Queue(st.scope)}
	}

	run, err := temporalClient.ExecuteWorkflow(t.Context(), opts("bound"), "CallGreet", greetIn{Name: "workflow"})
	if err != nil {
		t.Fatal(err)
	}

	var out greetOut
	if err := run.Get(t.Context(), &out); err != nil || out.Text != "Hello, workflow" {
		t.Errorf("bound hook from a workflow: %+v %v", out, err)
	}

	run, err = temporalClient.ExecuteWorkflow(t.Context(), opts("unbound"), "CallGreet", greetIn{Name: "nobody"})
	if err != nil {
		t.Fatal(err)
	}

	if err := run.Get(t.Context(), &out); err == nil || !strings.Contains(err.Error(), "hook "+name+".Greet: no binding") {
		t.Errorf("unbound hook from a workflow: %v", err)
	}

	payload, err := json.Marshal(echoIn{Text: "loud"})
	if err != nil {
		t.Fatal(err)
	}

	run, err = temporalClient.ExecuteWorkflow(t.Context(), opts("shout"), "Shout", &backplanev1.ActivityCall{
		Activity: "Shout", Payload: payload, Binding: "test", Step: "shout",
	})
	if err != nil {
		t.Fatal(err)
	}

	var res backplanev1.ActivityResult
	if err := run.Get(t.Context(), &res); err != nil {
		t.Fatalf("workflow-backed activity: %v", err)
	}

	var shouted echoOut
	if err := json.Unmarshal(res.GetPayload(), &shouted); err != nil || shouted.Text != "LOUD" {
		t.Errorf("workflow-backed activity: %s %v", res.GetPayload(), err)
	}
}
