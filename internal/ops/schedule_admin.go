package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	schedulepb "go.temporal.io/api/schedule/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/wire"
)

const (
	scheduleUpdateWait = 5 * time.Second
	scheduleUpdatePoll = 50 * time.Millisecond
)

func scheduleID(service, name string) (string, error) {
	if !serviceName.MatchString(service) || name == "" || len(name) > 128 || strings.ContainsAny(name, "/\x00\r\n") {
		return "", fmt.Errorf("%w: valid service and schedule name are required", ErrInput)
	}

	return wire.ScheduleID(service, name), nil
}

// GetSchedule reads from Temporal without requiring a currently running service.
func (a ScheduleAPI) GetSchedule(ctx context.Context,
	req *consolev1.GetScheduleRequest) (*consolev1.GetScheduleResponse,
	error,
) {
	id, err := scheduleID(req.GetService(), req.GetName())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	c, err := a.o.client()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	desc,
		err := a.describeSchedule(ctx, c.WorkflowService(), id)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	native, err := c.ScheduleClient().GetHandle(ctx, id).Describe(ctx)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	action := desc.GetSchedule().GetAction().GetStartWorkflow()

	timing, err := protojson.Marshal(desc.GetSchedule().GetSpec())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	input := ""
	if args := action.GetInput().GetPayloads(); len(args) > 0 {
		input = string(unwrap([]byte(payloadJSON(args[0]))))
	}

	return &consolev1.GetScheduleResponse{
		Schedule: &consolev1.ScheduleInfo{
			Service:   req.GetService(),
			Name:      req.GetName(),
			Id:        id,
			State:     scheduleStatePB(native),
			ManagedBy: managedBy(desc.GetMemo()),
		},
		Definition: &consolev1.ScheduleDefinition{
			Workflow:         action.GetWorkflowType().GetName(),
			Input:            input,
			ExecutionTimeout: action.GetWorkflowExecutionTimeout(),
		},
		Revision: desc.GetConflictToken(), TimingJson: string(timing),
	}, nil
}

// CreateSchedule uses a declared workflow and the platform's queue convention.
func (a ScheduleAPI) CreateSchedule(ctx context.Context,
	req *consolev1.CreateScheduleRequest) (*consolev1.CreateScheduleResponse,
	error,
) {
	id, err := scheduleID(req.GetService(), req.GetName())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	action, err := a.scheduleAction(ctx, req.GetService(), id, req.GetDefinition())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	spec, err := scheduleTiming(req.GetDefinition().GetTiming())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	c, err := a.o.client()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	owner, _ := converter.GetDefaultDataConverter().ToPayload(req.GetService())

	_, err = c.WorkflowService().CreateSchedule(ctx, &workflowservice.CreateScheduleRequest{
		Namespace: a.o.namespace, ScheduleId: id, Identity: a.o.author(ctx), RequestId: uuid.NewString(),
		Memo: &commonpb.Memo{Fields: map[string]*commonpb.Payload{wire.MemoService: owner}},
		Schedule: &schedulepb.Schedule{
			Spec: spec, Action: action, State: &schedulepb.ScheduleState{Paused: req.GetPaused()},
			Policies: &schedulepb.SchedulePolicies{OverlapPolicy: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP},
		},
	})
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	return &consolev1.CreateScheduleResponse{}, nil
}

// UpdateSchedule preserves state and advanced action settings. Temporal checks
// the conflict token atomically, across all platform replicas and external tools.
//
//nolint:wrapcheck // gRPC status is the public API error
func (a ScheduleAPI) UpdateSchedule(ctx context.Context,
	req *consolev1.UpdateScheduleRequest) (*consolev1.UpdateScheduleResponse,
	error,
) {
	id, err := scheduleID(req.GetService(), req.GetName())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	if len(req.GetRevision()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "revision is required")
	}

	replacement, err := a.scheduleAction(ctx, req.GetService(), id, req.GetDefinition())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	c, err := a.o.client()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	desc,
		err := a.describeSchedule(ctx, c.WorkflowService(), id)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	if !bytes.Equal(req.GetRevision(), desc.GetConflictToken()) {
		return nil, status.Error(codes.Aborted, "schedule changed; reload before editing")
	}

	if file := managedBy(desc.GetMemo()); file != "" {
		return nil, a.o.status(ctx, fmt.Errorf("%w %s: edit the file", ErrManaged, file))
	}

	schedule := proto.CloneOf(desc.GetSchedule())

	requestID, err := patchScheduleAction(schedule, replacement)
	if err != nil {
		return nil, err
	}

	if timing := req.GetDefinition().GetTiming(); timing != nil {
		schedule.Spec, err = scheduleTiming(timing)
		if err != nil {
			return nil, a.o.status(ctx, err)
		}
	}

	_, err = c.WorkflowService().UpdateSchedule(ctx, &workflowservice.UpdateScheduleRequest{
		Namespace:     a.o.namespace,
		ScheduleId:    id,
		Schedule:      schedule,
		ConflictToken: req.GetRevision(),
		Identity:      a.o.author(ctx),
		RequestId:     requestID,
	})
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	if err := a.confirmScheduleUpdate(ctx, c.WorkflowService(), id, req.GetRevision(), requestID); err != nil {
		return nil, err
	}

	return &consolev1.UpdateScheduleResponse{}, nil
}

