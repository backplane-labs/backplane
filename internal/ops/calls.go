package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nexus-rpc/sdk-go/nexus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/redact"
	"github.com/backplane-labs/backplane/internal/wire"
)

// Workflows of the calls, on backplane's task queue. The version is in the
// name, as with backplane.CallHook.v1: a change that is not
// replay-compatible is a new name.
const (
	// CallHookWorkflow raises a hook: the Nexus operation of the hook's
	// service, input HookCall, output HookResult.
	CallHookWorkflow = "backplane.console.CallHook.v1"
	// RunActivityWorkflow executes an activity by name on its service's
	// queue: input ActivityRun, output ActivityResult.
	RunActivityWorkflow = "backplane.console.RunActivity.v1"
)

// Defaults and bounds of the calls.
const (
	defaultStartToClose = time.Minute
	// defaultCallTimeout bounds a hook call when neither the request nor
	// the hook declares a timeout.
	defaultCallTimeout = 30 * time.Second
	maxCallTimeout     = 10 * time.Minute
	// waitSlack is how long past a call's own bound the API waits for its
	// run to report.
	waitSlack = 10 * time.Second
	// retryCeil bounds Temporal's default retry interval (100 x 1s).
	retryCeil = 100 * time.Second
	// consoleBinding names the console in ActivityCall.binding and .step.
	consoleBinding = "console"
	// timedOut is the message of a call that ran out of time.
	timedOut = "timed out"
)

// ActivityRun is the input of RunActivityWorkflow.
type ActivityRun struct {
	Service      string        `json:"service"`
	Name         string        `json:"name"`
	Workflow     bool          `json:"workflow"` // kind WORKFLOW: a child workflow
	Payload      []byte        `json:"payload"`
	Step         string        `json:"step"`
	StartToClose time.Duration `json:"start_to_close"`
	Heartbeat    time.Duration `json:"heartbeat"`
	MaxAttempts  int32         `json:"max_attempts"`
}

func registerWorkflows(r worker.Registry) {
	r.RegisterWorkflowWithOptions(callHook, workflow.RegisterOptions{Name: CallHookWorkflow})
	r.RegisterWorkflowWithOptions(runActivity, workflow.RegisterOptions{Name: RunActivityWorkflow})
}

// callHook raises call.hook "<service>.<Name>": operation <Name> of Nexus
// service <service>.Hooks on endpoint <service>, schedule-to-close
// call.deadline. Failures are non-retryable application errors with the
// handler's message: NoBindingType, TimeoutType or HookFailedType.
func callHook(ctx workflow.Context, call *backplanev1.HookCall) (*backplanev1.HookResult, error) {
	service, name, ok := strings.Cut(call.GetHook(), ".")
	if !ok || service == "" || name == "" {
		return nil, temporal.NewNonRetryableApplicationError( //nolint:wrapcheck // the error Temporal carries
			"bad hook name "+call.GetHook(), wire.HookFailedType, nil)
	}

	opts := workflow.NexusOperationOptions{ScheduleToCloseTimeout: call.GetDeadline().AsDuration()}

	var res backplanev1.HookResult

	err := workflow.NewNexusClient(service, wire.NexusService(service)).
		ExecuteOperation(ctx, name, call, opts).
		Get(ctx, &res)
	if err != nil {
		return nil, hookFailure(err)
	}

	return &res, nil
}

// hookFailure is err as one application error whose message reads
// without Temporal's wrapping.
func hookFailure(err error) error {
	var (
		timeout  *temporal.TimeoutError
		canceled *temporal.CanceledError
	)

	msg, typ := readable(err)

	switch {
	case typ == wire.NoBindingType:
	case errors.As(err, &timeout):
		msg, typ = timedOut, wire.TimeoutType
	case errors.As(err, &canceled):
		msg, typ = "canceled", wire.HookFailedType
	default:
		typ = wire.HookFailedType
	}

	return temporal.NewNonRetryableApplicationError(msg, typ, nil) //nolint:wrapcheck // the error Temporal carries
}

// readable is the deepest application error message in err's chain and
// its type; else the deepest other message.
func readable(err error) (string, string) {
	var app, typ, other string

	for e := err; e != nil; e = errors.Unwrap(e) {
		switch cause := e.(type) { //nolint:errorlint // walking the chain
		case *temporal.ApplicationError:
			if cause.Message() != "" {
				app = cause.Message()
			}

			if cause.Type() != "" {
				typ = cause.Type()
			}
		case *temporal.NexusOperationError, *temporal.WorkflowExecutionError, *temporal.ActivityError,
			*temporal.ChildWorkflowExecutionError:
		case *nexus.HandlerError:
			if cause.Message != "" {
				other = cause.Message
			}
		default:
			if m := e.Error(); m != "" {
				other = m
			}
		}
	}

	switch {
	case app != "":
		return app, typ
	case other != "":
		return other, typ
	default:
		return "failed", typ
	}
}

