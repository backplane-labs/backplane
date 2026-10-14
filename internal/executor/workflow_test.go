package executor_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/executor"
	"github.com/backplane-labs/backplane/internal/wire"
)

// installation: iam raises SendEmail and publishes UserRegistered;
// template, smtp and billing implement activities (no schemas: every
// value is dyn). Short activity names are unique across services, as the
// test environment registers them in one namespace.
func installation() bindings.Manifests {
	act := func(name string) *backplanev1.Activity {
		return &backplanev1.Activity{Name: name, Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY}
	}

	return bindings.Manifests{
		{
			Service: "iam", Version: "1.0.0",
			Hooks:  []*backplanev1.Hook{{Name: "SendEmail", Required: true}, {Name: "Audit"}},
			Events: []*backplanev1.Event{{Name: "UserRegistered"}},
		},
		{Service: "template", Version: "1.0.0", Activities: []*backplanev1.Activity{act("Exec"), act("Lookup")}},
		{
			Service: "smtp", Version: "1.0.0",
			Activities: []*backplanev1.Activity{
				act("Send"), act("Recall"),
				{Name: "Archive", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW},
			},
		},
		{Service: "billing", Version: "1.0.0", Activities: []*backplanev1.Activity{act("Charge"), act("Refund")}},
	}
}

// program compiles a binding (YAML) against installation.
func program(t *testing.T, text string) *bindings.Program {
	t.Helper()

	b, err := bindings.BindingYAML(text)
	if err != nil {
		t.Fatalf("binding: %v", err)
	}

	p, err := bindings.CompileBinding(b, installation())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	return p
}

// calls records the envelopes the activities got, in call order.
type calls struct {
	mu   sync.Mutex
	list []*backplanev1.ActivityCall
}

func (c *calls) add(call *backplanev1.ActivityCall) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.list = append(c.list, call)
}

func (c *calls) steps() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]string, 0, len(c.list))
	for _, call := range c.list {
		out = append(out, call.GetStep()+":"+call.GetActivity())
	}

	return out
}

func (c *calls) of(step string) *backplanev1.ActivityCall {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, call := range c.list {
		if call.GetStep() == step {
			return call
		}
	}

	return nil
}

type handler func(in map[string]any) (any, error)

// env is a test workflow environment with the binding workflow and the
// activities by name.
func env(t *testing.T, rec *calls, acts map[string]handler) *testsuite.TestWorkflowEnvironment {
	t.Helper()

	var suite testsuite.WorkflowTestSuite

	e := suite.NewTestWorkflowEnvironment()
	executor.RegisterWorkflow(e)

	for name, h := range acts {
		e.RegisterActivityWithOptions(func(_ context.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
			rec.add(call)

			return invoke(h, call)
		}, activity.RegisterOptions{Name: name})
	}

	return e
}

func invoke(h handler, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
	var in map[string]any
	if err := json.Unmarshal(call.GetPayload(), &in); err != nil {
		return nil, fmt.Errorf("input: %w", err)
	}

	out, err := h(in)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("output: %w", err)
	}

	return &backplanev1.ActivityResult{Payload: data}, nil
}

func run(t *testing.T, e *testsuite.TestWorkflowEnvironment, p *bindings.Program, req string) (string, error) {
	t.Helper()

	in, err := executor.BindingInput(p, executor.Identity{Hook: p.Source(), Version: 3}, []byte(req),
		map[string]string{"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"})
	if err != nil {
		t.Fatal(err)
	}

	e.ExecuteWorkflow(wire.BindingWorkflow, in)

	if !e.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}

	if err := e.GetWorkflowError(); err != nil {
		return "", err
	}

	var res backplanev1.HookResult
	if err := e.GetWorkflowResult(&res); err != nil {
		t.Fatal(err)
	}

	return string(res.GetPayload()), nil
}

// appError is the run's application error.
func appError(t *testing.T, err error) *temporal.ApplicationError {
	t.Helper()

	var app *temporal.ApplicationError
	if !errors.As(err, &app) {
		t.Fatalf("not an application error: %v", err)
	}

	return app
}

