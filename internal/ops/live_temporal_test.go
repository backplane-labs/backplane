package ops_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nexus-rpc/sdk-go/nexus"
	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc/codes"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/ops"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/wire"
)

// liveTemporal connects to BACKPLANE_TEST_TEMPORAL; skipped without it.
func liveTemporal(t *testing.T) client.Client {
	t.Helper()

	addr := os.Getenv("BACKPLANE_TEST_TEMPORAL")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_TEMPORAL not set (make up)")
	}

	c, err := client.DialContext(t.Context(), client.Options{
		HostPort: addr, Namespace: "default", Logger: tlog.NewStructuredLogger(slog.New(slog.DiscardHandler)),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(c.Close)

	return c
}

func serve(t *testing.T, c client.Client, queue string, register func(w worker.Worker)) {
	t.Helper()

	w := worker.New(c, queue, worker.Options{})
	register(w)

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(w.Stop)
}

type greetIn struct {
	Name string `json:"name"`
}

// The target service: activities Echo and Fail, workflow-backed activity
// Flow, declared workflows Greet and Wait.
func echo(_ context.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
	return &backplanev1.ActivityResult{Payload: call.GetPayload()}, nil
}

func fail(context.Context, *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
	return nil, temporal.NewNonRetryableApplicationError("svc.Fail: refused", wire.NonRetryableType, nil)
}

func flow(_ workflow.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
	return &backplanev1.ActivityResult{Payload: []byte(`"flowed ` + call.GetActivity() + `"`)}, nil
}

func greet(_ workflow.Context, in greetIn) (string, error) { return "Hello, " + in.Name, nil }

// wait returns on the "go" signal; a cancellation ends it canceled
// (Receive alone does not see cancellation).
func wait(ctx workflow.Context) (string, error) {
	var v string

	s := workflow.NewSelector(ctx)
	s.AddReceive(workflow.GetSignalChannel(ctx, "go"), func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, &v) })
	s.AddReceive(ctx.Done(), func(workflow.ReceiveChannel, bool) {})
	s.Select(ctx)

	if err := ctx.Err(); err != nil {
		return "", err
	}

	return "got " + v, nil
}

