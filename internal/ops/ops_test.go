package ops_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/ops"
	"github.com/backplane-labs/backplane/internal/registry"
)

// fixture is an installation of hello (events, activities, workflows,
// hooks, schedules) and watcher (a reactor on hello.Greeted).
func fixture(t *testing.T) *registry.Hub {
	t.Helper()

	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{
		"hello": service(&backplanev1.Manifest{
			Service: "hello", Version: "1.0.0",
			Events: []*backplanev1.Event{{Name: "Greeted", Description: "a greeting"}},
			Activities: []*backplanev1.Activity{
				{Name: "Echo", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY, StartToClose: durationpb.New(5 * time.Second)},
				{Name: "Flow", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW},
			},
			Workflows: []*backplanev1.Workflow{{Name: "Greet", Description: "greets"}},
			Hooks:     []*backplanev1.Hook{{Name: "Ask", Timeout: durationpb.New(3 * time.Second)}, {Name: "Tell"}},
			Schedules: []*backplanev1.Schedule{{Name: "Nightly", Spec: &backplanev1.Schedule_Cron{Cron: "0 3 * * *"}, Workflow: "Greet"}},
		}),
		"watcher": service(&backplanev1.Manifest{
			Service: "watcher", Version: "1.0.0",
			Subscriptions: []*backplanev1.Subscription{{Event: "hello.Greeted", Consumer: "sink"}},
		}),
	})

	return hub
}

func service(m *backplanev1.Manifest) registry.Service {
	return registry.Service{
		Name: m.GetService(), Manifests: map[string]*backplanev1.Manifest{m.GetVersion(): m},
		Instances: []registry.Instance{{ID: m.GetService() + "-1", State: &backplanev1.InstanceState{Version: m.GetVersion()}}},
	}
}

func code(t *testing.T, err error) codes.Code {
	t.Helper()

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a status: %v", err)
	}

	return st.Code()
}

// TestWithoutNATS: declarations come from the manifests alone; calls
// that need NATS or Temporal are UNAVAILABLE; unknown names NOT_FOUND.
func TestWithoutNATS(t *testing.T) {
	t.Parallel()

	srv := newServers(ops.Detached(fixture(t)))

	res, err := srv.events.ListEvents(t.Context(), &consolev1.ListEventsRequest{})
	if err != nil {
		t.Fatal(err)
	}

	if res.GetNatsError() == "" || len(res.GetEvents()) != 1 {
		t.Fatalf("list: %v", res)
	}

	greeted := res.GetEvents()[0]
	if greeted.GetEvent() != "hello.Greeted" || !greeted.GetDeclared() || greeted.GetDescription() != "a greeting" ||
		greeted.GetSubject() != "bp.hello.Greeted" || len(greeted.GetSubscribers()) != 1 {
		t.Fatalf("event: %v", greeted)
	}

	if s := greeted.GetSubscribers()[0]; s.GetService() != "watcher" || s.GetConsumer() != "sink" ||
		s.GetDurable() != "watcher__sink" || s.GetKind() != consolev1.SubscriberKind_SUBSCRIBER_KIND_REACTOR || s.GetState() != nil {
		t.Fatalf("subscriber: %v", s)
	}

	if _, err := srv.events.ListEvents(t.Context(), &consolev1.ListEventsRequest{Service: "nope"}); code(t, err) != codes.NotFound {
		t.Fatalf("unknown service: %v", err)
	}

	if _, err := srv.events.GetStream(t.Context(), &consolev1.GetStreamRequest{Service: "hello"}); code(t, err) != codes.Unavailable {
		t.Fatalf("stream without nats: %v", err)
	}

	if _, err := srv.workflows.ListRuns(t.Context(), &consolev1.ListRunsRequest{Service: "hello"}); code(t, err) != codes.Unavailable {
		t.Fatalf("runs without temporal: %v", err)
	}

	wfs, err := srv.workflows.ListWorkflows(t.Context(), &consolev1.ListWorkflowsRequest{})
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(wfs.GetWorkflows()))
	for _, w := range wfs.GetWorkflows() {
		names = append(names, w.GetService()+"."+w.GetName()+":"+w.GetKind().String()+"@"+w.GetTaskQueue())
	}

	if got := strings.Join(names, " "); got != "hello.Greet:WORKFLOW_KIND_WORKFLOW@hello hello.Flow:WORKFLOW_KIND_ACTIVITY@hello" {
		t.Fatalf("workflows: %s", got)
	}

	notSynced := ops.Detached(registry.NewHub())
	if _, err := newServers(notSynced).events.ListEvents(t.Context(), &consolev1.ListEventsRequest{}); code(t, err) != codes.Unavailable {
		t.Fatalf("not synced: %v", err)
	}
}

