package executor

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/redact"
)

// timeline is the steps of a run from its history: every program step by
// name — its call, or NOT_RUN — then every compensation in
// the order it was scheduled. A step's first call is the step; a second
// one (only undo schedules a step again: retries are attempts of one
// call) is its undo. pending adds the attempt and last failure of calls
// in flight. Inputs and outputs are masked by the program's schemas.
func timeline(
	program json.RawMessage, events []*historypb.HistoryEvent, pending []*consolev1.PendingActivity,
) []*consolev1.StepRun {
	builder := newTimelineBuilder(redact.ParseProgram(program))

	for _, e := range events {
		builder.event(e)
	}

	builder.pending(pending)

	return builder.runs()
}

// timelineBuilder collects the calls of a run while its history is read.
type timelineBuilder struct {
	prog  *redact.Program
	calls map[int64]*consolev1.StepRun  // by scheduled / initiated event id
	byAct map[string]*consolev1.StepRun // by activity id
	first map[string]*consolev1.StepRun // a call's first run, by call label
	order []*consolev1.StepRun          // first runs in scheduling order
	undos []*consolev1.StepRun
	// masks are what each call's output is masked by.
	masks map[*consolev1.StepRun]func([]byte) []byte
}

func newTimelineBuilder(prog *redact.Program) *timelineBuilder {
	return &timelineBuilder{
		prog:  prog,
		calls: map[int64]*consolev1.StepRun{},
		byAct: map[string]*consolev1.StepRun{},
		first: map[string]*consolev1.StepRun{},
		masks: map[*consolev1.StepRun]func([]byte) []byte{},
	}
}

// schedule records the call scheduled (or initiated) by e: the step's
// own call the first time, its undo after.
func (tl *timelineBuilder) schedule(
	e *historypb.HistoryEvent, input *commonpb.Payloads, workflow bool,
) *consolev1.StepRun {
	var call backplanev1.ActivityCall

	if ps := input.GetPayloads(); len(ps) > 0 {
		_ = converter.GetDefaultDataConverter().FromPayload(ps[0], &call)
	}

	label := bindings.ParseCallLabel(call.GetStep())
	run := &consolev1.StepRun{
		Step: label.Step, Parent: label.Parent, Activity: call.GetActivity(), Workflow: workflow,
		Status: consolev1.StepRunStatus_STEP_RUN_STATUS_SCHEDULED, ScheduledTime: e.GetEventTime(),
	}

	if label.Item >= 0 {
		run.Item = proto.Uint32(uint32(label.Item)) //nolint:gosec // an item index
	}

	// A call label scheduled again is the call's compensation: retries are
	// attempts of one call.
	_, undo := tl.first[call.GetStep()]
	if undo {
		run.Undo = true
		tl.undos = append(tl.undos, run)
	} else {
		tl.first[call.GetStep()] = run
		tl.order = append(tl.order, run)
	}

	payload := call.GetPayload()

	if st, ok := tl.prog.Step(call.GetStep()); ok {
		tl.masks[run] = st.MaskOutput

		if undo {
			payload = st.MaskUndo(payload)
		} else {
			payload = st.MaskInput(payload)
		}
	}

	run.Input = jsonText(payload)
	tl.calls[e.GetEventId()] = run

	return run
}

// event applies one history event: an activity's or a child workflow's.
func (tl *timelineBuilder) event(e *historypb.HistoryEvent) {
	if !tl.activityEvent(e) {
		tl.childEvent(e)
	}
}

