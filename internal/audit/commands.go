package audit

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/internal/wire"
)

const (
	resultDeadline   = 5 * time.Second
	outcomeSucceeded = "succeeded"
	outcomeFailed    = "failed"
	// lookupDeadline bounds asking Temporal for a run's task queue.
	lookupDeadline = 2 * time.Second
	// maxWords bounds the operator's words kept in a detail (runes).
	maxWords = 256
)

// Commands wraps only the enumerated external-control RPCs. Database mutations
// append their audit inside the actual mutation transaction instead.
func (s *Service) Commands(
	register func(grpc.ServiceRegistrar), author func(context.Context) string,
) func(grpc.ServiceRegistrar) {
	return func(next grpc.ServiceRegistrar) { register(commandRegistrar{next: next, service: s, author: author}) }
}

type commandRegistrar struct {
	next    grpc.ServiceRegistrar
	service *Service
	author  func(context.Context) string
}

func (r commandRegistrar) RegisterService(desc *grpc.ServiceDesc, impl any) {
	wrapped := *desc

	wrapped.Methods = append([]grpc.MethodDesc(nil), desc.Methods...)
	for i, method := range wrapped.Methods {
		action := commandAction("/" + desc.ServiceName + "/" + method.MethodName)
		if action == "" {
			continue
		}

		wrapped.Methods[i].Handler = func(
			server any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor,
		) (any, error) {
			return method.Handler(server, ctx, decode, func(
				ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler,
			) (any, error) {
				audited := func(ctx context.Context, req any) (any, error) {
					return r.service.command(ctx, r.author(ctx), action, req, next)
				}
				if interceptor != nil {
					return interceptor(ctx, req, info, audited)
				}

				return audited(ctx, req)
			})
		}
	}

	r.next.RegisterService(&wrapped, impl)
}

func commandAction(method string) string {
	actions := map[string]string{
		consolev1.ScheduleService_CreateSchedule_FullMethodName:  "schedule.create",
		consolev1.ScheduleService_UpdateSchedule_FullMethodName:  "schedule.update",
		consolev1.ScheduleService_DeleteSchedule_FullMethodName:  "schedule.delete",
		consolev1.EventService_PublishTestEvent_FullMethodName:   "event.publish_test",
		consolev1.EventService_RedriveDeadLetters_FullMethodName: "event.redrive",
		consolev1.EventService_PurgeDeadLetters_FullMethodName:   "event.purge",
		consolev1.WorkflowService_StartWorkflow_FullMethodName:   "workflow.start",
		consolev1.WorkflowService_CancelRun_FullMethodName:       "workflow.cancel",
		consolev1.WorkflowService_TerminateRun_FullMethodName:    "workflow.terminate",
		consolev1.WorkflowService_SignalRun_FullMethodName:       "workflow.signal",
		consolev1.ScheduleService_PauseSchedule_FullMethodName:   "schedule.pause",
		consolev1.ScheduleService_UnpauseSchedule_FullMethodName: "schedule.resume",
		consolev1.ScheduleService_TriggerSchedule_FullMethodName: "schedule.trigger",
		consolev1.CallService_CallHook_FullMethodName:            "hook.call",
		consolev1.CallService_RunActivity_FullMethodName:         "activity.run",
		consolev1.BindingService_TestBinding_FullMethodName:      "binding.test",
		consolev1.BindingService_CancelBindingRun_FullMethodName: "binding.cancel",
		consolev1.RuleService_TestRule_FullMethodName:            "rule.test",
		consolev1.RuleService_CancelRuleRun_FullMethodName:       "rule.cancel",
	}

	return actions[method]
}

func (s *Service) command(ctx context.Context, actor, action string, req any, next grpc.UnaryHandler) (any, error) {
	draft := store.AuditDraft{
		Actor:       actor,
		Action:      action,
		Subject:     commandSubject(req),
		Outcome:     "intent",
		OperationID: uuid.New(),
		Detail:      commandDetail(req),
		Service:     s.commandService(ctx, req),
	}
	if _, err := s.store.Get().AppendAudit(ctx, draft); err != nil {
		return nil, rpcError(codes.Unavailable, "cannot persist audit intent; command was not dispatched")
	}

	response, callErr := next(ctx, req)
	draft.Outcome, draft.Detail = commandResult(draft.Detail, response, callErr)

	resultCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resultDeadline)
	defer cancel()

	if _, err := s.store.Get().AppendAudit(resultCtx, draft); err != nil {
		return nil, rpcError(codes.Aborted, "command may have completed; audit operation "+
			draft.OperationID.String()+" has an unresolved result")
	}

	return response, callErr
}