// DeleteSchedule does not cancel running workflow executions.
func (a ScheduleAPI) DeleteSchedule(ctx context.Context,
	req *consolev1.DeleteScheduleRequest) (*consolev1.DeleteScheduleResponse,
	error,
) {
	id, err := scheduleID(req.GetService(), req.GetName())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	c, err := a.o.client()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	desc, err := a.describeSchedule(ctx, c.WorkflowService(), id)
	if err != nil {
		return nil, err
	}

	if file := managedBy(desc.GetMemo()); file != "" {
		return nil, a.o.status(ctx, fmt.Errorf("%w %s: remove it from the file", ErrManaged, file))
	}

	if err = c.ScheduleClient().GetHandle(ctx, id).Delete(ctx); err != nil {
		return nil, a.o.status(ctx, err)
	}

	return &consolev1.DeleteScheduleResponse{}, nil
}

func scheduleTiming(timing *consolev1.ScheduleTiming) (*schedulepb.ScheduleSpec, error) {
	if timing == nil || (len(timing.GetCron()) == 0 && timing.GetInterval() == nil) {
		return nil, fmt.Errorf("%w: cron or interval is required", ErrInput)
	}

	spec := &schedulepb.ScheduleSpec{CronString: timing.GetCron(), TimezoneName: timing.GetTimezone()}
	if d := timing.GetInterval(); d != nil {
		if d.CheckValid() != nil || d.AsDuration() <= 0 {
			return nil, fmt.Errorf("%w: interval must be positive", ErrInput)
		}

		spec.Interval = []*schedulepb.IntervalSpec{{Interval: d}}
	}

	return spec, nil
}

func (a ScheduleAPI) scheduleAction(ctx context.Context,
	service,
	id string,
	def *consolev1.ScheduleDefinition) (*schedulepb.ScheduleAction,
	error,
) {
	if def == nil {
		return nil, fmt.Errorf("%w: definition is required", ErrInput)
	}

	input, err := a.scheduleInput(service, def)
	if err != nil {
		return nil, err
	}

	opts,
		args,
		err := a.o.Workflows().start(ctx,
		&consolev1.StartWorkflowRequest{
			Service:    service,
			Workflow:   def.GetWorkflow(),
			Input:      input,
			WorkflowId: id,
			Timeout:    def.GetExecutionTimeout(),
		})
	if err != nil {
		return nil, err
	}

	payloads, err := converter.GetDefaultDataConverter().ToPayloads(args...)
	if err != nil {
		return nil, fmt.Errorf("%w: encode schedule input", ErrInput)
	}

	// The scheduled run's input is history like any other: sealed when
	// the installation encrypts it.
	sealed, err := a.o.codec().EncodeContext(ctx, payloads.GetPayloads())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	payloads = &commonpb.Payloads{Payloads: sealed}

	return &schedulepb.ScheduleAction{Action: &schedulepb.ScheduleAction_StartWorkflow{
		StartWorkflow: &workflowpb.NewWorkflowExecutionInfo{
			WorkflowId:               id,
			WorkflowType:             &commonpb.WorkflowType{Name: def.GetWorkflow()},
			TaskQueue:                &taskqueuepb.TaskQueue{Name: opts.TaskQueue},
			Input:                    payloads,
			WorkflowExecutionTimeout: def.GetExecutionTimeout(),
		},
	}}, nil
}

func (a ScheduleAPI) scheduleInput(service string, def *consolev1.ScheduleDefinition) (string, error) {
	if d := def.GetExecutionTimeout(); d != nil && (d.CheckValid() != nil || d.AsDuration() < 0) {
		return "", fmt.Errorf("%w: invalid execution timeout", ErrInput)
	}

	m, err := a.o.latest(service)
	if err != nil {
		return "", err
	}

	input := def.GetInput()
	if strings.TrimSpace(input) == "" {
		input = "{}"
	}

	if len(input) > 1<<20 || !json.Valid([]byte(input)) {
		return "", fmt.Errorf("%w: invalid or oversized JSON input", ErrInput)
	}

	for _, d := range workflowDefs([]*backplanev1.Manifest{m}) {
		if d.GetName() != def.GetWorkflow() {
			continue
		}

		if d.GetInput() == nil {
			return input, nil
		}

		return validatedScheduleInput(d, input)
	}

	return "", fmt.Errorf("%w: workflow is not declared", ErrNotDeclared)
}