// activityEvent applies e when it is an activity task's event.
func (tl *timelineBuilder) activityEvent(e *historypb.HistoryEvent) bool {
	switch {
	case e.GetActivityTaskScheduledEventAttributes() != nil:
		at := e.GetActivityTaskScheduledEventAttributes()
		tl.byAct[at.GetActivityId()] = tl.schedule(e, at.GetInput(), false)
	case e.GetActivityTaskStartedEventAttributes() != nil:
		at := e.GetActivityTaskStartedEventAttributes()
		started(tl.calls[at.GetScheduledEventId()], e.GetEventTime(), at.GetAttempt())
	case e.GetActivityTaskCompletedEventAttributes() != nil:
		at := e.GetActivityTaskCompletedEventAttributes()
		tl.completed(tl.calls[at.GetScheduledEventId()], e.GetEventTime(), at.GetResult())
	case e.GetActivityTaskFailedEventAttributes() != nil:
		at := e.GetActivityTaskFailedEventAttributes()
		failed(tl.calls[at.GetScheduledEventId()], e.GetEventTime(), consolev1.StepRunStatus_STEP_RUN_STATUS_FAILED,
			at.GetFailure())
	case e.GetActivityTaskTimedOutEventAttributes() != nil:
		at := e.GetActivityTaskTimedOutEventAttributes()
		failed(tl.calls[at.GetScheduledEventId()], e.GetEventTime(), consolev1.StepRunStatus_STEP_RUN_STATUS_TIMED_OUT,
			at.GetFailure())
	case e.GetActivityTaskCanceledEventAttributes() != nil:
		at := e.GetActivityTaskCanceledEventAttributes()
		failed(tl.calls[at.GetScheduledEventId()], e.GetEventTime(), consolev1.StepRunStatus_STEP_RUN_STATUS_CANCELED, nil)
	default:
		return false
	}

	return true
}

// childEvent applies e when it is a child workflow's event.
func (tl *timelineBuilder) childEvent(e *historypb.HistoryEvent) {
	switch {
	case e.GetStartChildWorkflowExecutionInitiatedEventAttributes() != nil:
		at := e.GetStartChildWorkflowExecutionInitiatedEventAttributes()
		tl.schedule(e, at.GetInput(), true)
	case e.GetStartChildWorkflowExecutionFailedEventAttributes() != nil:
		at := e.GetStartChildWorkflowExecutionFailedEventAttributes()
		if run := tl.calls[at.GetInitiatedEventId()]; run != nil {
			run.Status, run.CloseTime = consolev1.StepRunStatus_STEP_RUN_STATUS_FAILED, e.GetEventTime()
			run.Error = "child workflow did not start: " + at.GetCause().String()
		}
	case e.GetChildWorkflowExecutionStartedEventAttributes() != nil:
		at := e.GetChildWorkflowExecutionStartedEventAttributes()
		started(tl.calls[at.GetInitiatedEventId()], e.GetEventTime(), 1)
	case e.GetChildWorkflowExecutionCompletedEventAttributes() != nil:
		at := e.GetChildWorkflowExecutionCompletedEventAttributes()
		tl.completed(tl.calls[at.GetInitiatedEventId()], e.GetEventTime(), at.GetResult())
	case e.GetChildWorkflowExecutionFailedEventAttributes() != nil:
		at := e.GetChildWorkflowExecutionFailedEventAttributes()
		failed(tl.calls[at.GetInitiatedEventId()], e.GetEventTime(), consolev1.StepRunStatus_STEP_RUN_STATUS_FAILED,
			at.GetFailure())
	case e.GetChildWorkflowExecutionTimedOutEventAttributes() != nil:
		at := e.GetChildWorkflowExecutionTimedOutEventAttributes()
		failed(tl.calls[at.GetInitiatedEventId()], e.GetEventTime(), consolev1.StepRunStatus_STEP_RUN_STATUS_TIMED_OUT, nil)
	case e.GetChildWorkflowExecutionCanceledEventAttributes() != nil:
		at := e.GetChildWorkflowExecutionCanceledEventAttributes()
		failed(tl.calls[at.GetInitiatedEventId()], e.GetEventTime(), consolev1.StepRunStatus_STEP_RUN_STATUS_CANCELED, nil)
	case e.GetChildWorkflowExecutionTerminatedEventAttributes() != nil:
		at := e.GetChildWorkflowExecutionTerminatedEventAttributes()
		if run := tl.calls[at.GetInitiatedEventId()]; run != nil {
			run.Status, run.CloseTime = consolev1.StepRunStatus_STEP_RUN_STATUS_FAILED, e.GetEventTime()
			run.Error = "terminated"
		}
	}
}