func TestSequentialStepsAndResult(t *testing.T) {
	t.Parallel()

	rec := &calls{}
	e := env(t, rec, map[string]handler{
		"Exec": func(in map[string]any) (any, error) {
			return map[string]any{"subject": "Hi " + in["name"].(string), "text": "body"}, nil
		},
		"Send": func(in map[string]any) (any, error) {
			return map[string]any{"id": "m-1", "size": 42, "to": in["to"]}, nil
		},
	})

	p := program(t, `
hook: iam.SendEmail
steps:
  render: {activity: template.Exec, input: {name: req.name}}
  send: {activity: smtp.Send, input: {to: req.to, subject: render.subject, text: render.text}}
result: {message_id: send.id, size: send.size}
`)

	out, err := run(t, e, p, `{"name":"Ann","to":"ann@example.com"}`)
	if err != nil {
		t.Fatal(err)
	}

	if out != `{"message_id":"m-1","size":42}` {
		t.Errorf("result: %s", out)
	}

	if got := strings.Join(rec.steps(), ","); got != "render:template.Exec,send:smtp.Send" {
		t.Errorf("calls: %s", got)
	}

	send := rec.of("send")
	if send.GetBinding() != "iam.SendEmail@3" || send.GetTrace()["traceparent"] == "" ||
		string(send.GetPayload()) != `{"subject":"Hi Ann","text":"body","to":"ann@example.com"}` {
		t.Errorf("envelope: %v", send)
	}
}

// Independent steps run at once: each waits until the other has started.
func TestParallelGroup(t *testing.T) {
	t.Parallel()

	var (
		arrived sync.WaitGroup
		both    = make(chan struct{})
	)

	arrived.Add(2)

	go func() {
		arrived.Wait()
		close(both)
	}()

	rendezvous := func(out any) handler {
		return func(map[string]any) (any, error) {
			arrived.Done()

			select {
			case <-both:
				return out, nil
			case <-time.After(5 * time.Second):
				return nil, temporal.NewNonRetryableApplicationError("the other step never started", "test", nil)
			}
		}
	}

	rec := &calls{}
	e := env(t, rec, map[string]handler{
		"Exec":   rendezvous(map[string]any{"text": "a"}),
		"Lookup": rendezvous(map[string]any{"text": "b"}),
		"Send": func(in map[string]any) (any, error) {
			return map[string]any{"id": in["text"]}, nil
		},
	})

	p := program(t, `
hook: iam.SendEmail
steps:
  a: {activity: template.Exec, input: req}
  b: {activity: template.Lookup, input: req}
  send: {activity: smtp.Send, input: {text: a.text + b.text}}
result: {message_id: send.id}
`)

	if groups := p.Groups(); len(groups) != 2 || len(groups[0]) != 2 {
		t.Fatalf("groups: %v", groups)
	}

	out, err := run(t, e, p, `{}`)
	if err != nil || out != `{"message_id":"ab"}` {
		t.Fatalf("result: %s %v", out, err)
	}
}

// A step starts once its own dependencies are done, not when its whole
// level is: late waits for a, which waits until quick's follower ran.
func TestDependencyScheduling(t *testing.T) {
	t.Parallel()

	followed := make(chan struct{})
	rec := &calls{}
	e := env(t, rec, map[string]handler{
		"Exec": func(map[string]any) (any, error) {
			select {
			case <-followed:
				return map[string]any{"text": "slow"}, nil
			case <-time.After(5 * time.Second):
				return nil, errors.New("the follower of the quick step never started")
			}
		},
		"Lookup": func(map[string]any) (any, error) { return map[string]any{"text": "quick"}, nil },
		"Send": func(in map[string]any) (any, error) {
			if in["text"] == "quick" {
				close(followed)
			}

			return map[string]any{"id": in["text"]}, nil
		},
		"Charge": func(in map[string]any) (any, error) { return map[string]any{"id": in["v"]}, nil },
	})

	p := program(t, `
hook: iam.SendEmail
steps:
  slow: {activity: template.Exec, input: req}
  quick: {activity: template.Lookup, input: req}
  follow: {activity: smtp.Send, input: {text: quick.text}}
  late: {activity: billing.Charge, input: {v: slow.text + follow.id}}
result: {message_id: late.id}
`)

	out, err := run(t, e, p, `{}`)
	if err != nil || out != `{"message_id":"slowquick"}` {
		t.Fatalf("result: %s %v", out, err)
	}
}

