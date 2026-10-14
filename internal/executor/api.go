package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/propagation"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/redact"
	"github.com/backplane-labs/backplane/internal/wire"
)

// Bounds of the console's runs.
const (
	defaultTestTimeout = 30 * time.Second
	maxTestTimeout     = 10 * time.Minute
	// waitSlack is how long past a test's own timeout the call waits for
	// the run to report.
	waitSlack = 10 * time.Second
	// maxEvents bounds the history read for a timeline.
	maxEvents = 10000
	// maxVersion bounds a requested version so it converts to int64.
	maxVersion = 1 << 62
)

// BindingAPI is BindingService: the bindings Manager's RPCs with the run
// RPCs of the executor.
type BindingAPI struct {
	consolev1.BindingServiceServer

	x *Executor
}

var _ consolev1.BindingServiceServer = BindingAPI{}

// BindingService is base (bindings.Manager's BindingAPI) with the run
// RPCs.
func (x *Executor) BindingService(base consolev1.BindingServiceServer) BindingAPI {
	return BindingAPI{BindingServiceServer: base, x: x}
}

// Register registers BindingService — base with the run RPCs — on r.
func (x *Executor) Register(base consolev1.BindingServiceServer) func(r grpc.ServiceRegistrar) {
	return func(r grpc.ServiceRegistrar) { consolev1.RegisterBindingServiceServer(r, x.BindingService(base)) }
}

func invalid(format string, args ...any) error {
	return status.Errorf(codes.InvalidArgument, format, args...)
}

// status maps errors to gRPC codes; gRPC statuses pass; unexpected ones
// are logged.
func (x *Executor) status(ctx context.Context, err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}

	code := codes.Internal

	switch {
	case errors.Is(err, ErrNotSynced), errors.Is(err, ErrUnavailable), errors.Is(err, bindings.ErrNotSynced):
		code = codes.Unavailable
	case errors.Is(err, bindings.ErrNoBinding), errors.Is(err, bindings.ErrNoVersion):
		code = codes.NotFound
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	default:
		x.Log().Ctx().Error(ctx, "executor api", xlog.Err(err))
	}

	return status.Error(code, err.Error()) //nolint:wrapcheck // a gRPC status travels as is
}

// TestBinding implements BindingService.
func (a BindingAPI) TestBinding(
	ctx context.Context, req *consolev1.TestBindingRequest,
) (*consolev1.TestBindingResponse, error) {
	x := a.x

	b, version, err := x.testBinding(ctx, req)
	if err != nil {
		return nil, x.status(ctx, err)
	}

	cat := x.src.Current()
	if cat.Index == 0 {
		return nil, x.status(ctx, ErrNotSynced)
	}

	prog, err := bindings.CompileBinding(b, bindings.FromRegistry(cat))
	if err != nil {
		var bad *bindings.InvalidError
		if errors.As(err, &bad) {
			return &consolev1.TestBindingResponse{Violations: bindings.ViolationsPB(bad.Violations), Version: uint64(version)}, nil //nolint:gosec,lll // positive
		}

		return nil, x.status(ctx, err)
	}

	input := []byte(req.GetInput())
	if len(input) == 0 {
		input = []byte("{}")
	}

	if !json.Valid(input) {
		return nil, invalid("input is not valid JSON")
	}

	timeout := req.GetTimeout().AsDuration()
	if timeout <= 0 {
		timeout = defaultTestTimeout
	}

	timeout = min(timeout, maxTestTimeout)

	carrier := propagation.MapCarrier{}
	w3c{}.Inject(ctx, carrier)

	id := Identity{Hook: b.Hook, Version: version, Test: true}

	in, err := BindingInput(prog, id, input, carrier)
	if err != nil {
		return nil, x.status(ctx, err)
	}

	c, err := x.client()
	if err != nil {
		return nil, x.status(ctx, err)
	}

	opts := client.StartWorkflowOptions{
		ID: wire.TestRunID(b.Hook, uuid.NewString()), TaskQueue: x.queue, WorkflowExecutionTimeout: timeout,
		Memo: map[string]any{wire.MemoSource: x.author(ctx), wire.MemoBinding: id.String()},
	}

	res, err := x.execute(ctx, c, opts, in, timeout+waitSlack)
	if err != nil {
		return nil, x.status(ctx, err)
	}

	maskResult(res, redact.ParseProgram(in.Program))

	x.audit(ctx, "binding.test", b.Hook, xlog.String("workflow_id", res.GetWorkflowId()),
		xlog.Int64("version", version), xlog.String("error", res.GetError()))

	return &consolev1.TestBindingResponse{Result: res, Version: uint64(version)}, nil //nolint:gosec // positive
}

