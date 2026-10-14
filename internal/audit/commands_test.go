package audit //nolint:testpackage // verifies the audit metadata allowlist without external dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/store"
)

func TestCommandResultsAndRedaction(t *testing.T) {
	t.Parallel()

	failed := &consolev1.CallResult{Error: "secret payload", ErrorType: "secret type", WorkflowId: "flow", RunId: "run"}

	cases := []struct {
		name     string
		response any
		err      error
		outcome  string
	}{
		{"hook failure", &consolev1.CallHookResponse{Result: failed}, nil, "failed"},
		{"activity failure", &consolev1.RunActivityResponse{Result: failed}, nil, "failed"},
		{"binding failure", &consolev1.TestBindingResponse{Result: failed}, nil, "failed"},
		{"rule failure", &consolev1.TestRuleResponse{Result: failed}, nil, "failed"},
		{"evaluation failure", &consolev1.TestRuleResponse{Error: "secret expression"}, nil, "failed"},
		{"dry rule no match", &consolev1.TestRuleResponse{}, nil, "succeeded"},
		{"validation", &consolev1.TestRuleResponse{Violations: []*consolev1.Violation{{Message: "secret"}}}, nil, "rejected"},
		{"partial redrive", &consolev1.RedriveDeadLettersResponse{Redriven: 1, Failed: []*consolev1.SeqError{{Error: "secret"}}}, nil, "partial"},
		{"failed purge", &consolev1.PurgeDeadLettersResponse{Failed: []*consolev1.SeqError{{Error: "secret"}}}, nil, "failed"},
		{"timeout uncertain", nil, status.Error(codes.DeadlineExceeded, "secret"), "unknown"},
		{"invalid input", nil, status.Error(codes.InvalidArgument, "secret"), "rejected"},
		{"already running", nil, status.Error(codes.AlreadyExists, "secret"), "rejected"},
		{"successful output", &consolev1.CallHookResponse{Result: &consolev1.CallResult{Output: "secret"}}, nil, "succeeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			outcome, detail := commandResult(store.AuditDetail{}, tc.response, tc.err)
			if outcome != tc.outcome {
				t.Fatalf("got %s, want %s", outcome, tc.outcome)
			}

			encoded, err := json.Marshal(detail)
			if err != nil || strings.Contains(string(encoded), "secret") {
				t.Fatalf("unsafe detail: %s, %v", encoded, err)
			}

			if result, ok := tc.response.(interface{ GetResult() *consolev1.CallResult }); ok && result.GetResult() == failed {
				if detail.WorkflowID != "flow" || detail.RunID != "run" {
					t.Fatal("lost execution identifiers", detail)
				}
			}
		})
	}
}

func TestCommandSubjectExcludesPayload(t *testing.T) {
	t.Parallel()

	if got := commandSubject(&consolev1.TestRuleRequest{Id: "rule", Event: `{"secret":"token"}`}); got != "id=rule" {
		t.Fatal("rule payload copied into audit", got)
	}

	if got := commandSubject(&consolev1.PublishTestEventRequest{Event: "hello.Greeted", Payload: "secret"}); got != "event=hello.Greeted" {
		t.Fatal(got)
	}

	if got := commandSubject(&consolev1.RunActivityRequest{Activity: "formatter.Format", Input: "secret"}); got != "activity=formatter.Format" {
		t.Fatal(got)
	}
}

// TestCommandDetail: run and schedule commands keep the run they address
// and the operator's words — signal name, reason, note, bounded — never a
// signal's argument; the response's run survives the result.
func TestCommandDetail(t *testing.T) {
	t.Parallel()

	signal := commandDetail(&consolev1.SignalRunRequest{WorkflowId: "w", RunId: "r", Signal: "go", Input: `{"secret":1}`})

	encoded, err := json.Marshal(signal)
	if err != nil || strings.Contains(string(encoded), "secret") || signal.Signal != "go" || signal.WorkflowID != "w" || signal.RunID != "r" {
		t.Fatalf("signal: %s %v", encoded, err)
	}

	if d := commandDetail(&consolev1.TerminateRunRequest{WorkflowId: "w", Reason: "stuck"}); d.Reason != "stuck" || d.WorkflowID != "w" {
		t.Fatalf("terminate: %+v", d)
	}

	for _, req := range []any{
		&consolev1.PauseScheduleRequest{Service: "hello", Name: "Nightly", Note: "maintenance"},
		&consolev1.UnpauseScheduleRequest{Service: "hello", Name: "Nightly", Note: "maintenance"},
	} {
		if d := commandDetail(req); d.Note != "maintenance" {
			t.Fatalf("%T: %+v", req, d)
		}
	}

	if d := commandDetail(&consolev1.TerminateRunRequest{Reason: strings.Repeat("я", 300)}); len([]rune(d.Reason)) != maxWords+1 {
		t.Fatalf("unbounded reason: %d", len([]rune(d.Reason)))
	}

	outcome, d := commandResult(commandDetail(&consolev1.SignalRunRequest{WorkflowId: "w", RunId: "r", Signal: "go"}),
		&consolev1.SignalRunResponse{}, nil)
	if outcome != "succeeded" || d.Signal != "go" || d.WorkflowID != "w" || d.RunID != "r" {
		t.Fatalf("result: %s %+v", outcome, d)
	}
}