func TestWhenSkips(t *testing.T) {
	t.Parallel()

	rec := &calls{}
	e := env(t, rec, map[string]handler{
		"Charge": func(map[string]any) (any, error) { return map[string]any{"id": "c-1"}, nil },
		"Send":   func(in map[string]any) (any, error) { return map[string]any{"id": in["text"]}, nil },
	})

	p := program(t, `
hook: iam.SendEmail
steps:
  charge: {activity: billing.Charge, input: req, when: req.priority > 0}
  send: {activity: smtp.Send, input: {text: 'steps.charge.skipped ? "free" : charge.id'}, after: [charge]}
result: {message_id: send.id}
`)

	out, err := run(t, e, p, `{"priority":0}`)
	if err != nil || out != `{"message_id":"free"}` {
		t.Fatalf("skipped: %s %v", out, err)
	}

	if got := strings.Join(rec.steps(), ","); got != "send:smtp.Send" {
		t.Errorf("calls: %s", got)
	}
}

// A failed step compensates the steps done, newest first, with their undo
// inputs, and the run fails readably.
func TestUndoOnFailure(t *testing.T) {
	t.Parallel()

	rec := &calls{}
	e := env(t, rec, map[string]handler{
		"Charge": func(map[string]any) (any, error) { return map[string]any{"id": "c-1"}, nil },
		"Exec":   func(map[string]any) (any, error) { return map[string]any{"text": "body"}, nil },
		"Refund": func(map[string]any) (any, error) { return map[string]any{}, nil },
		"Recall": func(map[string]any) (any, error) { return map[string]any{}, nil },
		"Send": func(map[string]any) (any, error) {
			return nil, temporal.NewNonRetryableApplicationError("smtp.Send: connection refused", "", nil)
		},
	})

	p := program(t, `
hook: iam.SendEmail
steps:
  charge:
    activity: billing.Charge
    input: req
    undo: billing.Refund
    undoInput: {id: charge.id, reason: "'rollback'"}
  render: {activity: template.Exec, input: req, after: [charge], undo: smtp.Recall}
  send: {activity: smtp.Send, input: {text: render.text}}
result: {message_id: send.id}
`)

	_, err := run(t, e, p, `{}`)

	app := appError(t, err)
	if app.Type() != wire.StepFailedType || app.Message() != "step send: smtp.Send: connection refused" {
		t.Errorf("error: %s %q", app.Type(), app.Message())
	}

	want := "charge:billing.Charge,render:template.Exec,send:smtp.Send,render:smtp.Recall,charge:billing.Refund"
	if got := strings.Join(rec.steps(), ","); got != want {
		t.Errorf("calls:\n got %s\nwant %s", got, want)
	}

	rec.mu.Lock()
	recall, refund := rec.list[3], rec.list[4]
	rec.mu.Unlock()

	if string(recall.GetPayload()) != `{"text":"body"}` || string(refund.GetPayload()) != `{"id":"c-1","reason":"rollback"}` {
		t.Errorf("undo inputs: %s %s", recall.GetPayload(), refund.GetPayload())
	}
}

// A retryable failure is retried by the step's policy before the run
// fails; the handler's plain message gets the activity's name.
func TestRetriesThenFails(t *testing.T) {
	t.Parallel()

	rec := &calls{}
	e := env(t, rec, map[string]handler{
		"Send": func(map[string]any) (any, error) { return nil, errors.New("try later") },
	})

	p := program(t, `
hook: iam.SendEmail
steps:
  send: {activity: smtp.Send, input: req, retry: {attempts: 2, initialInterval: 0.010s}}
`)

	_, err := run(t, e, p, `{}`)

	app := appError(t, err)
	if app.Message() != "step send: smtp.Send: try later" || app.NonRetryable() != true {
		t.Errorf("error: %q", app.Message())
	}

	if n := len(rec.steps()); n != 2 {
		t.Errorf("attempts: %d", n)
	}
}

func TestEvalError(t *testing.T) {
	t.Parallel()

	rec := &calls{}
	e := env(t, rec, map[string]handler{
		"Charge": func(map[string]any) (any, error) { return map[string]any{"id": "c-1"}, nil },
		"Refund": func(map[string]any) (any, error) { return map[string]any{}, nil },
		"Exec":   func(map[string]any) (any, error) { return map[string]any{}, nil },
	})

	p := program(t, `
hook: iam.SendEmail
steps:
  charge: {activity: billing.Charge, input: req, undo: billing.Refund}
  render: {activity: template.Exec, input: {name: req.missing.name}, after: [charge]}
`)

	_, err := run(t, e, p, `{}`)

	app := appError(t, err)
	if app.Type() != wire.TransformFailedType ||
		!strings.HasPrefix(app.Message(), "transform failed at step render (input.name): ") {
		t.Errorf("error: %s %q", app.Type(), app.Message())
	}

	// The charge done before the failed expression is compensated.
	if got := strings.Join(rec.steps(), ","); got != "charge:billing.Charge,charge:billing.Refund" {
		t.Errorf("calls: %s", got)
	}
}

