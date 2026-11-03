package workflowtest_test

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/hook"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal/temporaltest"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows/workflowtest"
)

type (
	text struct {
		Text string `json:"text"`
	}
	greeting struct {
		Text string `json:"text"`
	}
)

// fatalTB records a fatal failure and ends the goroutine, as the testing
// package does, without failing the test.
type fatalTB struct {
	testing.TB

	msg string
}

func (f *fatalTB) Helper() {}

func (f *fatalTB) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)

	runtime.Goexit()
}

// greeter is a service whose workflow runs its own activity and calls its
// hook.
func greeter(tb testing.TB, service string) *backplanetest.Harness {
	tb.Helper()

	h := backplanetest.New(tb, backplanetest.Name(service))
	greet := hook.Declare[text, greeting](h.Root(), "Greet")
	upper := workflows.HandleActivity(h.Root(), "Upper", func(_ context.Context, in text) (text, error) {
		return text{Text: in.Text + "!"}, nil
	})

	workflows.Declare(h.Root(), "Welcome", func(ctx workflow.Context, in text) (greeting, error) {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})

		loud, err := upper.Execute(ctx, in)
		if err != nil {
			return greeting{}, err
		}

		return workflows.CallHook(ctx, greet, loud)
	})

	backplanetest.Answer(h, greet, func(_ context.Context, in text) (greeting, error) {
		return greeting{Text: "hello, " + in.Text}, nil
	})

	return h
}

// welcome runs the Welcome workflow on the service's queue.
func welcome(t *testing.T, c client.Client, service, name string) {
	t.Helper()

	run, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{
		TaskQueue: service, WorkflowExecutionTimeout: 30 * time.Second,
	}, "Welcome", text{Text: name})
	if err != nil {
		t.Fatal(err)
	}

	var out greeting
	if err := run.Get(t.Context(), &out); err != nil || out.Text != "hello, "+name+"!" {
		t.Fatalf("welcome %s: %+v %v", name, out, err)
	}
}

// The service's workflow runs on a real Temporal: its own activity and
// its hook answered by the harness. A replica (a second harness of the
// service) joins the queue and the endpoint; when the first one stops, the
// replica serves the workflow and its hook.
func TestWorker(t *testing.T) {
	t.Parallel()

	c := temporaltest.Dial(t)
	service := temporaltest.Name("wt")

	first := greeter(t, service)
	workflowtest.Worker(t, first, c)
	first.Start()

	welcome(t, c, service, "ann")

	replica := greeter(t, service)
	workflowtest.Worker(t, replica, c)
	replica.Start()

	first.Stop()

	welcome(t, c, service, "bob")
}

// An endpoint of the service's name that routes elsewhere fails the
// start.
func TestWorkerEndpointTaken(t *testing.T) {
	t.Parallel()

	c := temporaltest.Dial(t)
	service := temporaltest.Name("wt")
	temporaltest.Endpoint(t, c, service, "elsewhere")

	fatal := &fatalTB{TB: t}
	h := greeter(fatal, service)
	workflowtest.Worker(fatal, h, c)

	done := make(chan struct{})

	go func() {
		defer close(done)

		h.Start()
	}()

	<-done

	if !strings.Contains(fatal.msg, workflowtest.ErrEndpointTaken.Error()) {
		t.Fatalf("start: %q", fatal.msg)
	}
}