// testBinding is what TestBinding runs: an unsaved definition (version
// 0), or a saved version.
func (x *Executor) testBinding(
	ctx context.Context, req *consolev1.TestBindingRequest,
) (bindings.Binding, int64, error) {
	hook, def := req.GetHook(), req.GetDefinition()

	if def != nil {
		if req.GetVersion() != 0 {
			return bindings.Binding{}, 0, invalid("version and definition are exclusive")
		}

		switch {
		case hook == "":
			hook = def.GetHook()
		case def.GetHook() != "" && def.GetHook() != hook:
			return bindings.Binding{}, 0, invalid("the definition is of %s, not %s", def.GetHook(), hook)
		}

		b := bindings.BindingFromPB(def)
		b.Hook = hook

		return b, 0, nil
	}

	if !bindings.IsFullName(hook) {
		return bindings.Binding{}, 0, invalid("hook must be <service>.<Hook>, got %q", hook)
	}

	v, err := x.bindings.Version(ctx, hook, int64(min(req.GetVersion(), maxVersion))) //nolint:gosec // bounded
	if err != nil {
		return bindings.Binding{}, 0, err //nolint:wrapcheck // mapped by status
	}

	if v.Deleted {
		return bindings.Binding{}, 0, status.Errorf(codes.FailedPrecondition,
			"%s: version %d is a delete: no binding to test", hook, v.Version)
	}

	return v.Binding, v.Version, nil
}

// execute starts a run and waits up to wait for it: a failed run is a
// result with its error, not an error of execute.
func (x *Executor) execute(
	ctx context.Context, c client.Client, opts client.StartWorkflowOptions, in Input, wait time.Duration,
) (*consolev1.CallResult, error) {
	start := time.Now()

	run, err := c.ExecuteWorkflow(ctx, opts, wire.BindingWorkflow, in)
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", wire.BindingWorkflow, err)
	}

	out := &consolev1.CallResult{WorkflowId: run.GetID(), RunId: run.GetRunID()}

	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	var res backplanev1.HookResult

	err = run.Get(wctx, &res)
	out.Took = durationpb.New(time.Since(start))

	switch {
	case err == nil:
		if payload := res.GetPayload(); json.Valid(payload) {
			out.Output = string(payload)
		} else {
			out.Raw = payload
		}
	case wctx.Err() != nil && ctx.Err() == nil:
		return nil, status.Errorf(codes.DeadlineExceeded,
			"run %s is still going: follow it in GetBindingRun", run.GetID())
	default:
		out.Error, out.ErrorType = readable(err)

		var timeout *temporal.TimeoutError
		if errors.As(err, &timeout) && out.GetErrorType() == "" {
			out.Error, out.ErrorType = "timed out", wire.TimeoutType
		}
	}

	return out, nil
}

// maskResult hides the secrets of a test run's answer by its program.
func maskResult(res *consolev1.CallResult, prog *redact.Program) {
	if res.GetOutput() != "" {
		res.Output = string(prog.MaskResult([]byte(res.GetOutput())))
	}
}

// runsAPI is the runs service or UNAVAILABLE.
func (x *Executor) runsAPI() (consolev1.WorkflowServiceServer, error) {
	if x.runs == nil {
		return nil, status.Error(codes.Unavailable, "executor: runs are not served") //nolint:wrapcheck // a gRPC status
	}

	return x.runs, nil
}

// ListBindingRuns implements BindingService.
func (a BindingAPI) ListBindingRuns(
	ctx context.Context, req *consolev1.ListBindingRunsRequest,
) (*consolev1.ListBindingRunsResponse, error) {
	if !bindings.IsFullName(req.GetHook()) {
		return nil, invalid("hook must be <service>.<Hook>, got %q", req.GetHook())
	}

	runs, err := a.x.runsAPI()
	if err != nil {
		return nil, err
	}

	prefix := wire.BindingRunPrefix + req.GetHook() + "/"
	if req.GetTests() {
		prefix = wire.TestRunPrefix + req.GetHook() + "/"
	}

	res, err := runs.ListRuns(ctx, &consolev1.ListRunsRequest{
		Service: a.x.queue, Workflow: wire.BindingWorkflow, Status: req.GetStatus(), WorkflowIdPrefix: prefix,
		PageSize: req.GetPageSize(), PageToken: req.GetPageToken(),
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // a gRPC status from ops
	}

	return &consolev1.ListBindingRunsResponse{
		Runs: res.GetRuns(), NextPageToken: res.GetNextPageToken(), Query: res.GetQuery(),
	}, nil
}

// runID checks that id is a run of a binding.
func runID(id string) error {
	if _, _, ok := wire.BindingRunHook(id); !ok {
		return invalid("%q is not a binding run: binding/<hook>/... or test/<hook>/...", id)
	}

	return nil
}

// GetBindingRun implements BindingService.
func (a BindingAPI) GetBindingRun(
	ctx context.Context, req *consolev1.GetBindingRunRequest,
) (*consolev1.GetBindingRunResponse, error) {
	x := a.x

	if err := runID(req.GetWorkflowId()); err != nil {
		return nil, err
	}

	runs, err := x.runsAPI()
	if err != nil {
		return nil, err
	}

	run, err := runs.GetRun(ctx, &consolev1.GetRunRequest{WorkflowId: req.GetWorkflowId(), RunId: req.GetRunId()})
	if err != nil {
		return nil, err //nolint:wrapcheck // a gRPC status from ops
	}

	if t := run.GetRun().GetWorkflowType(); t != wire.BindingWorkflow {
		return nil, status.Errorf(codes.NotFound, "%s is a %s run, not a binding run",
			req.GetWorkflowId(), t)
	}

	c, err := x.client()
	if err != nil {
		return nil, x.status(ctx, err)
	}

	steps, id, err := Timeline(ctx, c, run)
	if err != nil {
		return nil, x.status(ctx, err)
	}

	hook, test, _ := wire.BindingRunHook(req.GetWorkflowId())
	if id.Hook != "" {
		hook = id.Hook
	}

	return &consolev1.GetBindingRunResponse{
		Run: run, Hook: hook, Version: uint64(max(id.Version, 0)), Test: test || id.Test, Steps: steps,
	}, nil
}

// Timeline is the steps of a run of wire.BindingWorkflow — a binding's or
// a rule's — and what ran: every step of the program in its input by
// name, its call from the run's history (c reads it) or NOT_RUN, then the
// compensations in the order they were scheduled.
func Timeline(
	ctx context.Context, c client.Client, run *consolev1.GetRunResponse,
) ([]*consolev1.StepRun, Identity, error) {
	var in Input

	_ = json.Unmarshal([]byte(run.GetInput()), &in)

	events, err := history(ctx, c, run.GetRun().GetWorkflowId(), run.GetRun().GetRunId())
	if err != nil {
		return nil, in.Identity, err
	}

	return timeline(in.Program, events, run.GetPendingActivities()), in.Identity, nil
}

// history is a run's events, at most maxEvents.
func history(ctx context.Context, c client.Client, workflowID, runID string) ([]*historypb.HistoryEvent, error) {
	iter := c.GetWorkflowHistory(ctx, workflowID, runID, false, historyFilter)

	var out []*historypb.HistoryEvent

	for iter.HasNext() && len(out) < maxEvents {
		e, err := iter.Next()
		if err != nil {
			return nil, fmt.Errorf("history of %s: %w", workflowID, err)
		}

		out = append(out, e)
	}

	return out, nil
}

// CancelBindingRun implements BindingService.
func (a BindingAPI) CancelBindingRun(
	ctx context.Context, req *consolev1.CancelBindingRunRequest,
) (*consolev1.CancelBindingRunResponse, error) {
	if err := runID(req.GetWorkflowId()); err != nil {
		return nil, err
	}

	runs, err := a.x.runsAPI()
	if err != nil {
		return nil, err
	}

	if _, err := runs.CancelRun(ctx, &consolev1.CancelRunRequest{
		WorkflowId: req.GetWorkflowId(), RunId: req.GetRunId(),
	}); err != nil {
		return nil, err //nolint:wrapcheck // a gRPC status from ops
	}

	a.x.audit(ctx, "binding.run.cancel", req.GetWorkflowId(), xlog.String("run_id", req.GetRunId()))

	return &consolev1.CancelBindingRunResponse{}, nil
}