func TestResultEvalError(t *testing.T) {
	t.Parallel()

	e := env(t, &calls{}, map[string]handler{
		"Charge": func(map[string]any) (any, error) { return map[string]any{"id": "c-1"}, nil },
	})

	p := program(t, `
hook: iam.SendEmail
steps:
  charge: {activity: billing.Charge, input: req}
result: {message_id: charge.nope}
`)

	_, err := run(t, e, p, `{}`)
	if app := appError(t, err); !strings.HasPrefix(app.Message(), "transform failed at result.message_id: ") {
		t.Errorf("error: %q", app.Message())
	}
}

// An activity of kind WORKFLOW runs as a child workflow by name with the
// same envelope.
func TestChildWorkflowStep(t *testing.T) {
	t.Parallel()

	rec := &calls{}
	e := env(t, rec, map[string]handler{
		"Send": func(in map[string]any) (any, error) { return map[string]any{"id": "m-" + in["text"].(string)}, nil },
	})
	e.RegisterWorkflowWithOptions(func(_ workflow.Context, call *backplanev1.ActivityCall) (*backplanev1.ActivityResult, error) {
		rec.add(call)

		return invoke(func(in map[string]any) (any, error) {
			return map[string]any{"ref": "arch-" + in["id"].(string)}, nil
		}, call)
	}, workflow.RegisterOptions{Name: "Archive"})

	p := program(t, `
hook: iam.SendEmail
steps:
  send: {activity: smtp.Send, input: {text: "'x'"}}
  archive: {activity: smtp.Archive, input: {id: send.id}}
result: {message_id: archive.ref}
`)

	out, err := run(t, e, p, `{}`)
	if err != nil || out != `{"message_id":"arch-m-x"}` {
		t.Fatalf("result: %s %v", out, err)
	}

	if call := rec.of("archive"); call.GetActivity() != "smtp.Archive" || call.GetBinding() != "iam.SendEmail@3" {
		t.Errorf("envelope: %v", call)
	}
}

