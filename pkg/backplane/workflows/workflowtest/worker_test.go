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

// The service's workflow runs on a real Temporal: its own activity and
// its hook answered by the harness; a second harness of the same service
// at once is refused; the stop deletes the endpoint, so the next one
// starts.
func TestWorker(t *testing.T) {
	t.Parallel()

	c := temporaltest.Dial(t)
	service := temporaltest.Name("wt")

	h := greeter(t, service)
	workflowtest.Worker(t, h, c)
	h.Start()

	run, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{
		TaskQueue: h.Service(), WorkflowExecutionTimeout: 30 * time.Second,
	}, "Welcome", text{Text: "ann"})
	if err != nil {
		t.Fatal(err)
	}

	var out greeting
	if err := run.Get(t.Context(), &out); err != nil || out.Text != "hello, ann!" {
		t.Fatalf("welcome: %+v %v", out, err)
	}

	// The same service at once: its endpoint is taken, the start fails.
	fatal := &fatalTB{TB: t}
	twin := greeter(fatal, service)
	workflowtest.Worker(fatal, twin, c)

	done := make(chan struct{})

	go func() {
		defer close(done)

		twin.Start()
	}()

	<-done

	if !strings.Contains(fatal.msg, workflowtest.ErrEndpointTaken.Error()) {
		t.Fatalf("twin: %q", fatal.msg)
	}

	// Stopped, the endpoint is free again.
	h.Stop()

	next := greeter(t, service)
	workflowtest.Worker(t, next, c)
	next.Start()
}