// A pattern reactor subscribes to every listed event it matches.
func TestPatternSubscribers(t *testing.T) {
	t.Parallel()

	hub := registry.NewHub()
	hub.Publish(map[string]registry.Service{
		"hello": service(&backplanev1.Manifest{
			Service: "hello", Version: "1.0.0", Events: []*backplanev1.Event{{Name: "Greeted"}},
		}),
		"watcher": service(&backplanev1.Manifest{
			Service: "watcher", Version: "1.0.0",
			Subscriptions: []*backplanev1.Subscription{{Event: "hello.Greeted", Consumer: "sink"}},
		}),
		"journal": service(&backplanev1.Manifest{
			Service: "journal", Version: "1.0.0",
			Subscriptions: []*backplanev1.Subscription{
				{Event: "*", Consumer: "all"}, {Event: "hello.*", Consumer: "hello"}, {Event: "other.*", Consumer: "other"},
			},
		}),
	})

	res, err := newServers(ops.Detached(hub)).events.ListEvents(t.Context(), &consolev1.ListEventsRequest{})
	if err != nil || len(res.GetEvents()) != 1 {
		t.Fatalf("list: %v %v", res, err)
	}

	consumers := make([]string, 0, len(res.GetEvents()[0].GetSubscribers()))
	for _, s := range res.GetEvents()[0].GetSubscribers() {
		consumers = append(consumers, s.GetService()+"/"+s.GetConsumer())
	}

	slices.Sort(consumers)

	if got := strings.Join(consumers, " "); got != "journal/all journal/hello watcher/sink" {
		t.Fatalf("subscribers: %s", got)
	}
}

type servers struct {
	events    consolev1.EventServiceServer
	workflows consolev1.WorkflowServiceServer
	schedules consolev1.ScheduleServiceServer
	calls     consolev1.CallServiceServer
}

// capture is a registrar that keeps the implementations.
type capture struct{ impl map[string]any }

func (c *capture) RegisterService(desc *grpc.ServiceDesc, impl any) {
	if c.impl == nil {
		c.impl = map[string]any{}
	}

	c.impl[desc.ServiceName] = impl
}

// newServers are o's services as the console registers them.
func newServers(o *ops.Ops) servers {
	r := &capture{}
	o.Register(r)

	return servers{
		events:    r.impl["backplane.console.v1.EventService"].(consolev1.EventServiceServer),
		workflows: r.impl["backplane.console.v1.WorkflowService"].(consolev1.WorkflowServiceServer),
		schedules: r.impl["backplane.console.v1.ScheduleService"].(consolev1.ScheduleServiceServer),
		calls:     r.impl["backplane.console.v1.CallService"].(consolev1.CallServiceServer),
	}
}