// A rule's run evaluates `event` and `meta` and returns no payload.
func TestRuleRun(t *testing.T) {
	t.Parallel()

	rec := &calls{}
	e := env(t, rec, map[string]handler{
		"Send": func(in map[string]any) (any, error) { return map[string]any{"id": in["to"]}, nil },
	})

	r, err := bindings.RuleYAML(`
event: iam.UserRegistered
when: event.email != ""
steps:
  send: {activity: smtp.Send, input: {to: event.email, text: meta.id}}
`)
	if err != nil {
		t.Fatal(err)
	}

	p, err := bindings.CompileRule(r, installation())
	if err != nil {
		t.Fatal(err)
	}

	in, err := executor.RuleInput(p, executor.Identity{Rule: "0b5e", Version: 2}, []byte(`{"email":"a@b"}`),
		executor.EventMeta{ID: "ev-1", Type: "iam.UserRegistered"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	e.ExecuteWorkflow(wire.BindingWorkflow, in)

	if err := e.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}

	send := rec.of("send")
	if send.GetBinding() != "rule:0b5e@2" || string(send.GetPayload()) != `{"text":"ev-1","to":"a@b"}` {
		t.Errorf("envelope: %v", send)
	}
}

func TestBadProgram(t *testing.T) {
	t.Parallel()

	e := env(t, &calls{}, nil)
	e.ExecuteWorkflow(wire.BindingWorkflow, executor.Input{Kind: executor.KindBinding, Program: json.RawMessage(`{"kind":1,"steps":[{"name":"s","when_expr":")("}]}`)})

	if app := appError(t, e.GetWorkflowError()); app.Type() != wire.TransformFailedType {
		t.Errorf("error: %v", app)
	}
}

// Items run one call each, labeled by position; with onError continue a
// failed item is null in the list and counted, and the run goes on.
func TestForEachActivities(t *testing.T) {
	t.Parallel()

	rec := &calls{}
	e := env(t, rec, map[string]handler{
		"Send": func(in map[string]any) (any, error) {
			if in["to"] == "bad" {
				return nil, errors.New("refused")
			}

			return map[string]any{"id": in["to"]}, nil
		},
	})

	p := program(t, `
hook: iam.SendEmail
steps:
  send:
    forEach: req.to
    as: who
    when: who != "skip"
    concurrency: 2
    onError: continue
    activity: smtp.Send
    input: {to: who}
    retry: {attempts: 1}
result: {ids: send, failed: steps.send.failed, first: "steps.send.errors[0].index"}
`)

	out, err := run(t, e, p, `{"to":["a","bad","skip","b"]}`)
	if err != nil || out != `{"failed":1,"first":1,"ids":[{"id":"a"},null,null,{"id":"b"}]}` {
		t.Fatalf("result: %s %v", out, err)
	}

	got := rec.steps()
	slices.Sort(got)

	if want := []string{"send[0]:smtp.Send", "send[1]:smtp.Send", "send[3]:smtp.Send"}; !slices.Equal(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

// A body is a sub-flow per item. A failed item undoes its own work at once;
// the run's failure then undoes the items that succeeded.
func TestForEachBodyCompensates(t *testing.T) {
	t.Parallel()

	rec := &calls{}
	e := env(t, rec, map[string]handler{
		"Charge": func(in map[string]any) (any, error) { return map[string]any{"id": "c-" + in["v"].(string)}, nil },
		"Refund": func(map[string]any) (any, error) { return map[string]any{}, nil },
		"Send": func(in map[string]any) (any, error) {
			if in["text"] == "c-y" {
				return nil, errors.New("refused")
			}

			return map[string]any{"id": in["text"]}, nil
		},
	})

	p := program(t, `
hook: iam.SendEmail
steps:
  pay:
    forEach: req.users
    concurrency: 1
    steps:
      charge: {activity: billing.Charge, input: {v: item}, undo: billing.Refund}
      send: {activity: smtp.Send, input: {text: charge.id}, retry: {attempts: 1}}
    result: {id: send.id}
`)

	_, err := run(t, e, p, `{"users":["x","y"]}`)

	app := appError(t, err)
	if !strings.HasPrefix(app.Message(), "step pay[1].send: smtp.Send: ") {
		t.Fatalf("error: %q", app.Message())
	}

	var refunds []string

	for _, call := range rec.list {
		if call.GetActivity() == "billing.Refund" {
			refunds = append(refunds, call.GetStep()+":"+string(call.GetPayload()))
		}
	}

	// y's own charge first (its body failed), then x's (the run failed).
	want := []string{`pay[1].charge:{"id":"c-y"}`, `pay[0].charge:{"id":"c-x"}`}
	if !slices.Equal(refunds, want) {
		t.Fatalf("refunds %v, want %v", refunds, want)
	}
}

func TestForEachCancellationCompensates(t *testing.T) {
	t.Parallel()

	for _, policy := range []string{"fail", "continue"} {
		t.Run(policy, func(t *testing.T) {
			t.Parallel()

			rec := &calls{}
			e := env(t, rec, map[string]handler{
				"Charge": func(map[string]any) (any, error) { return map[string]any{"id": "paid"}, nil },
				"Refund": func(map[string]any) (any, error) { return map[string]any{}, nil },
				"Send":   func(map[string]any) (any, error) { return map[string]any{}, nil },
			})
			e.OnActivity("Send", mock.Anything, mock.Anything).After(time.Hour).Return(&backplanev1.ActivityResult{Payload: []byte(`{}`)}, nil)
			e.RegisterDelayedCallback(e.CancelWorkflow, time.Second)

			p := program(t, `
hook: iam.SendEmail
steps:
  pay:
    forEach: req.users
    concurrency: 1
    onError: `+policy+`
    steps:
      charge: {activity: billing.Charge, input: {v: item}, undo: billing.Refund}
      send: {activity: smtp.Send, input: {text: charge.id}, retry: {attempts: 1}}
    result: {id: send.id}
`)

			_, err := run(t, e, p, `{"users":["x","y"]}`)
			if !temporal.IsCanceledError(err) {
				t.Fatalf("expected canceled workflow: %v", err)
			}

			if got := rec.steps(); !slices.Equal(got, []string{"pay[0].charge:billing.Charge", "pay[0].charge:billing.Refund"}) {
				t.Fatalf("must compensate the completed item and not start the next: %v", got)
			}
		})
	}
}