func commandSubject(value any) string {
	message, isMessage := value.(proto.Message)
	if !isMessage {
		return ""
	}

	var parts []string

	for _, name := range []protoreflect.Name{
		"service",
		"hook",
		"activity",
		"workflow",
		"subscriber",
		"consumer",
		"name",
		"id",
		"workflow_id",
		"run_id",
	} {
		if field := message.ProtoReflect().Descriptor().Fields().ByName(name); field != nil &&
			field.Kind() == protoreflect.StringKind && !field.IsList() {
			if text := message.ProtoReflect().Get(field).String(); text != "" {
				parts = append(parts, string(name)+"="+text)
			}
		}
	}

	// Only PublishTestEvent.event is an identifier; TestRule.event is a payload.
	if req, ok := value.(*consolev1.PublishTestEventRequest); ok && req.GetEvent() != "" {
		parts = append(parts, "event="+req.GetEvent())
	}

	return strings.Join(parts, ";")
}

// commandDetail is what a command's request adds to its entries: the run it
// addresses and the operator's words — a signal's name (never its argument),
// a termination reason, a pause or resume note — bounded.
func commandDetail(value any) store.AuditDetail {
	var detail store.AuditDetail

	switch req := value.(type) {
	case *consolev1.CancelRunRequest:
		detail.WorkflowID, detail.RunID = req.GetWorkflowId(), req.GetRunId()
	case *consolev1.TerminateRunRequest:
		detail.WorkflowID, detail.RunID, detail.Reason = req.GetWorkflowId(), req.GetRunId(), bounded(req.GetReason())
	case *consolev1.SignalRunRequest:
		detail.WorkflowID, detail.RunID, detail.Signal = req.GetWorkflowId(), req.GetRunId(), bounded(req.GetSignal())
	case *consolev1.CancelBindingRunRequest:
		detail.WorkflowID, detail.RunID = req.GetWorkflowId(), req.GetRunId()
	case *consolev1.CancelRuleRunRequest:
		detail.WorkflowID, detail.RunID = req.GetWorkflowId(), req.GetRunId()
	case *consolev1.PauseScheduleRequest:
		detail.Note = bounded(req.GetNote())
	case *consolev1.UnpauseScheduleRequest:
		detail.Note = bounded(req.GetNote())
	}

	return detail
}

// bounded is s cut to maxWords runes.
func bounded(s string) string {
	if utf8.RuneCountInString(s) <= maxWords {
		return s
	}

	return string([]rune(s)[:maxWords]) + "…"
}

// commandService is the service a command is addressed to: its service or
// subscriber, the owner of its hook/activity/event, the event owner of a
// rule, or the service of the run it acts on. Empty when none of these is
// known.
func (s *Service) commandService(ctx context.Context, value any) string {
	message, isMessage := value.(proto.Message)
	if !isMessage {
		return ""
	}

	if service := declaredService(message, value); service != "" {
		return service
	}

	if service := s.ruleService(ctx, stringField(message, "id")); service != "" {
		return service
	}

	if workflowID := stringField(message, "workflow_id"); workflowID != "" {
		return s.runService(ctx, workflowID, stringField(message, "run_id"))
	}

	return ""
}

// ruleService is the owner of the event of rule id; empty when id is not a
// rule's.
func (s *Service) ruleService(ctx context.Context, id string) string {
	rule, err := uuid.Parse(id)
	if err != nil {
		return ""
	}

	event, err := s.store.Get().Q.GetRuleEvent(ctx, rule)
	if err != nil {
		return ""
	}

	return store.ServiceOf(event.Event)
}

// runService is the service a run belongs to. Backplane's ids say it:
// console/<service>/..., console/activity/<service>/..., hook/<service>/...,
// binding/<hook>/..., test/<hook>/..., rule/<id>/... and test/rule/<id>/...
// (the owner of the rule's event). Any other id is the service of the run's
// task queue (<service> or <service>.hooks) when Temporal is asked, else a
// schedule's run <service>/<Name>-<time>.
func (s *Service) runService(ctx context.Context, workflowID, runID string) string {
	switch kind, key := runOwner(workflowID); kind {
	case ownerService:
		return key
	case ownerRule:
		// A draft's test run (test/rule/draft/...) names no saved rule.
		if service := s.ruleService(ctx, key); service != "" {
			return service
		}
	case ownerNone:
	}

	if s.runQueue != nil {
		lookup, cancel := context.WithTimeout(ctx, lookupDeadline)
		defer cancel()

		if queue, err := s.runQueue(lookup, workflowID, runID); err == nil {
			return queueService(queue)
		}
	}

	if match := scheduleRun.FindStringSubmatch(workflowID); match != nil {
		return match[1]
	}

	return ""
}

// consoleActivityParts: console/activity/<service>/<Name>/<uuid>, where
// a console start is console/<service>/<Workflow>/<uuid>.
const consoleActivityParts = 5

// What a run id names.
type owner int

const (
	ownerNone owner = iota
	ownerService
	ownerRule
)

var (
	serviceName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	// scheduleRun is the id of a schedule's run: <service>/<Name>-<time>.
	scheduleRun = regexp.MustCompile(`^([a-z][a-z0-9-]*)/[A-Za-z][A-Za-z0-9_]*-`)
)