// TestTestEvent: a test event carries the CloudEvents of its service's
// emitter plus the console's marks; undeclared events, bad JSON and
// reserved extensions are refused.
func TestTestEvent(t *testing.T) {
	t.Parallel()

	o := ops.Detached(fixture(t))
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(ops.SessionMetadata, "s-1"))

	msg, err := o.TestEvent(ctx, &consolev1.PublishTestEventRequest{
		Event: "hello.Greeted", Payload: `{"name":"x"}`, Key: "k1", Extensions: map[string]string{"tenant": "t1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	h := msg.Header
	if msg.Subject != "bp.hello.Greeted" || string(msg.Data) != `{"name":"x"}` ||
		h.Get("ce-specversion") != "1.0" || h.Get("ce-source") != "hello" || h.Get("ce-type") != "hello.Greeted" ||
		h.Get("ce-subject") != "k1" || h.Get("ce-instance") != "backplane" || h.Get("ce-bptest") != "true" ||
		h.Get("ce-bpauthor") != "console:s-1" || h.Get("ce-tenant") != "t1" || h.Get("ce-id") == "" ||
		h.Get(jetstream.MsgIDHeader) != h.Get("ce-id") || h.Get("content-type") != "application/json" {
		t.Fatalf("message: %s %s %v", msg.Subject, msg.Data, h)
	}

	if _, err := time.Parse(time.RFC3339Nano, h.Get("ce-time")); err != nil {
		t.Fatalf("ce-time: %v", err)
	}

	empty, err := o.TestEvent(ctx, &consolev1.PublishTestEventRequest{Event: "hello.Greeted", Id: "fixed"})
	if err != nil || string(empty.Data) != "{}" || empty.Header.Get("ce-id") != "fixed" {
		t.Fatalf("defaults: %v %v", empty, err)
	}

	for name, req := range map[string]*consolev1.PublishTestEventRequest{
		"undeclared": {Event: "hello.Nope"},
		"bad name":   {Event: "hello"},
		"bad json":   {Event: "hello.Greeted", Payload: "{"},
		"reserved":   {Event: "hello.Greeted", Extensions: map[string]string{"source": "x"}},
		"ext name":   {Event: "hello.Greeted", Extensions: map[string]string{"Bad-Name": "x"}},
		"key break":  {Event: "hello.Greeted", Key: "a\nb"},
	} {
		if _, err := o.TestEvent(ctx, req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	if _, err := o.TestEvent(ctx, &consolev1.PublishTestEventRequest{Event: "hello.Nope"}); !errors.Is(err, ops.ErrNotDeclared) {
		t.Fatalf("undeclared: %v", err)
	}
}

func TestMessagePB(t *testing.T) {
	t.Parallel()

	stored := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	m := ops.MessagePB(&jetstream.RawStreamMsg{
		Subject: "bp.hello.Greeted", Sequence: 7, Time: stored, Data: []byte(`{"a":1}`),
		Header: nats.Header{
			"ce-specversion": {"1.0"}, "ce-id": {"id-1"}, "ce-source": {"hello"}, "ce-type": {"hello.Greeted"},
			"ce-time": {"2026-09-29T09:59:59.5Z"}, "ce-subject": {"k"}, "ce-instance": {"hello-1"},
			"ce-tenant": {"t1"}, "ce-bptest": {"true"}, "traceparent": {"00-x"},
		},
	})

	ce := m.GetCloudEvent()
	if m.GetSeq() != 7 || m.GetEvent() != "hello.Greeted" || m.GetData() != `{"a":1}` || m.GetRaw() != nil ||
		!m.GetTest() || !m.GetStoredAt().AsTime().Equal(stored) || m.GetHeaders()["traceparent"] != "00-x" ||
		ce.GetId() != "id-1" || ce.GetSource() != "hello" || ce.GetSubject() != "k" || ce.GetInstance() != "hello-1" ||
		ce.GetTime().AsTime().Nanosecond() != 5e8 || len(ce.GetExtensions()) != 2 || ce.GetExtensions()["tenant"] != "t1" {
		t.Fatalf("message: %v", m)
	}

	raw := ops.MessagePB(&jetstream.RawStreamMsg{
		Subject: "bp.hello._redrive.w.c", Data: []byte{0xff},
		Header: nats.Header{"ce-type": {"hello.Greeted"}},
	})
	if raw.GetData() != "" || len(raw.GetRaw()) != 1 || raw.GetEvent() != "hello.Greeted" {
		t.Fatalf("raw: %v", raw)
	}
}

func TestDeadLetterPB(t *testing.T) {
	t.Parallel()

	d := ops.DeadLetterPB(&jetstream.RawStreamMsg{
		Subject: "bp.dlq.watcher.sink", Sequence: 3, Data: []byte(`{}`),
		Header: nats.Header{
			"ce-type": {"hello.Greeted"}, "bp-error": {"boom"}, "bp-consumer": {"sink"}, "bp-delivered": {"5"},
		},
	})
	if d.GetConsumer() != "sink" || d.GetError() != "boom" || d.GetDelivered() != 5 ||
		d.GetEventSubject() != "bp.hello.Greeted" || d.GetMessage().GetSeq() != 3 {
		t.Fatalf("dead letter: %v", d)
	}

	if c, ok := ops.ConsumerOfDead("bp.dlq.watcher.send_3Ahello_2EGreeted"); !ok || c != "send:hello.Greeted" {
		t.Fatalf("consumer of dead: %q %v", c, ok)
	}

	if _, ok := ops.ConsumerOfDead("bp.dlq.watcher"); ok {
		t.Fatal("short subject")
	}
}

// TestRedriveMsg: a redriven dead letter keeps the event's headers and
// payload, drops the dead letter's own, goes to the reactor's redrive
// subject and is deduplicated per dead letter.
func TestRedriveMsg(t *testing.T) {
	t.Parallel()

	m := ops.RedriveMsg("hello", "watcher", "sink", &jetstream.RawStreamMsg{
		Subject: "bp.dlq.watcher.sink", Sequence: 42, Data: []byte(`{"a":1}`),
		Header: nats.Header{
			"ce-id": {"id-1"}, "ce-type": {"hello.Greeted"}, "bp-error": {"boom"}, "bp-consumer": {"sink"},
			"bp-delivered": {"5"}, "Nats-Msg-Id": {"old"}, "traceparent": {"00-x"},
		},
	})

	h := m.Header
	if m.Subject != "bp.hello._redrive.watcher.sink" || string(m.Data) != `{"a":1}` ||
		h.Get("ce-id") != "id-1" || h.Get("traceparent") != "00-x" || h.Get("bp-error") != "" ||
		h.Get("bp-consumer") != "" || h.Get("bp-delivered") != "" || h.Get("bp-redriven") != "42" ||
		h.Get("Nats-Msg-Id") != "redrive:watcher__sink:42" {
		t.Fatalf("redrive: %s %v", m.Subject, h)
	}
}

func TestEventFilter(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ service, event, want string }{
		{"hello", "", "bp.hello.*"},
		{"hello", "Greeted", "bp.hello.Greeted"},
		{"hello", "hello.Greeted", "bp.hello.Greeted"},
		{"hello", "other.Greeted", ""},
		{"hello", "greeted", ""},
	} {
		got, err := ops.EventFilter(c.service, c.event)
		if got != c.want || (err != nil) != (c.want == "") {
			t.Errorf("%s %s: %q %v", c.service, c.event, got, err)
		}
	}
}

func TestRunsQuery(t *testing.T) {
	t.Parallel()

	query, err := ops.RunsQuery(&consolev1.ListRunsRequest{
		Service: "hello", Workflow: "Greet", Status: consolev1.RunStatus_RUN_STATUS_RUNNING, WorkflowIdPrefix: "hello/Nightly-",
	}, "backplane")
	if err != nil {
		t.Fatal(err)
	}

	if want := "TaskQueue = 'hello' AND WorkflowType = 'Greet' AND ExecutionStatus = 'Running' AND " +
		"WorkflowId STARTS_WITH 'hello/Nightly-'"; query != want {
		t.Fatalf("query:\n%s\n%s", query, want)
	}

	// Hook calls: the service's hooks queue and the console's calls of its
	// hooks on backplane's queue.
	query, err = ops.RunsQuery(&consolev1.ListRunsRequest{Service: "hello", Hooks: true}, "backplane")
	if want := "(TaskQueue = 'hello.hooks' OR (TaskQueue = 'backplane' AND WorkflowId STARTS_WITH 'hook/hello/'))"; err != nil || query != want {
		t.Fatalf("hooks query:\n%s\n%s %v", query, want, err)
	}

	if query, err = ops.RunsQuery(&consolev1.ListRunsRequest{Service: "hello", Hooks: true}, ""); err != nil || query != "TaskQueue = 'hello.hooks'" {
		t.Fatalf("hooks query without console queue: %s %v", query, err)
	}

	for _, bad := range []*consolev1.ListRunsRequest{
		{},
		{Service: "Hello"},
		{Service: "hello", Workflow: "x' OR 1=1"},
		{Service: "hello", WorkflowIdPrefix: `a\`},
	} {
		if _, err := ops.RunsQuery(bad, "backplane"); !errors.Is(err, ops.ErrInput) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

func TestPayloadJSON(t *testing.T) {
	t.Parallel()

	for want, p := range map[string]*commonpb.Payload{
		`{"a":1}`: {Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte(`{"a":1}`)},
		`{"b":2}`: {Metadata: map[string][]byte{"encoding": []byte("json/protobuf")}, Data: []byte(`{"b":2}`)},
		"null":    {Metadata: map[string][]byte{"encoding": []byte("binary/null")}},
		`"AAE="`:  {Metadata: map[string][]byte{"encoding": []byte("binary/plain")}, Data: []byte{0, 1}},
		`"e30="`:  {Metadata: map[string][]byte{"encoding": []byte("binary/protobuf")}, Data: []byte("{}")},
	} {
		if got := ops.PayloadJSON(p); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}

// TestPayloadJSONEnvelopes: backplane's envelopes show the JSON their
// payload carries instead of its base64; other messages and payloads that
// are not JSON stay as they are.
func TestPayloadJSONEnvelopes(t *testing.T) {
	t.Parallel()

	encode := func(msg proto.Message) *commonpb.Payload {
		p, err := converter.GetDefaultDataConverter().ToPayload(msg)
		if err != nil {
			t.Fatal(err)
		}

		return p
	}

	for want, p := range map[string]*commonpb.Payload{
		`{"activity":"hello.Welcome","binding":"console","payload":{"name":"Ada"},"step":"console"}`: encode(&backplanev1.ActivityCall{
			Activity: "hello.Welcome", Payload: []byte(`{"name":"Ada"}`), Binding: "console", Step: "console",
		}),
		`{"payload":{"text":"Welcome, Ada!"}}`:   encode(&backplanev1.ActivityResult{Payload: []byte(`{"text":"Welcome, Ada!"}`)}),
		`{"hook":"hello.Greet","payload":[1,2]}`: encode(&backplanev1.HookCall{Hook: "hello.Greet", Payload: []byte(`[1,2]`)}),
		`{"payload":"text"}`:                     encode(&backplanev1.HookResult{Payload: []byte(`"text"`)}),
	} {
		if got := ops.PayloadJSON(p); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}

	// protojson's spacing varies by build: compare with the data itself.
	for _, other := range []*commonpb.Payload{
		encode(&backplanev1.Manifest{Service: "hello", Version: "1.0.0"}), encode(&backplanev1.HookResult{Payload: []byte{0, 1}}),
	} {
		if got := ops.PayloadJSON(other); got != string(other.GetData()) {
			t.Errorf("left as is: %s", got)
		}
	}
}

func TestHistoryPB(t *testing.T) {
	t.Parallel()

	jsonIn := &commonpb.Payloads{Payloads: []*commonpb.Payload{
		{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte(`"x"`)},
	}}

	scheduled := ops.HistoryPB(&historypb.HistoryEvent{
		EventId: 5, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED,
		Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{
			ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{
				ActivityType: &commonpb.ActivityType{Name: "Echo"}, Input: jsonIn,
				TaskQueue: nil,
			},
		},
	})
	if scheduled.GetId() != 5 || scheduled.GetType() != "ActivityTaskScheduled" ||
		!strings.HasPrefix(scheduled.GetSummary(), "Echo on ") || scheduled.GetPayload() != `"x"` {
		t.Fatalf("scheduled: %v", scheduled)
	}

	failed := ops.HistoryPB(&historypb.HistoryEvent{
		EventId: 6, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_FAILED,
		Attributes: &historypb.HistoryEvent_ActivityTaskFailedEventAttributes{
			ActivityTaskFailedEventAttributes: &historypb.ActivityTaskFailedEventAttributes{
				Failure: &failurepb.Failure{Message: "activity error", Cause: &failurepb.Failure{Message: "hello.Echo: boom"}},
			},
		},
	})
	if failed.GetFailure() != "hello.Echo: boom" {
		t.Fatalf("failed: %v", failed)
	}

	child := ops.HistoryPB(&historypb.HistoryEvent{
		EventId: 7, EventType: enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_STARTED,
		Attributes: &historypb.HistoryEvent_ChildWorkflowExecutionStartedEventAttributes{
			ChildWorkflowExecutionStartedEventAttributes: &historypb.ChildWorkflowExecutionStartedEventAttributes{
				WorkflowType:      &commonpb.WorkflowType{Name: "Flow"},
				WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: "child-1", RunId: "run-c"},
			},
		},
	})
	if child.GetSummary() != "Flow child-1" || child.GetWorkflowId() != "child-1" || child.GetRunId() != "run-c" {
		t.Fatalf("child: %v", child)
	}

	terminated := ops.HistoryPB(&historypb.HistoryEvent{
		EventId: 8, EventType: enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_TERMINATED,
		Attributes: &historypb.HistoryEvent_ChildWorkflowExecutionTerminatedEventAttributes{
			ChildWorkflowExecutionTerminatedEventAttributes: &historypb.ChildWorkflowExecutionTerminatedEventAttributes{
				WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: "child-1", RunId: "run-c"},
			},
		},
	})
	if terminated.GetSummary() != "child-1" || terminated.GetRunId() != "run-c" {
		t.Fatalf("terminated child: %v", terminated)
	}

	continued := ops.HistoryPB(&historypb.HistoryEvent{
		EventId: 9, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CONTINUED_AS_NEW,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionContinuedAsNewEventAttributes{
			WorkflowExecutionContinuedAsNewEventAttributes: &historypb.WorkflowExecutionContinuedAsNewEventAttributes{
				NewExecutionRunId: "run-2", Input: jsonIn,
			},
		},
	})
	if continued.GetRunId() != "run-2" || continued.GetSummary() != "continued as run run-2" || continued.GetPayload() != `"x"` {
		t.Fatalf("continued: %v", continued)
	}
}

// TestCallInputs: a hook call and an activity run take the declared
// defaults; undeclared names and bad input are refused.
func TestCallInputs(t *testing.T) {
	t.Parallel()

	o := ops.Detached(fixture(t))

	id, call, err := o.HookCallOf(t.Context(), &consolev1.CallHookRequest{Hook: "hello.Ask", Key: "k1"})
	if err != nil {
		t.Fatal(err)
	}

	if id != "hook/hello/Ask/k1" || call.GetHook() != "hello.Ask" || string(call.GetPayload()) != "{}" ||
		call.GetDeadline().AsDuration() != 3*time.Second || call.GetInstance() != "admin" {
		t.Fatalf("hook call: %s %v", id, call)
	}

	id, call, err = o.HookCallOf(t.Context(), &consolev1.CallHookRequest{Hook: "hello.Tell", Input: `{"a":1}`})
	if err != nil || !strings.HasPrefix(id, "hook/hello/Tell/console-") || call.GetDeadline().AsDuration() != 30*time.Second {
		t.Fatalf("hook call defaults: %s %v %v", id, call, err)
	}

	for _, req := range []*consolev1.CallHookRequest{{Hook: "hello.Nope"}, {Hook: "hello"}, {Hook: "hello.Ask", Input: "{"}} {
		if _, _, err := o.HookCallOf(t.Context(), req); err == nil {
			t.Errorf("%v: accepted", req)
		}
	}

	echo, err := o.ActivityRunOf(&consolev1.RunActivityRequest{Activity: "hello.Echo"})
	if err != nil {
		t.Fatal(err)
	}

	if echo.Workflow || echo.StartToClose != 5*time.Second || echo.MaxAttempts != 1 || echo.Step != "console" ||
		string(echo.Payload) != "{}" {
		t.Fatalf("echo: %+v", echo)
	}

	flow, err := o.ActivityRunOf(&consolev1.RunActivityRequest{
		Activity: "hello.Flow", StartToClose: durationpb.New(time.Second), MaxAttempts: 3,
	})
	if err != nil || !flow.Workflow || flow.StartToClose != time.Second || flow.MaxAttempts != 3 {
		t.Fatalf("flow: %+v %v", flow, err)
	}

	if _, err := o.ActivityRunOf(&consolev1.RunActivityRequest{Activity: "hello.Nope"}); !errors.Is(err, ops.ErrNotDeclared) {
		t.Fatalf("undeclared: %v", err)
	}
}