func TestCommandService(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		request any
		service string
	}{
		{"workflow start", &consolev1.StartWorkflowRequest{Service: "hello", Workflow: "GreetMany"}, "hello"},
		{"schedule", &consolev1.PauseScheduleRequest{Service: "hello", Name: "HourlyReport"}, "hello"},
		{"dead letters", &consolev1.RedriveDeadLettersRequest{Subscriber: "formatter", Consumer: "c"}, "formatter"},
		{"hook call", &consolev1.CallHookRequest{Hook: "hello.Greet"}, "hello"},
		{"activity run", &consolev1.RunActivityRequest{Activity: "formatter.Format"}, "formatter"},
		{"test event", &consolev1.PublishTestEventRequest{Event: "hello.Greeted"}, "hello"},
		{"unsaved binding test", &consolev1.TestBindingRequest{Definition: &consolev1.BindingDefinition{Hook: "hello.Greet"}}, "hello"},
		{"unsaved rule test", &consolev1.TestRuleRequest{Definition: &consolev1.RuleDefinition{Event: "hello.Greeted"}}, "hello"},
		{"binding run", &consolev1.CancelBindingRunRequest{WorkflowId: "binding/hello.Greet/request"}, "hello"},
		{"binding test run", &consolev1.CancelBindingRunRequest{WorkflowId: "test/hello.Greet/request"}, "hello"},
		{"console start", &consolev1.CancelRunRequest{WorkflowId: "console/hello/GreetMany/0b5e"}, "hello"},
		{"console activity", &consolev1.TerminateRunRequest{WorkflowId: "console/activity/formatter/Format/0b5e"}, "formatter"},
		{"hook call", &consolev1.SignalRunRequest{WorkflowId: "hook/hello/Greet/key", Signal: "go"}, "hello"},
		{"schedule run", &consolev1.CancelRunRequest{WorkflowId: "hello/HourlyReport-2026-09-30T12:00:00Z"}, "hello"},
		{"plain run", &consolev1.CancelRunRequest{WorkflowId: "orders-42", RunId: "run"}, ""},
		{"draft rule test", &consolev1.CancelRunRequest{WorkflowId: "test/rule/draft/0b5e"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := (&Service{}).commandService(t.Context(), tc.request); got != tc.service {
				t.Fatalf("got %q, want %q", got, tc.service)
			}
		})
	}
}

// TestCommandServiceByQueue: a run whose id says nothing is the service of
// its task queue — a hook call's <service>.hooks too — when Temporal
// answers; a schedule's run id is the fallback when it does not.
func TestCommandServiceByQueue(t *testing.T) {
	t.Parallel()

	queues := map[string]string{"orders-42": "orders", "call-7": "hello.hooks", "odd": "Not A Queue", "hello/Nightly-1": "billing"}
	svc := &Service{runQueue: func(_ context.Context, workflowID, _ string) (string, error) {
		if queue, ok := queues[workflowID]; ok {
			return queue, nil
		}

		return "", errors.New("not found")
	}}

	for id, want := range map[string]string{
		"orders-42": "orders", "call-7": "hello", "odd": "", "hello/Nightly-1": "billing",
		"hello/Report-2026": "hello", "gone": "", "console/hello/GreetMany/x": "hello",
	} {
		if got := svc.commandService(t.Context(), &consolev1.CancelRunRequest{WorkflowId: id}); got != want {
			t.Errorf("%s: got %q, want %q", id, got, want)
		}
	}
}