// runOwner reads the owner of a run from Backplane's workflow ids: a
// service, or a rule (by id) whose event owner owns it.
func runOwner(workflowID string) (owner, string) {
	if hook, _, ok := wire.BindingRunHook(workflowID); ok {
		return ownerService, store.ServiceOf(hook)
	}

	parts := strings.Split(workflowID, "/")
	// service is the first of rest, when more follows it.
	service := func(rest []string) (owner, string) {
		if len(rest) > 1 && serviceName.MatchString(rest[0]) {
			return ownerService, rest[0]
		}

		return ownerNone, ""
	}

	switch {
	case len(parts) > 3 && parts[0] == "test" && parts[1] == "rule":
		return ownerRule, parts[2]
	case len(parts) > 2 && parts[0] == "rule":
		return ownerRule, parts[1]
	case parts[0] == "hook":
		return service(parts[1:])
	case len(parts) == consoleActivityParts && parts[0] == "console" && parts[1] == "activity":
		return service(parts[2:])
	case parts[0] == "console":
		return service(parts[1:])
	}

	return ownerNone, ""
}

// queueService is the service of a task queue: <service> or
// <service>.hooks.
func queueService(queue string) string {
	service := strings.TrimSuffix(queue, wire.HooksQueue(""))
	if !serviceName.MatchString(service) {
		return ""
	}

	return service
}

// declaredService reads the service a request names directly or through a
// qualified hook, activity, event or unsaved definition.
func declaredService(message proto.Message, value any) string {
	for _, name := range []protoreflect.Name{"service", "subscriber"} {
		if service := stringField(message, name); service != "" {
			return service
		}
	}

	for _, name := range []protoreflect.Name{"hook", "activity"} {
		if qualified := stringField(message, name); qualified != "" {
			return store.ServiceOf(qualified)
		}
	}

	switch req := value.(type) {
	case *consolev1.PublishTestEventRequest:
		return store.ServiceOf(req.GetEvent())
	case *consolev1.TestBindingRequest:
		return store.ServiceOf(req.GetDefinition().GetHook())
	case *consolev1.TestRuleRequest:
		return store.ServiceOf(req.GetDefinition().GetEvent())
	}

	return ""
}

func stringField(message proto.Message, name protoreflect.Name) string {
	field := message.ProtoReflect().Descriptor().Fields().ByName(name)
	if field == nil || field.Kind() != protoreflect.StringKind || field.IsList() {
		return ""
	}

	return message.ProtoReflect().Get(field).String()
}

// commandResult is the outcome of a command and its detail: detail (what
// the request said) with what the response adds.
func commandResult(detail store.AuditDetail, response any, err error) (string, store.AuditDetail) {
	detail.Code = status.Code(err).String()
	if err != nil {
		switch status.Code(err) {
		case codes.InvalidArgument, codes.NotFound, codes.FailedPrecondition, codes.PermissionDenied,
			codes.AlreadyExists:
			return "rejected", detail
		default:
			return "unknown", detail
		}
	}

	return responseResult(response, detail)
}

func responseResult(response any, detail store.AuditDetail) (string, store.AuditDetail) {
	message, isMessage := response.(proto.Message)
	if !isMessage {
		return outcomeSucceeded, detail
	}

	fields := message.ProtoReflect().Descriptor().Fields()
	if field := fields.ByName("violations"); field != nil && field.IsList() &&
		message.ProtoReflect().Get(field).List().Len() > 0 {
		detail.Code = "ValidationFailed"
		return "rejected", detail
	}

	if result, ok := response.(interface{ GetResult() *consolev1.CallResult }); ok {
		call := result.GetResult()

		detail.WorkflowID, detail.RunID = call.GetWorkflowId(), call.GetRunId()
		if call.GetError() != "" {
			detail.Code = "CallFailed"
			return outcomeFailed, detail
		}
	}

	if result, ok := response.(*consolev1.TestRuleResponse); ok && result.GetError() != "" {
		detail.Code = "EvaluationFailed"
		return outcomeFailed, detail
	}

	detail = startedRun(response, detail)

	switch result := response.(type) {
	case *consolev1.RedriveDeadLettersResponse:
		return batchOutcome(result.GetRedriven(), len(result.GetFailed()), detail)
	case *consolev1.PurgeDeadLettersResponse:
		return batchOutcome(result.GetPurged(), len(result.GetFailed()), detail)
	default:
		return outcomeSucceeded, detail
	}
}

// startedRun is detail with the run a response names (StartWorkflow's).
func startedRun(response any, detail store.AuditDetail) store.AuditDetail {
	if result, ok := response.(interface{ GetWorkflowId() string }); ok && result.GetWorkflowId() != "" {
		detail.WorkflowID = result.GetWorkflowId()
	}

	if result, ok := response.(interface{ GetRunId() string }); ok && result.GetRunId() != "" {
		detail.RunID = result.GetRunId()
	}

	return detail
}

func batchOutcome(completed uint64, failed int, detail store.AuditDetail) (string, store.AuditDetail) {
	if failed == 0 {
		return outcomeSucceeded, detail
	}

	detail.Code = "BatchFailed"
	if completed == 0 {
		return outcomeFailed, detail
	}

	return "partial", detail
}