// pending adds the attempt and last failure of the calls in flight.
func (tl *timelineBuilder) pending(pending []*consolev1.PendingActivity) {
	for _, p := range pending {
		run := tl.byAct[p.GetActivityId()]
		if run == nil || run.GetCloseTime() != nil {
			continue
		}

		run.Attempt = p.GetAttempt()
		run.Error = p.GetLastFailure()

		if p.GetState() == "STARTED" {
			run.Status = consolev1.StepRunStatus_STEP_RUN_STATUS_STARTED
			run.StartedTime = p.GetLastStartedTime()
		}
	}
}

// runs orders the collected calls: each program step's calls (a for-each
// step's items and body calls in scheduling order; NOT_RUN when none),
// then calls of steps the program does not name (a history of another
// program version), then undos.
func (tl *timelineBuilder) runs() []*consolev1.StepRun {
	steps := tl.prog.Steps()
	out := make([]*consolev1.StepRun, 0, len(steps)+len(tl.order)+len(tl.undos))
	used := map[*consolev1.StepRun]bool{}

	for _, s := range steps {
		found := false

		for _, run := range tl.order {
			if top, _, _ := strings.Cut(run.GetParent(), "/"); top == s.Name || (top == "" && run.GetStep() == s.Name) {
				out, used[run], found = append(out, run), true, true
			}
		}

		if !found {
			out = append(out, &consolev1.StepRun{
				Step: s.Name, Activity: s.Activity, Workflow: s.Kind == backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW,
				Status: consolev1.StepRunStatus_STEP_RUN_STATUS_NOT_RUN,
			})
		}
	}

	for _, run := range tl.order {
		if !used[run] {
			out = append(out, run)
		}
	}

	return append(out, tl.undos...)
}

func started(run *consolev1.StepRun, at *timestamppb.Timestamp, attempt int32) {
	if run == nil {
		return
	}

	run.Status, run.StartedTime, run.Attempt = consolev1.StepRunStatus_STEP_RUN_STATUS_STARTED, at, attempt
}

func (tl *timelineBuilder) completed(run *consolev1.StepRun, at *timestamppb.Timestamp, result *commonpb.Payloads) {
	if run == nil {
		return
	}

	run.Status, run.CloseTime, run.Error = consolev1.StepRunStatus_STEP_RUN_STATUS_COMPLETED, at, ""

	var res backplanev1.ActivityResult
	if ps := result.GetPayloads(); len(ps) > 0 && converter.GetDefaultDataConverter().FromPayload(ps[0], &res) == nil {
		output := res.GetPayload()
		if m := tl.masks[run]; m != nil {
			output = m(output)
		}

		run.Output = jsonText(output)
	}
}

func failed(
	run *consolev1.StepRun, closeTime *timestamppb.Timestamp, st consolev1.StepRunStatus, f *failurepb.Failure,
) {
	if run == nil {
		return
	}

	run.Status, run.CloseTime = st, closeTime
	run.Error, run.ErrorType = failureOf(f)

	if run.GetError() == "" {
		run.Error = map[consolev1.StepRunStatus]string{
			consolev1.StepRunStatus_STEP_RUN_STATUS_TIMED_OUT: "timed out",
			consolev1.StepRunStatus_STEP_RUN_STATUS_CANCELED:  "canceled",
		}[st]
	}
}

// failureOf is the deepest message of a failure chain and the type of its
// deepest application failure.
func failureOf(f *failurepb.Failure) (string, string) {
	var msg, typ string

	for ; f != nil; f = f.GetCause() {
		if f.GetMessage() != "" {
			msg = f.GetMessage()
		}

		if t := f.GetApplicationFailureInfo().GetType(); t != "" {
			typ = t
		}
	}

	return msg, typ
}

// jsonText is a payload as JSON text: as is when it is JSON, else a
// base64 JSON string; empty stays empty.
func jsonText(b []byte) string {
	switch {
	case len(b) == 0:
		return ""
	case json.Valid(b):
		return string(b)
	default:
		return string(jsonString(base64.StdEncoding.EncodeToString(b)))
	}
}

// jsonString is s as a JSON string.
func jsonString(s string) []byte {
	quoted, _ := json.Marshal(s) //nolint:errchkjson // a string always marshals

	return quoted
}

// historyFilter reads every event.
const historyFilter = enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT
