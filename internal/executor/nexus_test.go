package executor_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nexus-rpc/sdk-go/nexus"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/executor"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/wire"
)

// fakeBindings is the bindings in force by hook.
type fakeBindings struct {
	bound map[string]bindings.Binding
	err   error
}

func (f fakeBindings) Active(_ context.Context, hook string) (bindings.Binding, int64, error) {
	if f.err != nil {
		return bindings.Binding{}, 0, f.err
	}

	b, ok := f.bound[hook]
	if !ok {
		return bindings.Binding{}, 0, fmt.Errorf("%w for %s", bindings.ErrNoBinding, hook)
	}

	return b, 7, nil
}

func (f fakeBindings) Version(ctx context.Context, hook string, _ int64) (bindings.BindingVersion, error) {
	b, v, err := f.Active(ctx, hook)

	return bindings.BindingVersion{Hook: hook, Version: v, Binding: b}, err
}

// hub publishes installation, plus iam 0.9.0 — run by instance iam-old —
// where Audit is required and Legacy exists.
func hub() *registry.Hub {
	services := map[string]registry.Service{}

	for _, m := range installation() {
		services[m.GetService()] = registry.Service{
			Name: m.GetService(), Manifests: map[string]*backplanev1.Manifest{m.GetVersion(): m},
		}
	}

	iam := services["iam"]
	iam.Manifests["0.9.0"] = &backplanev1.Manifest{
		Service: "iam", Version: "0.9.0",
		Hooks: []*backplanev1.Hook{{Name: "Audit", Required: true}, {Name: "Legacy"}},
	}
	iam.Instances = []registry.Instance{
		{ID: "iam-new", State: &backplanev1.InstanceState{Version: "1.0.0"}},
		{ID: "iam-old", State: &backplanev1.InstanceState{Version: "0.9.0"}},
	}
	services["iam"] = iam

	h := registry.NewHub()
	h.Publish(services)

	return h
}

func TestHooksOfEveryVersion(t *testing.T) {
	t.Parallel()

	got := executor.Hooks(hub().Current())
	if len(got) != 1 || !slices.Equal(got["iam"], []string{"Audit", "Legacy", "SendEmail"}) {
		t.Errorf("hooks: %v", got)
	}
}

func TestRequired(t *testing.T) {
	t.Parallel()

	x := executor.Detached(fakeBindings{}, hub())

	for _, tc := range []struct {
		hook, instance string
		want           bool
	}{
		{"iam.SendEmail", "iam-new", true},
		{"iam.Audit", "iam-new", false},
		{"iam.Audit", "", false},       // the latest manifest
		{"iam.Audit", "iam-old", true}, // the caller's own version
		{"iam.Legacy", "iam-new", false},
		{"iam.Nope", "", true},
		{"ghost.Hook", "", true},
	} {
		if got := x.Required(tc.hook, tc.instance); got != tc.want {
			t.Errorf("%s from %q: %v", tc.hook, tc.instance, got)
		}
	}
}

