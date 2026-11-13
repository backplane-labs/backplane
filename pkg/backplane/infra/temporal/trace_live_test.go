package temporal_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	commonpb "go.temporal.io/api/common/v1"
	schedulepb "go.temporal.io/api/schedule/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/durationpb"

	inftemporal "github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal/temporaltest"
)

// spans records what the client and its workers trace: the SDK's global
// provider and W3C propagator stand in, set once for the package (the
// tracing interceptor takes the global tracer when options are built).
var spans = tracetest.NewSpanRecorder()

func TestMain(m *testing.M) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	os.Exit(m.Run())
}

func shout(_ context.Context, in string) (string, error) { return strings.ToUpper(in), nil }

func greet(ctx workflow.Context, in string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})

	var out string

	err := workflow.ExecuteActivity(ctx, shout, in).Get(ctx, &out)

	return out, err
}

// tracedClient is a client with the SDK's options (tracing interceptor
// included) and a worker of greet and shout on a queue of its own; the
// worker takes its interceptors from the client.
func tracedClient(t *testing.T) (client.Client, string) {
	t.Helper()

	addr := temporaltest.Addr(t)

	opts, err := inftemporal.Options(inftemporal.Config{Addr: addr, Namespace: temporaltest.Namespace}, "test", nil)
	if err != nil {
		t.Fatal(err)
	}

	c, err := inftemporal.Dial(t.Context(), inftemporal.Config{Addr: addr}, opts)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(c.Close)

	queue := temporaltest.Name("traced")
	w := worker.New(c, queue, worker.Options{})
	w.RegisterWorkflow(greet)
	w.RegisterActivity(shout)

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(w.Stop)

	return c, queue
}

// spansOf are the ended spans of trace id whose name starts with prefix.
func spansOf(id trace.TraceID, prefix string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan

	for _, s := range spans.Ended() {
		if s.SpanContext().TraceID() == id && strings.HasPrefix(s.Name(), prefix) {
			out = append(out, s)
		}
	}

	return out
}

// A workflow started by a caller runs in the caller's trace: start → the
// workflow on the worker → its activity.
func TestWorkflowContinuesTheCallersTrace(t *testing.T) {
	t.Parallel()

	c, queue := tracedClient(t)

	ctx, caller := otel.Tracer("test").Start(t.Context(), "caller")

	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		TaskQueue: queue, WorkflowExecutionTimeout: 30 * time.Second,
	}, greet, "hi")
	if err != nil {
		t.Fatal(err)
	}

	var out string
	if err := run.Get(t.Context(), &out); err != nil || out != "HI" {
		t.Fatalf("run: %q %v", out, err)
	}

	caller.End()

	id := caller.SpanContext().TraceID()
	if len(spansOf(id, "StartWorkflow:greet")) == 0 || len(spansOf(id, "RunWorkflow:greet")) == 0 ||
		len(spansOf(id, "RunActivity:shout")) == 0 {
		for _, s := range spans.Ended() {
			t.Logf("span %s trace %s parent %s", s.Name(), s.SpanContext().TraceID(), s.Parent().SpanID())
		}

		t.Fatalf("the workflow and its activity are not in the caller's trace %s", id)
	}
}

// Every scheduled run starts a trace of its own. backplane creates its
// schedules through the workflow service API (internal/ops schedule
// files): the action carries no caller's trace context, so each run's
// workflow span on the worker is a root, one trace per run.
func TestScheduledRunsAreTracesOfTheirOwn(t *testing.T) {
	t.Parallel()

	c, queue := tracedClient(t)
	id := temporaltest.Name("schedule")

	_, err := c.WorkflowService().CreateSchedule(t.Context(), &workflowservice.CreateScheduleRequest{
		Namespace: temporaltest.Namespace, ScheduleId: id, RequestId: id,
		Schedule: &schedulepb.Schedule{
			Spec: &schedulepb.ScheduleSpec{},
			Action: &schedulepb.ScheduleAction{Action: &schedulepb.ScheduleAction_StartWorkflow{
				StartWorkflow: &workflowpb.NewWorkflowExecutionInfo{
					WorkflowId: id + "-run", WorkflowType: &commonpb.WorkflowType{Name: "greet"},
					TaskQueue: &taskqueuepb.TaskQueue{Name: queue}, Input: payloads(t, "tick"),
					WorkflowExecutionTimeout: durationpb.New(30 * time.Second),
				},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	handle := c.ScheduleClient().GetHandle(t.Context(), id)
	t.Cleanup(func() { _ = handle.Delete(context.Background()) })

	for range 2 {
		if err := handle.Trigger(t.Context(), client.ScheduleTriggerOptions{}); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Second) // the server takes one trigger per second
	}

	var runs []sdktrace.ReadOnlySpan

	for deadline := time.Now().Add(30 * time.Second); len(runs) < 2 && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		runs = runs[:0]

		for _, s := range spans.Ended() {
			for _, a := range s.Attributes() {
				if s.Name() == "RunWorkflow:greet" && string(a.Key) == "temporalWorkflowID" &&
					strings.HasPrefix(a.Value.AsString(), id+"-run") {
					runs = append(runs, s)
				}
			}
		}
	}

	if len(runs) < 2 || runs[0].SpanContext().TraceID() == runs[1].SpanContext().TraceID() ||
		runs[0].Parent().IsValid() || runs[1].Parent().IsValid() {
		t.Fatalf("scheduled runs: %d root spans of distinct traces, want 2", len(runs))
	}
}

// payloads is in as a workflow's input.
func payloads(t *testing.T, in string) *commonpb.Payloads {
	t.Helper()

	p, err := converter.GetDefaultDataConverter().ToPayloads(in)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

// A schedule created through the SDK's client in a caller's span: its runs
// still start traces of their own, not the creator's.
func TestScheduleClientRunsAreTracesOfTheirOwn(t *testing.T) {
	t.Parallel()

	c, queue := tracedClient(t)

	ctx, creator := otel.Tracer("test").Start(t.Context(), "creator")
	id := temporaltest.Name("schedule-client")

	handle, err := c.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: id, Spec: client.ScheduleSpec{},
		Action: &client.ScheduleWorkflowAction{
			ID: id + "-run", Workflow: greet, Args: []any{"tick"}, TaskQueue: queue,
			WorkflowExecutionTimeout: 30 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	creator.End()
	t.Cleanup(func() { _ = handle.Delete(context.Background()) })

	if err := handle.Trigger(t.Context(), client.ScheduleTriggerOptions{}); err != nil {
		t.Fatal(err)
	}

	var run sdktrace.ReadOnlySpan

	for deadline := time.Now().Add(30 * time.Second); run == nil && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		for _, s := range spans.Ended() {
			for _, a := range s.Attributes() {
				if s.Name() == "RunWorkflow:greet" && string(a.Key) == "temporalWorkflowID" &&
					strings.HasPrefix(a.Value.AsString(), id+"-run") {
					run = s
				}
			}
		}
	}

	if run == nil || run.Parent().IsValid() || run.SpanContext().TraceID() == creator.SpanContext().TraceID() {
		t.Fatalf("scheduled run: %v", run)
	}
}