// TestWorkflowsLive: against Temporal, the console starts declared
// workflows, lists and describes runs, signals, cancels and terminates
// them, runs activities by name and raises hooks through Nexus, and
// operates schedules.
//
//nolint:paralleltest // one scenario over one Temporal
func TestWorkflowsLive(t *testing.T) {
	c := liveTemporal(t)
	svc := unique("opst")
	queue := "backplane-" + svc
	ctx := t.Context()

	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{svc: service(&backplanev1.Manifest{
		Service: svc, Version: "1.0.0",
		Activities: []*backplanev1.Activity{
			{Name: "Echo", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY},
			{Name: "Fail", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY},
			{Name: "Flow", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW},
		},
		Workflows: []*backplanev1.Workflow{{Name: "Greet"}, {Name: "Wait"}},
		Hooks:     []*backplanev1.Hook{{Name: "Ask"}},
		Schedules: []*backplanev1.Schedule{{Name: "Nightly", Workflow: "Greet"}},
	})})

	o := ops.Detached(hub, ops.WithTemporal(func() (client.Client, error) { return c, nil }, queue))
	srv := newServers(o)

	serve(t, c, queue, func(w worker.Worker) { ops.RegisterWorkflows(w) })
	serve(t, c, svc, func(w worker.Worker) {
		w.RegisterActivityWithOptions(echo, activity.RegisterOptions{Name: "Echo"})
		w.RegisterActivityWithOptions(fail, activity.RegisterOptions{Name: "Fail"})
		w.RegisterWorkflowWithOptions(flow, workflow.RegisterOptions{Name: "Flow"})
		w.RegisterWorkflowWithOptions(greet, workflow.RegisterOptions{Name: "Greet"})
		w.RegisterWorkflowWithOptions(wait, workflow.RegisterOptions{Name: "Wait"})
	})

	// Declared workflow: start, result, listed.
	started, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{
		Service: svc, Workflow: "Greet", Input: `{"name":"console"}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	run := waitRun(t, srv, started.GetWorkflowId(), consolev1.RunStatus_RUN_STATUS_COMPLETED)
	if run.GetResult() != `"Hello, console"` || run.GetInput() != `{"name":"console"}` ||
		run.GetRun().GetMemo()[wire.MemoSource] != `"admin"` || len(run.GetHistory()) == 0 {
		t.Fatalf("run: %v", run)
	}

	waitFor(t, "run listed", func() bool {
		runs, err := srv.workflows.ListRuns(ctx, &consolev1.ListRunsRequest{Service: svc, Workflow: "Greet"})

		return err == nil && len(runs.GetRuns()) == 1 && runs.GetRuns()[0].GetWorkflowId() == started.GetWorkflowId()
	})

	// Signal, cancel, terminate.
	signaled, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{Service: svc, Workflow: "Wait"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := srv.workflows.SignalRun(ctx, &consolev1.SignalRunRequest{
		WorkflowId: signaled.GetWorkflowId(), Signal: "go", Input: `"now"`,
	}); err != nil {
		t.Fatal(err)
	}

	if r := waitRun(t, srv, signaled.GetWorkflowId(), consolev1.RunStatus_RUN_STATUS_COMPLETED); r.GetResult() != `"got now"` {
		t.Fatalf("signaled: %v", r)
	}

	for action, want := range map[string]consolev1.RunStatus{
		"cancel": consolev1.RunStatus_RUN_STATUS_CANCELED, "terminate": consolev1.RunStatus_RUN_STATUS_TERMINATED,
	} {
		w, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{Service: svc, Workflow: "Wait"})
		if err != nil {
			t.Fatal(err)
		}

		if action == "cancel" {
			_, err = srv.workflows.CancelRun(ctx, &consolev1.CancelRunRequest{WorkflowId: w.GetWorkflowId()})
		} else {
			_, err = srv.workflows.TerminateRun(ctx, &consolev1.TerminateRunRequest{WorkflowId: w.GetWorkflowId(), Reason: "test"})
		}

		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}

		waitRun(t, srv, w.GetWorkflowId(), want)
	}

	if _, err := srv.workflows.GetRun(ctx, &consolev1.GetRunRequest{WorkflowId: "nope-" + svc}); code(t, err) != codes.NotFound {
		t.Fatalf("missing run: %v", err)
	}

	// Activities by name.
	for activityName, want := range map[string]string{"Echo": `{"a":1}`, "Flow": `"flowed ` + svc + `.Flow"`} {
		res, err := srv.calls.RunActivity(ctx, &consolev1.RunActivityRequest{Activity: svc + "." + activityName, Input: `{"a":1}`})
		if err != nil || res.GetResult().GetOutput() != want || res.GetResult().GetError() != "" {
			t.Fatalf("%s: %v %v", activityName, res, err)
		}
	}

	failed, err := srv.calls.RunActivity(ctx, &consolev1.RunActivityRequest{Activity: svc + ".Fail"})
	if err != nil || failed.GetResult().GetError() != "svc.Fail: refused" ||
		failed.GetResult().GetErrorType() != wire.NonRetryableType {
		t.Fatalf("failing activity: %v %v", failed, err)
	}

	// Hooks: no endpoint yet, then through a Nexus endpoint played here.
	if _, err := srv.calls.CallHook(ctx, &consolev1.CallHookRequest{Hook: svc + ".Ask"}); code(t, err) != codes.FailedPrecondition {
		t.Fatalf("no endpoint: %v", err)
	}

	playHooks(t, c, svc)

	asked, err := srv.calls.CallHook(ctx, &consolev1.CallHookRequest{Hook: svc + ".Ask", Input: `{"q":1}`, Key: "k1"})
	if err != nil || asked.GetResult().GetOutput() != `{"answer":{"q":1}}` ||
		asked.GetResult().GetWorkflowId() != "hook/"+svc+"/Ask/k1" {
		t.Fatalf("hook: %v %v", asked, err)
	}

	// Schedules: a declared one and one nobody declares.
	for _, name := range []string{"Nightly", "Old"} {
		if _, err := c.ScheduleClient().Create(ctx, client.ScheduleOptions{
			ID:     wire.ScheduleID(svc, name),
			Spec:   client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: time.Hour}}},
			Action: &client.ScheduleWorkflowAction{ID: svc + "/" + name, Workflow: "Greet", TaskQueue: svc, Args: []any{greetIn{Name: name}}},
			Memo:   map[string]any{wire.MemoService: svc},
		}); err != nil {
			t.Fatal(err)
		}

		id := wire.ScheduleID(svc, name)

		t.Cleanup(func() { _ = c.ScheduleClient().GetHandle(context.Background(), id).Delete(context.Background()) })
	}

	var schedules []*consolev1.ScheduleInfo

	waitFor(t, "schedules listed", func() bool {
		res, err := srv.schedules.ListSchedules(ctx, &consolev1.ListSchedulesRequest{Service: svc})
		schedules = res.GetSchedules()

		return err == nil && len(schedules) == 2 && schedules[1].GetState() != nil
	})

	if n := schedules[0]; n.GetName() != "Nightly" || n.GetDeclared() == nil || n.GetState().GetOwner() != svc ||
		n.GetState().GetWorkflowType() != "Greet" || len(n.GetState().GetNextActions()) == 0 {
		t.Fatalf("declared schedule: %v", n)
	}

	if old := schedules[1]; old.GetName() != "Old" || old.GetDeclared() != nil {
		t.Fatalf("undeclared schedule: %v", old)
	}

	if _, err := srv.schedules.PauseSchedule(ctx, &consolev1.PauseScheduleRequest{Service: svc, Name: "Nightly", Note: "maintenance"}); err != nil {
		t.Fatal(err)
	}

	desc, err := c.ScheduleClient().GetHandle(ctx, wire.ScheduleID(svc, "Nightly")).Describe(ctx)
	if err != nil || !desc.Schedule.State.Paused || !strings.HasPrefix(desc.Schedule.State.Note, "maintenance (") {
		t.Fatalf("paused: %v %v", desc, err)
	}

	if _, err := srv.schedules.UnpauseSchedule(ctx, &consolev1.UnpauseScheduleRequest{Service: svc, Name: "Nightly"}); err != nil {
		t.Fatal(err)
	}

	if _, err := srv.schedules.TriggerSchedule(ctx, &consolev1.TriggerScheduleRequest{Service: svc, Name: "Nightly"}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "triggered run", func() bool {
		d, err := c.ScheduleClient().GetHandle(ctx, wire.ScheduleID(svc, "Nightly")).Describe(ctx)

		return err == nil && !d.Schedule.State.Paused && len(d.Info.RecentActions) > 0
	})
}

// waitRun waits until the run reaches status and returns it.
func waitRun(t *testing.T, srv servers, id string, want consolev1.RunStatus) *consolev1.GetRunResponse {
	t.Helper()

	var run *consolev1.GetRunResponse

	waitFor(t, "run "+id+" "+want.String(), func() bool {
		r, err := srv.workflows.GetRun(t.Context(), &consolev1.GetRunRequest{WorkflowId: id})
		run = r

		return err == nil && r.GetRun().GetStatus() == want
	})

	return run
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// playHooks plays backplane's Nexus side for svc: endpoint svc on a test
// queue serving svc.Hooks with Ask, which answers its input.
func playHooks(t *testing.T, c client.Client, svc string) {
	t.Helper()

	hooks := nexus.NewService(wire.NexusService(svc))

	ask := nexus.NewSyncOperation("Ask", func(
		_ context.Context, call *backplanev1.HookCall, _ nexus.StartOperationOptions,
	) (*backplanev1.HookResult, error) {
		return &backplanev1.HookResult{Payload: []byte(`{"answer":` + string(call.GetPayload()) + `}`)}, nil
	})
	if err := hooks.Register(ask); err != nil {
		t.Fatal(err)
	}

	queue := "nexus-" + svc
	serve(t, c, queue, func(w worker.Worker) { w.RegisterNexusService(hooks) })

	res, err := c.OperatorService().CreateNexusEndpoint(t.Context(), &operatorservice.CreateNexusEndpointRequest{
		Spec: &nexuspb.EndpointSpec{
			Name: svc,
			Target: &nexuspb.EndpointTarget{Variant: &nexuspb.EndpointTarget_Worker_{
				Worker: &nexuspb.EndpointTarget_Worker{Namespace: "default", TaskQueue: queue},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = c.OperatorService().DeleteNexusEndpoint(context.Background(), &operatorservice.DeleteNexusEndpointRequest{
			Id: res.GetEndpoint().GetId(), Version: res.GetEndpoint().GetVersion(),
		})
	})
}

// busy records more history events than GetRun returns.
func busy(ctx workflow.Context) (string, error) {
	for i := range 1100 {
		_ = workflow.SideEffect(ctx, func(workflow.Context) any { return i })
	}

	return "done", nil
}

// loop continues as new once.
func loop(ctx workflow.Context, n int) (int, error) {
	if n == 0 {
		return 0, workflow.NewContinueAsNewError(ctx, "Loop", 1)
	}

	return n, nil
}

// parent runs Greet as its child.
func parent(ctx workflow.Context) (string, error) {
	var out string

	cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowID: workflow.GetInfo(ctx).WorkflowExecution.ID + "/child"})
	err := workflow.ExecuteChildWorkflow(cctx, "Greet", greetIn{Name: "child"}).Get(ctx, &out)

	return out, err
}

// TestRunsLive: against Temporal, a running workflow id is not started
// twice, a workflow-backed activity starts with its envelope and GetRun
// shows the JSON it carries, runs are filtered and paged, hook calls are
// listed apart, long histories are truncated with the outcome kept, a
// signal without input sends no argument, child and continued runs are
// linked, and a declared schedule missing in Temporal has no state.
//
//nolint:paralleltest // one scenario over one Temporal
func TestRunsLive(t *testing.T) {
	c := liveTemporal(t)
	svc := unique("runs")
	queue := "backplane-" + svc
	ctx := t.Context()

	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{svc: service(&backplanev1.Manifest{
		Service: svc, Version: "1.0.0",
		Activities: []*backplanev1.Activity{{Name: "Flow", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW}},
		Workflows:  []*backplanev1.Workflow{{Name: "Greet"}, {Name: "Wait"}, {Name: "Busy"}, {Name: "Loop"}, {Name: "Parent"}},
		Schedules:  []*backplanev1.Schedule{{Name: "Ghost", Workflow: "Greet"}},
	})})

	o := ops.Detached(hub, ops.WithTemporal(func() (client.Client, error) { return c, nil }, queue))
	srv := newServers(o)

	serve(t, c, svc, func(w worker.Worker) {
		w.RegisterWorkflowWithOptions(flow, workflow.RegisterOptions{Name: "Flow"})
		w.RegisterWorkflowWithOptions(greet, workflow.RegisterOptions{Name: "Greet"})
		w.RegisterWorkflowWithOptions(wait, workflow.RegisterOptions{Name: "Wait"})
		w.RegisterWorkflowWithOptions(busy, workflow.RegisterOptions{Name: "Busy"})
		w.RegisterWorkflowWithOptions(loop, workflow.RegisterOptions{Name: "Loop"})
		w.RegisterWorkflowWithOptions(parent, workflow.RegisterOptions{Name: "Parent"})
	})

	// A worker polls the service's queue.
	waitFor(t, "pollers", func() bool {
		res, err := srv.workflows.ListWorkflows(ctx, &consolev1.ListWorkflowsRequest{Service: svc})

		return err == nil && len(res.GetWorkflows()) == 6 && res.GetWorkflows()[0].GetPollers() > 0
	})

	// An explicit id: refused while running, a new run once closed.
	id := svc + "/fixed"

	first, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{Service: svc, Workflow: "Wait", WorkflowId: id})
	if err != nil || first.GetWorkflowId() != id {
		t.Fatalf("start: %v %v", first, err)
	}

	if _, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{
		Service: svc, Workflow: "Wait", WorkflowId: id,
	}); code(t, err) != codes.AlreadyExists || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second start of a running id: %v", err)
	}

	// No input: the signal carries no argument, not null.
	if _, err := srv.workflows.SignalRun(ctx, &consolev1.SignalRunRequest{WorkflowId: id, Signal: "go"}); err != nil {
		t.Fatal(err)
	}

	done := waitRun(t, srv, id, consolev1.RunStatus_RUN_STATUS_COMPLETED)
	if i := slices.IndexFunc(done.GetHistory(), func(e *consolev1.HistoryEvent) bool {
		return e.GetType() == "WorkflowExecutionSignaled"
	}); i < 0 || done.GetHistory()[i].GetSummary() != "go" || done.GetHistory()[i].GetPayload() != "" || done.GetResult() != `"got "` {
		t.Fatalf("signal without input: %v", done)
	}

	again, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{Service: svc, Workflow: "Wait", WorkflowId: id})
	if err != nil || again.GetRunId() == first.GetRunId() {
		t.Fatalf("restart of a closed id: %v %v", again, err)
	}

	if _, err := srv.workflows.TerminateRun(ctx, &consolev1.TerminateRunRequest{WorkflowId: id}); err != nil {
		t.Fatal(err)
	}

	// A workflow-backed activity: started with its envelope, shown with the
	// JSON its payload carries.
	flowed, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{Service: svc, Workflow: "Flow", Input: `{"a":1}`})
	if err != nil {
		t.Fatal(err)
	}

	run := waitRun(t, srv, flowed.GetWorkflowId(), consolev1.RunStatus_RUN_STATUS_COMPLETED)
	if want := `{"activity":"` + svc + `.Flow","binding":"console","payload":{"a":1},"step":"console"}`; run.GetInput() != want ||
		run.GetResult() != `{"payload":"flowed `+svc+`.Flow"}` || run.GetHistory()[0].GetPayload() != want {
		t.Fatalf("envelopes: %s → %s (%s)", run.GetInput(), run.GetResult(), run.GetHistory()[0].GetPayload())
	}

	// Filters and pages.
	for i := range 3 {
		if _, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{
			Service: svc, Workflow: "Greet", Input: `{"name":"p"}`, WorkflowId: fmt.Sprintf("%s/page-%d", svc, i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	waitFor(t, "paged runs", func() bool {
		req := &consolev1.ListRunsRequest{
			Service: svc, Workflow: "Greet", Status: consolev1.RunStatus_RUN_STATUS_COMPLETED,
			WorkflowIdPrefix: svc + "/page-", PageSize: 2,
		}

		one, err := srv.workflows.ListRuns(ctx, req)
		if err != nil || len(one.GetRuns()) != 2 || len(one.GetNextPageToken()) == 0 {
			return false
		}

		req.PageToken = one.GetNextPageToken()

		two, err := srv.workflows.ListRuns(ctx, req)
		if err != nil || len(two.GetRuns()) != 1 || len(two.GetNextPageToken()) != 0 {
			return false
		}

		seen := map[string]bool{}
		for _, r := range append(one.GetRuns(), two.GetRuns()...) {
			seen[r.GetWorkflowId()] = r.GetWorkflowType() == "Greet" && r.GetStatus() == consolev1.RunStatus_RUN_STATUS_COMPLETED
		}

		return len(seen) == 3 && seen[svc+"/page-0"] && seen[svc+"/page-1"] && seen[svc+"/page-2"]
	})

	if res, err := srv.workflows.ListRuns(ctx, &consolev1.ListRunsRequest{
		Service: svc, Status: consolev1.RunStatus_RUN_STATUS_FAILED,
	}); err != nil || len(res.GetRuns()) != 0 {
		t.Fatalf("no failed runs: %v %v", res, err)
	}

	// Hook calls: the service's hooks queue and the console's calls on
	// backplane's queue, not its workflows.
	for _, call := range []struct{ id, queue string }{{"hook/" + svc + "/Ask/sdk", wire.HooksQueue(svc)}, {"hook/" + svc + "/Ask/console", queue}} {
		if _, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: call.id, TaskQueue: call.queue}, wire.CallHookWorkflow); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _ = c.TerminateWorkflow(context.Background(), call.id, "", "test done") })
	}

	waitFor(t, "hook calls", func() bool {
		hooks, err := srv.workflows.ListRuns(ctx, &consolev1.ListRunsRequest{Service: svc, Hooks: true})
		runs, errRuns := srv.workflows.ListRuns(ctx, &consolev1.ListRunsRequest{Service: svc})

		if err != nil || errRuns != nil || len(hooks.GetRuns()) != 2 {
			return false
		}

		for _, r := range runs.GetRuns() {
			if strings.HasPrefix(r.GetWorkflowId(), "hook/") {
				return false
			}
		}

		return true
	})

	// A long history: 1000 events, the outcome still read.
	busyRun, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{Service: svc, Workflow: "Busy"})
	if err != nil {
		t.Fatal(err)
	}

	long := waitRun(t, srv, busyRun.GetWorkflowId(), consolev1.RunStatus_RUN_STATUS_COMPLETED)
	if !long.GetTruncated() || len(long.GetHistory()) != 1000 || long.GetResult() != `"done"` {
		t.Fatalf("truncated: %v, %d events, result %s", long.GetTruncated(), len(long.GetHistory()), long.GetResult())
	}

	// Continued as new: the first run links the next.
	looped, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{Service: svc, Workflow: "Loop", Input: "0"})
	if err != nil {
		t.Fatal(err)
	}

	waitRun(t, srv, looped.GetWorkflowId(), consolev1.RunStatus_RUN_STATUS_COMPLETED)

	firstLoop, err := srv.workflows.GetRun(ctx, &consolev1.GetRunRequest{WorkflowId: looped.GetWorkflowId(), RunId: looped.GetRunId()})
	last := firstLoop.GetHistory()[len(firstLoop.GetHistory())-1]

	if err != nil || firstLoop.GetRun().GetStatus() != consolev1.RunStatus_RUN_STATUS_CONTINUED_AS_NEW ||
		firstLoop.GetContinuedRunId() == "" || last.GetRunId() != firstLoop.GetContinuedRunId() ||
		last.GetWorkflowId() != looped.GetWorkflowId() {
		t.Fatalf("continued: %v %v", firstLoop, err)
	}

	// A child: its run names the parent, the parent's history the child.
	parented, err := srv.workflows.StartWorkflow(ctx, &consolev1.StartWorkflowRequest{Service: svc, Workflow: "Parent"})
	if err != nil {
		t.Fatal(err)
	}

	parentRun := waitRun(t, srv, parented.GetWorkflowId(), consolev1.RunStatus_RUN_STATUS_COMPLETED)
	childID := parented.GetWorkflowId() + "/child"

	childRun, err := srv.workflows.GetRun(ctx, &consolev1.GetRunRequest{WorkflowId: childID})
	if err != nil || childRun.GetRun().GetParentWorkflowId() != parented.GetWorkflowId() ||
		childRun.GetRun().GetParentRunId() != parented.GetRunId() {
		t.Fatalf("child: %v %v", childRun, err)
	}

	var linked bool
	for _, e := range parentRun.GetHistory() {
		linked = linked || (e.GetType() == "ChildWorkflowExecutionCompleted" && e.GetWorkflowId() == childID &&
			e.GetRunId() == childRun.GetRun().GetRunId() && e.GetPayload() == `"Hello, child"`)
	}

	if !linked {
		t.Fatalf("parent history: %v", parentRun.GetHistory())
	}

	// Legacy declarations do not create phantom administrative schedules.
	schedules, err := srv.schedules.ListSchedules(ctx, &consolev1.ListSchedulesRequest{Service: svc})
	if err != nil || len(schedules.GetSchedules()) != 0 {
		t.Fatalf("missing schedule: %v %v", schedules, err)
	}

	if _, err := srv.schedules.TriggerSchedule(ctx, &consolev1.TriggerScheduleRequest{Service: svc, Name: "Ghost"}); code(t, err) != codes.NotFound {
		t.Fatalf("trigger of a missing schedule: %v", err)
	}
}