// Legacy Temporal servers acknowledge a signal before applying the update.
// Confirm our own marker, not merely a changed revision or identical payload.
//
//nolint:wrapcheck // gRPC status is the public API error
func (a ScheduleAPI) confirmScheduleUpdate(ctx context.Context,
	api workflowservice.WorkflowServiceClient,
	id string,
	previous []byte,
	marker string,
) error {
	ctx, cancel := context.WithTimeout(ctx, scheduleUpdateWait)
	defer cancel()

	ticker := time.NewTicker(scheduleUpdatePoll)
	defer ticker.Stop()

	for {
		desc,
			err := a.describeSchedule(ctx, api, id)
		if err != nil {
			return a.o.status(ctx, err)
		}

		if !bytes.Equal(previous, desc.GetConflictToken()) {
			var applied string

			payload := desc.GetSchedule().GetAction().GetStartWorkflow().GetMemo().GetFields()["backplane.schedule.update"]
			if payload != nil && converter.GetDefaultDataConverter().FromPayload(payload, &applied) == nil && applied == marker {
				return nil
			}

			return status.Error(codes.Aborted, "schedule changed concurrently; reload before editing")
		}

		select {
		case <-ctx.Done():
			return status.Error(codes.DeadlineExceeded, "schedule update outcome is unknown; reload before retrying")
		case <-ticker.C:
		}
	}
}

func validatedScheduleInput(d *consolev1.WorkflowDef, input string) (string, error) {
	var values map[string]any

	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.UseNumber()

	if err := decoder.Decode(&values); err != nil || values == nil {
		return "", fmt.Errorf("%w: input must be an object", ErrInput)
	}

	result, err := d.GetInput().Validate(values)
	if err != nil {
		return "", fmt.Errorf("%w: invalid workflow input schema", ErrPrecondition)
	}

	if result.Blocking() {
		return "", fmt.Errorf("%w: input does not satisfy workflow schema", ErrInput)
	}

	encoded, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("%w: resolved input is invalid", ErrInput)
	}

	return string(encoded), nil
}

//nolint:wrapcheck // gRPC status is the public API error
func patchScheduleAction(schedule *schedulepb.Schedule, replacement *schedulepb.ScheduleAction) (string, error) {
	old := schedule.GetAction().GetStartWorkflow()
	if old == nil {
		return "", status.Error(codes.FailedPrecondition, "schedule has no workflow action")
	}

	newAction := replacement.GetStartWorkflow()
	requestID := uuid.NewString()
	marker, _ := converter.GetDefaultDataConverter().ToPayload(requestID)

	if old.GetMemo() == nil {
		old.Memo = &commonpb.Memo{}
	}

	if old.GetMemo().GetFields() == nil {
		old.Memo.Fields = map[string]*commonpb.Payload{}
	}

	old.Memo.Fields["backplane.schedule.update"] = marker
	old.WorkflowType = newAction.GetWorkflowType()
	old.TaskQueue = newAction.GetTaskQueue()
	old.Input = newAction.GetInput()
	old.WorkflowExecutionTimeout = newAction.GetWorkflowExecutionTimeout()

	return requestID, nil
}

// Reading a schedule immediately after an update may temporarily exhaust
// Temporal's consistent-query buffer. Retry only the read, never the mutation.
func (a ScheduleAPI) describeSchedule(
	ctx context.Context,
	api workflowservice.WorkflowServiceClient,
	id string,
) (*workflowservice.DescribeScheduleResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, scheduleUpdateWait)
	defer cancel()

	ticker := time.NewTicker(scheduleUpdatePoll)
	defer ticker.Stop()

	for {
		result, err := api.DescribeSchedule(ctx, &workflowservice.DescribeScheduleRequest{
			Namespace: a.o.namespace, ScheduleId: id,
		})
		if err == nil {
			return result, a.o.decode(ctx, result)
		}

		mapped := a.o.status(ctx, err)
		if status.Code(mapped) != codes.ResourceExhausted && status.Code(mapped) != codes.Unavailable {
			return nil, mapped
		}

		select {
		case <-ctx.Done():
			return nil, a.o.status(ctx, ctx.Err())
		case <-ticker.C:
		}
	}
}