// runActivity executes in.Name on in.Service's queue with the ActivityCall
// envelope: an activity, or a child workflow for kind WORKFLOW.
func runActivity(ctx workflow.Context, in ActivityRun) (*backplanev1.ActivityResult, error) {
	call := &backplanev1.ActivityCall{
		Activity: in.Service + "." + in.Name, Payload: in.Payload, Binding: consoleBinding, Step: in.Step,
	}
	retry := &temporal.RetryPolicy{MaximumAttempts: max(in.MaxAttempts, 1)}

	var (
		res backplanev1.ActivityResult
		err error
	)

	if in.Workflow {
		cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			TaskQueue: wire.Queue(in.Service), WorkflowRunTimeout: in.StartToClose, RetryPolicy: retry,
		})
		err = workflow.ExecuteChildWorkflow(cctx, in.Name, call).Get(ctx, &res)
	} else {
		actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			TaskQueue: wire.Queue(in.Service), StartToCloseTimeout: in.StartToClose,
			HeartbeatTimeout: in.Heartbeat, RetryPolicy: retry,
		})
		err = workflow.ExecuteActivity(actx, in.Name, call).Get(ctx, &res)
	}

	if err != nil {
		msg, typ := readable(err)

		return nil, temporal.NewNonRetryableApplicationError(msg, typ, nil) //nolint:wrapcheck // the error Temporal carries
	}

	return &res, nil
}

// CallAPI is backplane.console.v1.CallService.
type CallAPI struct {
	consolev1.UnimplementedCallServiceServer

	o *Ops
}

var _ consolev1.CallServiceServer = CallAPI{}

// jsonInput is the JSON input of a call; empty is {}.
func jsonInput(s string) ([]byte, error) {
	if s == "" {
		return []byte("{}"), nil
	}

	if !json.Valid([]byte(s)) {
		return nil, fmt.Errorf("%w: input is not valid JSON", ErrInput)
	}

	return []byte(s), nil
}

// HookWorkflowID is the run of a console hook call: with a key the same
// id as the service's own call with hook.Key(key), else a fresh one.
func HookWorkflowID(service, name, key string) string {
	if key == "" {
		key = "console-" + uuid.NewString()
	}

	return hookRunPrefix(service) + name + "/" + key
}

// hookRunPrefix starts the ids of the hook calls of service's hooks:
// hook/<service>/<Name>/<key> (the SDK's and the console's alike).
func hookRunPrefix(service string) string { return "hook/" + service + "/" }

// CallHook implements CallService.
func (a CallAPI) CallHook(ctx context.Context, req *consolev1.CallHookRequest) (*consolev1.CallHookResponse, error) {
	opts, call, err := a.hookCall(ctx, req)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	c, err := a.o.client()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	service, _, _ := strings.Cut(req.GetHook(), ".")
	if err := endpoint(ctx, c, service); err != nil {
		return nil, a.o.status(ctx, err)
	}

	var res backplanev1.HookResult

	out, err := a.o.run(ctx, c, opts, CallHookWorkflow, call, &res, opts.WorkflowExecutionTimeout+waitSlack)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	if out.GetError() == "" {
		h, _ := a.o.redactor().Hook(req.GetHook())
		setOutput(out, redact.Mask(h.GetOutput(), res.GetPayload()))
	}

	a.o.audit(ctx, "hook.call", req.GetHook(), xlog.String("workflow_id", out.GetWorkflowId()),
		xlog.String("error", out.GetError()))

	return &consolev1.CallHookResponse{Result: out}, nil
}

// hookCall is the run CallHook starts: its options and the envelope.
func (a CallAPI) hookCall(
	ctx context.Context, req *consolev1.CallHookRequest,
) (client.StartWorkflowOptions, *backplanev1.HookCall, error) {
	var opts client.StartWorkflowOptions

	service, name, err := splitFull("hook", req.GetHook())
	if err != nil {
		return opts, nil, err
	}

	m, err := a.o.latest(service)
	if err != nil {
		return opts, nil, err
	}

	i := slices.IndexFunc(m.GetHooks(), func(h *backplanev1.Hook) bool { return h.GetName() == name })
	if i < 0 {
		return opts, nil, fmt.Errorf("%w: %s declares no hook %s", ErrNotDeclared, service, name)
	}

	payload, err := jsonInput(req.GetInput())
	if err != nil {
		return opts, nil, err
	}

	timeout := req.GetTimeout().AsDuration()
	if timeout <= 0 {
		timeout = m.GetHooks()[i].GetTimeout().AsDuration()
	}

	if timeout <= 0 {
		timeout = defaultCallTimeout
	}

	timeout = min(timeout, maxCallTimeout)

	author := a.o.author(ctx)
	opts = client.StartWorkflowOptions{
		ID: HookWorkflowID(service, name, req.GetKey()), TaskQueue: a.o.queue,
		WorkflowExecutionTimeout: timeout, Memo: map[string]any{wire.MemoSource: author},
	}

	if req.GetKey() != "" {
		opts.WorkflowIDConflictPolicy = enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING
		opts.WorkflowIDReusePolicy = enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY
	}

	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	return opts, &backplanev1.HookCall{
		Hook: req.GetHook(), Instance: author, Payload: payload, Trace: carrier, Deadline: durationpb.New(timeout),
	}, nil
}

