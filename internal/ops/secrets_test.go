package ops_test

import (
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/ops"
	"github.com/backplane-labs/backplane/internal/redact"
)

type (
	otpIn struct {
		To   string `json:"to"`
		Code string `json:"code" schemapb:"secret=true"`
	}
	vaultOut struct {
		Secret string `json:"secret" schemapb:"secret=true"`
	}
)

func schemaOf[T any](t *testing.T) *sp.Schema {
	t.Helper()

	s, err := sp.ReflectType[T](sp.ID(sp.Namespace("test"), sp.SchemaName("payload"), sp.Ver(1, 0, 0)))
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func encoded(t *testing.T, m proto.Message) *commonpb.Payloads {
	t.Helper()

	ps, err := converter.GetDefaultDataConverter().ToPayloads(m)
	if err != nil {
		t.Fatal(err)
	}

	return ps
}

// A run read through GetRun hides what the manifests mark secret: a hook
// call's input and an activity's output, in the history and in the
// response's input and result.
func TestRunHistoryMasked(t *testing.T) {
	t.Parallel()

	cat := redact.Manifests(func(service string) *backplanev1.Manifest {
		switch service {
		case "iam":
			return &backplanev1.Manifest{Hooks: []*backplanev1.Hook{{Name: "SendOTP", Input: schemaOf[otpIn](t)}}}
		case "vault":
			return &backplanev1.Manifest{Activities: []*backplanev1.Activity{{Name: "Read", Output: schemaOf[vaultOut](t)}}}
		default:
			return nil
		}
	})

	call := &backplanev1.HookCall{Hook: "iam.SendOTP", Instance: "console", Payload: []byte(`{"to":"a","code":"1"}`)}
	events := []*historypb.HistoryEvent{
		{
			EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
			Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
				WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
					WorkflowType: &commonpb.WorkflowType{Name: ops.CallHookWorkflow},
					TaskQueue:    &taskqueuepb.TaskQueue{Name: "backplane"}, Input: encoded(t, call),
				},
			},
		},
		{
			EventId: 5, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED,
			Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{
				ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{
					ActivityType: &commonpb.ActivityType{Name: "Read"},
					Input:        encoded(t, &backplanev1.ActivityCall{Activity: "vault.Read", Payload: []byte(`{}`)}),
				},
			},
		},
		{
			EventId: 6, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED,
			Attributes: &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{
				ActivityTaskCompletedEventAttributes: &historypb.ActivityTaskCompletedEventAttributes{
					ScheduledEventId: 5, Result: encoded(t, &backplanev1.ActivityResult{Payload: []byte(`{"secret":"s"}`)}),
				},
			},
		},
		{
			EventId: 9, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED,
			Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{
				WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{
					Result: encoded(t, &backplanev1.HookResult{Payload: []byte(`{"ok":true}`)}),
				},
			},
		},
	}

	evs, out := ops.MaskedHistory(events, redact.NewHistory(cat))

	wantStart := `{"hook":"iam.SendOTP","instance":"console","payload":{"code":"***","to":"a"}}`
	if evs[0].GetPayload() != wantStart || out.GetInput() != wantStart {
		t.Errorf("started: %s / %s", evs[0].GetPayload(), out.GetInput())
	}

	if evs[1].GetPayload() != `{"activity":"vault.Read","payload":{}}` {
		t.Errorf("scheduled: %s", evs[1].GetPayload())
	}

	if evs[2].GetPayload() != `{"payload":{"secret":"***"}}` {
		t.Errorf("completed: %s", evs[2].GetPayload())
	}

	if evs[3].GetPayload() != `{"payload":{"ok":true}}` || out.GetResult() != `{"payload":{"ok":true}}` {
		t.Errorf("result: %s / %s", evs[3].GetPayload(), out.GetResult())
	}
}