func TestPrepare(t *testing.T) {
	t.Parallel()

	b, err := bindings.BindingYAML("hook: iam.SendEmail\nsteps: {send: {activity: smtp.Send, input: {to: req.to}}}\nresult: {id: send.id}")
	if err != nil {
		t.Fatal(err)
	}

	bad := b
	bad.Hook = "iam.Audit"
	bad.Steps = []bindings.Step{{Name: "send", Activity: "smtp.Gone"}}

	ctx := t.Context()
	x := executor.Detached(fakeBindings{bound: map[string]bindings.Binding{"iam.SendEmail": b, "iam.Audit": bad}}, hub())
	trace := map[string]string{"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}

	in, err := x.Prepare(ctx, "iam.SendEmail", &backplanev1.HookCall{
		Hook: "iam.SendEmail", Instance: "iam-new", Payload: []byte(`{"to":"a@b"}`), Trace: trace,
	})
	if err != nil {
		t.Fatal(err)
	}

	if in.Kind != executor.KindBinding || in.Identity.String() != "iam.SendEmail@7" ||
		string(in.Payload) != `{"to":"a@b"}` || in.Trace["traceparent"] == "" {
		t.Errorf("input: %+v", in)
	}

	var p bindings.Program
	if err := json.Unmarshal(in.Program, &p); err != nil || p.Source() != "iam.SendEmail" || len(p.Steps()) != 1 {
		t.Errorf("program: %v %v", p.Steps(), err)
	}

	// No binding: required fails with backplane.NoBinding; optional is {}.
	unbound := executor.Detached(fakeBindings{}, hub())

	_, err = unbound.Prepare(ctx, "iam.SendEmail", &backplanev1.HookCall{Instance: "iam-new"})
	if app := appError(t, err); app.Type() != wire.NoBindingType || app.Message() != "no binding for iam.SendEmail" ||
		!app.NonRetryable() {
		t.Errorf("required: %v", app)
	}

	if _, err := unbound.Prepare(ctx, "iam.Audit", &backplanev1.HookCall{Instance: "iam-new"}); !errors.Is(err, executor.ErrDefault) {
		t.Errorf("optional: %v", err)
	}

	// A binding the manifests no longer accept.
	_, err = x.Prepare(ctx, "iam.Audit", &backplanev1.HookCall{})
	if app := appError(t, err); app.Type() != wire.HookFailedType || !strings.Contains(app.Message(), "does not compile") {
		t.Errorf("invalid: %v", app)
	}

	// Input that is not JSON.
	_, err = x.Prepare(ctx, "iam.SendEmail", &backplanev1.HookCall{Payload: []byte("nope")})
	if app := appError(t, err); !strings.Contains(app.Message(), "not JSON") {
		t.Errorf("not JSON: %v", app)
	}

	// The store down: retryable.
	down := executor.Detached(fakeBindings{err: errors.New("connection refused")}, hub())

	_, err = down.Prepare(ctx, "iam.SendEmail", &backplanev1.HookCall{})

	var he *nexus.HandlerError
	if !errors.As(err, &he) || he.Type != nexus.HandlerErrorTypeUnavailable {
		t.Errorf("store down: %v", err)
	}
}

func TestPrepareNotSynced(t *testing.T) {
	t.Parallel()

	b, err := bindings.BindingYAML("hook: iam.SendEmail\nsteps: {send: {activity: smtp.Send, input: req}}")
	if err != nil {
		t.Fatal(err)
	}

	x := executor.Detached(fakeBindings{bound: map[string]bindings.Binding{"iam.SendEmail": b}}, registry.NewHub())

	var he *nexus.HandlerError
	if _, err := x.Prepare(t.Context(), "iam.SendEmail", &backplanev1.HookCall{}); !errors.As(err, &he) ||
		he.Type != nexus.HandlerErrorTypeUnavailable {
		t.Errorf("not synced: %v", err)
	}
}

func TestRunIDs(t *testing.T) {
	t.Parallel()

	if id := wire.BindingRunID("iam.SendEmail", "req-1"); id != "binding/iam.SendEmail/req-1" {
		t.Errorf("run id: %s", id)
	}

	for _, tc := range []struct {
		id         string
		hook       string
		test, isOK bool
	}{
		{"binding/iam.SendEmail/req-1", "iam.SendEmail", false, true},
		{wire.TestRunID("iam.SendEmail", "u"), "iam.SendEmail", true, true},
		{"test/rule/0b5e/u", "", false, false},
		{"rule/0b5e/ev", "", false, false},
		{"binding/iam.SendEmail", "", false, false},
		{"hook/iam/SendEmail/x", "", false, false},
	} {
		hook, test, ok := wire.BindingRunHook(tc.id)
		if hook != tc.hook || test != tc.test || ok != tc.isOK {
			t.Errorf("%s: %q %v %v", tc.id, hook, test, ok)
		}
	}
}

func TestIdentity(t *testing.T) {
	t.Parallel()

	for id, want := range map[executor.Identity]string{
		{Hook: "iam.SendEmail", Version: 3}: "iam.SendEmail@3",
		{Hook: "iam.SendEmail", Test: true}: "iam.SendEmail@draft",
		{Rule: "0b5e", Version: 2}:          "rule:0b5e@2",
	} {
		if got := id.String(); got != want {
			t.Errorf("%+v: %s", id, got)
		}
	}
}

// ---- timeline ---------------------------------------------------------------

func payloads(t *testing.T, v proto.Message) *commonpb.Payloads {
	t.Helper()

	p, err := converter.GetDefaultDataConverter().ToPayloads(v)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func scheduled(t *testing.T, id int64, actID, step, activity, input string) *historypb.HistoryEvent {
	t.Helper()

	return &historypb.HistoryEvent{
		EventId: id, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED,
		Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{
			ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{
				ActivityId: actID,
				Input:      payloads(t, &backplanev1.ActivityCall{Activity: activity, Step: step, Payload: []byte(input)}),
			},
		},
	}
}

func completedEvent(t *testing.T, id, sched int64, output string) *historypb.HistoryEvent {
	t.Helper()

	return &historypb.HistoryEvent{
		EventId: id, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED,
		Attributes: &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{
			ActivityTaskCompletedEventAttributes: &historypb.ActivityTaskCompletedEventAttributes{
				ScheduledEventId: sched, Result: payloads(t, &backplanev1.ActivityResult{Payload: []byte(output)}),
			},
		},
	}
}

func TestTimeline(t *testing.T) {
	t.Parallel()

	p := program(t, `
hook: iam.SendEmail
steps:
  render: {activity: template.Exec, input: req, undo: smtp.Recall}
  send: {activity: smtp.Send, input: {text: render.text}}
  audit: {activity: billing.Charge, input: req, after: [send]}
  late: {activity: template.Lookup, input: req, after: [send]}
`)

	prog, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	appFailure := temporal.GetDefaultFailureConverter().ErrorToFailure(
		temporal.NewApplicationError("smtp.Send: refused", "Refused"))

	events := []*historypb.HistoryEvent{
		scheduled(t, 5, "5", "render", "template.Exec", `{}`),
		{EventId: 6, Attributes: &historypb.HistoryEvent_ActivityTaskStartedEventAttributes{
			ActivityTaskStartedEventAttributes: &historypb.ActivityTaskStartedEventAttributes{ScheduledEventId: 5, Attempt: 2},
		}},
		completedEvent(t, 7, 5, `{"text":"body"}`),
		scheduled(t, 8, "8", "send", "smtp.Send", `{"text":"body"}`),
		{EventId: 9, Attributes: &historypb.HistoryEvent_ActivityTaskFailedEventAttributes{
			ActivityTaskFailedEventAttributes: &historypb.ActivityTaskFailedEventAttributes{
				ScheduledEventId: 8, Failure: &failurepb.Failure{Message: "activity error", Cause: appFailure},
			},
		}},
		scheduled(t, 10, "10", "render", "smtp.Recall", `{"text":"body"}`),
		scheduled(t, 11, "11", "late", "template.Lookup", `{}`),
	}

	got := executor.TimelineOf(prog, events, []*consolev1.PendingActivity{
		{ActivityId: "11", State: "STARTED", Attempt: 3, LastFailure: "busy"},
	})

	type row struct {
		step, activity string
		undo           bool
		status         consolev1.StepRunStatus
	}

	want := []row{
		{"audit", "billing.Charge", false, consolev1.StepRunStatus_STEP_RUN_STATUS_NOT_RUN},
		{"late", "template.Lookup", false, consolev1.StepRunStatus_STEP_RUN_STATUS_STARTED},
		{"render", "template.Exec", false, consolev1.StepRunStatus_STEP_RUN_STATUS_COMPLETED},
		{"send", "smtp.Send", false, consolev1.StepRunStatus_STEP_RUN_STATUS_FAILED},
		{"render", "smtp.Recall", true, consolev1.StepRunStatus_STEP_RUN_STATUS_SCHEDULED},
	}

	if len(got) != len(want) {
		t.Fatalf("timeline: %v", got)
	}

	for i, w := range want {
		g := got[i]
		if g.GetStep() != w.step || g.GetActivity() != w.activity || g.GetUndo() != w.undo || g.GetStatus() != w.status {
			t.Errorf("%d: %v, want %+v", i, g, w)
		}
	}

	if got[2].GetOutput() != `{"text":"body"}` || got[2].GetAttempt() != 2 {
		t.Errorf("render: %v", got[2])
	}

	if got[3].GetError() != "smtp.Send: refused" || got[3].GetErrorType() != "Refused" {
		t.Errorf("send: %v", got[3])
	}

	if got[1].GetAttempt() != 3 || got[1].GetError() != "busy" {
		t.Errorf("late: %v", got[1])
	}
}

// Calls of a for-each step's items and body carry their item and parent;
// the step's row is its calls in scheduling order.
func TestTimelineItems(t *testing.T) {
	t.Parallel()

	p := program(t, `
hook: iam.SendEmail
steps:
  send: {forEach: req.to, activity: smtp.Send, input: {to: item}}
  pay:
    forEach: req.users
    steps: {charge: {activity: billing.Charge, input: {v: item}}}
`)

	prog, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	got := executor.TimelineOf(prog, []*historypb.HistoryEvent{
		scheduled(t, 5, "5", "send[1]", "smtp.Send", `{}`),
		scheduled(t, 6, "6", "pay[0].charge", "billing.Charge", `{}`),
		scheduled(t, 7, "7", "send[0]", "smtp.Send", `{}`),
	}, nil)

	rows := make([]string, 0, len(got))
	for _, run := range got {
		rows = append(rows, fmt.Sprintf("%s/%s[%d]", run.GetParent(), run.GetStep(), run.GetItem()))
	}

	// Steps by name: pay (its body call), then send (both items).
	if want := []string{"pay/charge[0]", "/send[1]", "/send[0]"}; !slices.Equal(rows, want) {
		t.Fatalf("rows %v, want %v", rows, want)
	}
}