// endpoint fails when Temporal has no Nexus endpoint for service: a call
// would wait out its deadline. A server that refuses the check lets the
// call go.
func endpoint(ctx context.Context, c client.Client, service string) error {
	res, err := c.OperatorService().ListNexusEndpoints(ctx, &operatorservice.ListNexusEndpointsRequest{
		Name: service, PageSize: 1,
	})
	if err == nil && len(res.GetEndpoints()) == 0 {
		return fmt.Errorf("%w: no Nexus endpoint %q: backplane has not registered the service's hooks",
			ErrPrecondition, service)
	}

	return nil
}

// RunActivity implements CallService.
func (a CallAPI) RunActivity(
	ctx context.Context, req *consolev1.RunActivityRequest,
) (*consolev1.RunActivityResponse, error) {
	in, err := a.activityRun(req)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	c, err := a.o.client()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	bound := time.Duration(max(in.MaxAttempts, 1))*(in.StartToClose+retryCeil) + time.Minute
	opts := client.StartWorkflowOptions{
		ID:        "console/activity/" + in.Service + "/" + in.Name + "/" + uuid.NewString(),
		TaskQueue: a.o.queue, WorkflowExecutionTimeout: bound,
		Memo: map[string]any{wire.MemoSource: a.o.author(ctx)},
	}

	var res backplanev1.ActivityResult

	out, err := a.o.run(ctx, c, opts, RunActivityWorkflow, in, &res, bound+waitSlack)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	if out.GetError() == "" {
		act, _ := a.o.redactor().Activity(req.GetActivity())
		setOutput(out, redact.Mask(act.GetOutput(), res.GetPayload()))
	}

	a.o.audit(ctx, "activity.run", req.GetActivity(), xlog.String("workflow_id", out.GetWorkflowId()),
		xlog.String("error", out.GetError()))

	return &consolev1.RunActivityResponse{Result: out}, nil
}

// activityRun is the input of RunActivityWorkflow for req, with the
// declared defaults.
func (a CallAPI) activityRun(req *consolev1.RunActivityRequest) (ActivityRun, error) {
	service, name, err := splitFull("activity", req.GetActivity())
	if err != nil {
		return ActivityRun{}, err
	}

	m, err := a.o.latest(service)
	if err != nil {
		return ActivityRun{}, err
	}

	i := slices.IndexFunc(m.GetActivities(), func(x *backplanev1.Activity) bool { return x.GetName() == name })
	if i < 0 {
		return ActivityRun{}, fmt.Errorf("%w: %s declares no activity %s", ErrNotDeclared, service, name)
	}

	decl := m.GetActivities()[i]

	payload, err := jsonInput(req.GetInput())
	if err != nil {
		return ActivityRun{}, err
	}

	if req.GetMaxAttempts() < 0 {
		return ActivityRun{}, fmt.Errorf("%w: max_attempts must not be negative", ErrInput)
	}

	in := ActivityRun{
		Service: service, Name: name, Workflow: decl.GetKind() == backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW,
		Payload: payload, Step: req.GetStep(), StartToClose: req.GetStartToClose().AsDuration(),
		Heartbeat: decl.GetHeartbeat().AsDuration(), MaxAttempts: max(req.GetMaxAttempts(), 1),
	}

	if in.Step == "" {
		in.Step = consoleBinding
	}

	if in.StartToClose <= 0 {
		in.StartToClose = decl.GetStartToClose().AsDuration()
	}

	if in.StartToClose <= 0 {
		in.StartToClose = defaultStartToClose
	}

	in.StartToClose = min(in.StartToClose, maxCallTimeout)

	return in, nil
}

// run starts a call workflow and waits up to wait for it: the result is
// decoded into res; a failed call is a result with its error, not an error
// of run.
func (o *Ops) run(
	ctx context.Context, c client.Client, opts client.StartWorkflowOptions,
	workflowName string, arg, res any, wait time.Duration,
) (*consolev1.CallResult, error) {
	start := time.Now()

	r, err := c.ExecuteWorkflow(ctx, opts, workflowName, arg)
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", workflowName, err)
	}

	out := &consolev1.CallResult{WorkflowId: r.GetID(), RunId: r.GetRunID()}

	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	err = r.Get(wctx, res)
	out.Took = durationpb.New(time.Since(start))

	switch {
	case err == nil:
	case wctx.Err() != nil:
		return nil, status.Errorf(codes.DeadlineExceeded,
			"run %s is still going: follow it in WorkflowService.GetRun", r.GetID())
	default:
		out.Error, out.ErrorType = readable(err)

		var timeout *temporal.TimeoutError
		if out.GetErrorType() == "" && errors.As(err, &timeout) {
			out.Error, out.ErrorType = timedOut, wire.TimeoutType
		}
	}

	return out, nil
}

// setOutput is the payload of a successful call.
func setOutput(out *consolev1.CallResult, payload []byte) {
	if json.Valid(payload) {
		out.Output = string(payload)
	} else {
		out.Raw = payload
	}
}
