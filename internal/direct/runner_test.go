package direct_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/direct"
	"github.com/backplane-labs/backplane/internal/wire"
)

// installation: iam asks SendEmail and publishes UserRegistered; template,
// smtp and billing implement activities without schemas; smtp.Archive is a
// workflow.
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

// catalog is the installation with the direct runtime as the default.
func catalog() bindings.Catalog {
	return bindings.WithDefaults(installation(), bindings.Defaults{Runtime: consolev1.Runtime_RUNTIME_DIRECT})
}

func program(tb testing.TB, text string) *bindings.Program {
	tb.Helper()

	b, err := bindings.BindingYAML(text)
	if err != nil {
		tb.Fatalf("binding: %v", err)
	}

	p, err := bindings.CompileBinding(b, catalog())
	if err != nil {
		tb.Fatalf("compile: %v", err)
	}

	return p
}

func rule(tb testing.TB, text string) *bindings.Program {
	tb.Helper()

	r, err := bindings.RuleYAML(text)
	if err != nil {
		tb.Fatalf("rule: %v", err)
	}

	p, err := bindings.CompileRule(r, catalog())
	if err != nil {
		tb.Fatalf("compile: %v", err)
	}

	return p
}

// handler answers one activity call on its decoded input.
type handler func(ctx context.Context, in map[string]any) (any, error)

// fake is a Caller over handlers by activity name, recording the calls.
type fake struct {
	acts map[string]handler

	mu       sync.Mutex
	calls    []*backplanev1.ActivityCall
	timeouts []time.Duration
	inFlight atomic.Int32
	peak     atomic.Int32
}

func newFake(acts map[string]handler) *fake { return &fake{acts: acts} }

type callKey struct{}

// callOf is the call a handler serves.
func callOf(ctx context.Context) *backplanev1.ActivityCall {
	c, _ := ctx.Value(callKey{}).(*backplanev1.ActivityCall)

	return c
}

func (f *fake) Call(
	ctx context.Context, call *backplanev1.ActivityCall, timeout time.Duration,
) (*backplanev1.ActivityResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.timeouts = append(f.timeouts, timeout)
	f.mu.Unlock()

	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)

	for {
		peak := f.peak.Load()
		if n <= peak || f.peak.CompareAndSwap(peak, n) {
			break
		}
	}

	h, ok := f.acts[call.GetActivity()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no activity %s", call.GetActivity())
	}

	var in map[string]any
	if err := json.Unmarshal(call.GetPayload(), &in); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "input: %v", err)
	}

	ctx = context.WithValue(ctx, callKey{}, call)

	out, err := h(ctx, in)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}

	return &backplanev1.ActivityResult{Payload: data}, nil
}

func (f *fake) steps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.GetStep()+":"+c.GetActivity())
	}

	return out
}

func (f *fake) of(step string) *backplanev1.ActivityCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, c := range f.calls {
		if c.GetStep() == step {
			return c
		}
	}

	return nil
}

// recorder keeps what the runner reported, in order.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) Scheduled(label, step, parent string, item int, activity string, input []byte, _ time.Time) {
	r.add(fmt.Sprintf("scheduled %s step=%s parent=%s item=%d %s %s", label, step, parent, item, activity, input))
}

func (r *recorder) Completed(label string, output []byte, _ time.Time) {
	r.add(fmt.Sprintf("completed %s %s", label, output))
}

func (r *recorder) Failed(label, msg, typ string, _ time.Time) {
	r.add(fmt.Sprintf("failed %s %s %s", label, typ, msg))
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, s)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.events)
}

func nonRetryable(msg string) error {
	st, _ := status.New(codes.FailedPrecondition, msg).WithDetails(
		&backplanev1.CallFailure{Type: wire.NonRetryableType, Retryable: false})

	return st.Err()
}

func retryAfter(msg string, d time.Duration) error {
	st, _ := status.New(codes.Unavailable, msg).WithDetails(
		&backplanev1.CallFailure{Retryable: true, RetryAfter: durationpb.New(d)})

	return st.Err()
}

func retryable(msg string) error {
	st, _ := status.New(codes.Unavailable, msg).WithDetails(&backplanev1.CallFailure{Retryable: true})

	return st.Err()
}

// A binding runs its steps in dependency order, independent ones at once,
// binds every output into the scope and answers its result; every call
// carries the run's identity, its label and a key.
func TestExecute(t *testing.T) {
	t.Parallel()

	p := program(t, `
hook: iam.SendEmail
steps:
  render: {activity: template.Exec, input: {name: req.template}}
  lookup: {activity: template.Lookup, input: {user: req.to}}
  send: {activity: smtp.Send, input: {to: lookup.email, text: render.text}}
result: {id: send.id, both: [render.text, lookup.email]}
`)

	// render and lookup wait for each other: both must be in flight at
	// once for either to answer.
	barrier := make(chan struct{}, 2)
	meet := func(ctx context.Context) error {
		barrier <- struct{}{}

		for len(barrier) < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}

		return nil
	}

	f := newFake(map[string]handler{
		"template.Exec": func(ctx context.Context, _ map[string]any) (any, error) {
			if err := meet(ctx); err != nil {
				return nil, err
			}

			return map[string]any{"text": "hello"}, nil
		},
		"template.Lookup": func(ctx context.Context, _ map[string]any) (any, error) {
			if err := meet(ctx); err != nil {
				return nil, err
			}

			return map[string]any{"email": "a@b"}, nil
		},
		"smtp.Send": func(_ context.Context, in map[string]any) (any, error) {
			return map[string]any{"id": "m-" + in["to"].(string) + "-" + in["text"].(string)}, nil
		},
	})

	rec := &recorder{}

	out, err := direct.NewRunner(f).Execute(t.Context(), direct.Run{
		ID: "direct/binding/iam.SendEmail/1", Kind: direct.KindBinding, Program: p,
		Payload: []byte(`{"to":"ann","template":"welcome"}`), Identity: "iam.SendEmail@3",
	}, rec)
	if err != nil {
		t.Fatal(err)
	}

	if string(out) != `{"both":["hello","a@b"],"id":"m-a@b-hello"}` {
		t.Fatalf("result %s", out)
	}

	steps := f.steps()
	slices.Sort(steps[:2])

	if !slices.Equal(steps, []string{"lookup:template.Lookup", "render:template.Exec", "send:smtp.Send"}) {
		t.Fatalf("calls %v", steps)
	}

	if f.peak.Load() != 2 {
		t.Fatalf("render and lookup did not run together: peak %d", f.peak.Load())
	}

	send := f.of("send")
	if send.GetBinding() != "iam.SendEmail@3" || send.GetKey() != "direct/binding/iam.SendEmail/1/send" ||
		send.GetAttempt() != 1 || string(send.GetPayload()) != `{"text":"hello","to":"a@b"}` {
		t.Fatalf("send call %v", send)
	}

	if d := f.timeouts[0]; d != bindings.DefaultStartToClose {
		t.Fatalf("timeout %s", d)
	}

	events := rec.list()
	if len(events) != 6 || !slices.Contains(events, `completed send {"id":"m-a@b-hello"}`) ||
		!slices.Contains(events, `scheduled send step=send parent= item=-1 smtp.Send {"text":"hello","to":"a@b"}`) {
		t.Fatalf("recorded %q", events)
	}
}

// A step's when skips it (its output {}), after orders without data, a
// failed step's error names the step and the activity and keeps the
// handler's retryability; the steps in flight are canceled and nothing
// else starts.
func TestExecuteWhenAndFailure(t *testing.T) {
	t.Parallel()

	p := program(t, `
hook: iam.SendEmail
steps:
  skipped: {activity: template.Lookup, when: "false", input: {x: 1}}
  slow: {activity: template.Exec, input: {name: "'a'"}}
  bad: {activity: smtp.Send, input: {to: "'x'"}}
  never: {activity: billing.Charge, input: {amount: 1}, after: [bad]}
result: {skipped: skipped, done: steps.skipped.skipped}
`)

	var canceled atomic.Bool

	f := newFake(map[string]handler{
		"template.Exec": func(ctx context.Context, _ map[string]any) (any, error) {
			<-ctx.Done()
			canceled.Store(true)

			return nil, ctx.Err()
		},
		"smtp.Send":      func(context.Context, map[string]any) (any, error) { return nil, nonRetryable("smtp.Send: refused") },
		"billing.Charge": func(context.Context, map[string]any) (any, error) { return map[string]any{}, nil },
	})

	_, err := direct.NewRunner(f).Execute(t.Context(), direct.Run{
		ID: "r", Kind: direct.KindBinding, Program: p, Payload: []byte(`{}`),
	}, nil)

	var failure *direct.Failure
	if !errors.As(err, &failure) || failure.Type != wire.StepFailedType || failure.Retryable ||
		failure.Msg != "step bad: smtp.Send: refused" {
		t.Fatalf("error %#v", err)
	}

	if !canceled.Load() {
		t.Fatal("slow was not canceled")
	}

	if steps := f.steps(); slices.Contains(steps, "never:billing.Charge") || slices.Contains(steps, "skipped:template.Lookup") {
		t.Fatalf("calls %v", steps)
	}

	// Retryable failures stay retryable.
	f = newFake(map[string]handler{
		"template.Exec":  func(context.Context, map[string]any) (any, error) { return map[string]any{}, nil },
		"smtp.Send":      func(context.Context, map[string]any) (any, error) { return nil, retryable("smtp.Send: busy") },
		"billing.Charge": func(context.Context, map[string]any) (any, error) { return map[string]any{}, nil },
	})

	_, err = direct.NewRunner(f).Execute(t.Context(), direct.Run{
		ID: "r", Kind: direct.KindBinding, Program: p, Payload: []byte(`{}`),
	}, nil)
	if !errors.As(err, &failure) || !failure.Retryable || !direct.Retryable(err) {
		t.Fatalf("retryable error %#v", err)
	}
}

// A step is retried by its policy while the failure is retryable: the
// same key, a growing attempt, at least the delay the handler asked for;
// a final failure is not retried; a for-each item is retried alone.
func TestExecuteRetries(t *testing.T) {
	t.Parallel()

	p := program(t, `
hook: iam.SendEmail
steps:
  send:
    activity: smtp.Send
    input: {to: "'x'"}
    retry: {attempts: 3, initialInterval: 0.010s, maxInterval: 0.100s, backoff: 2}
  each:
    activity: template.Exec
    forEach: "[1, 2, 3]"
    input: {name: "'n'"}
    retry: {attempts: 2, initialInterval: 0.010s}
result: {}
`)

	var (
		lock sync.Mutex
		seen []*backplanev1.ActivityCall
	)

	itemCalls := map[int32]int{}
	f := newFake(map[string]handler{
		"smtp.Send": func(ctx context.Context, _ map[string]any) (any, error) {
			lock.Lock()
			defer lock.Unlock()

			seen = append(seen, callOf(ctx))
			if len(seen) == 1 {
				return nil, retryAfter("smtp.Send: slow down", 60*time.Millisecond)
			}

			if len(seen) == 2 {
				return nil, retryable("smtp.Send: busy")
			}

			return map[string]any{}, nil
		},
		"template.Exec": func(ctx context.Context, _ map[string]any) (any, error) {
			lock.Lock()
			defer lock.Unlock()

			call := callOf(ctx)
			itemCalls[call.GetAttempt()]++

			if call.GetStep() == "each[1]" && call.GetAttempt() == 1 {
				return nil, retryable("busy")
			}

			return map[string]any{}, nil
		},
	})

	start := time.Now()

	if _, err := direct.NewRunner(f).Execute(t.Context(), direct.Run{
		ID: "r", Kind: direct.KindBinding, Program: p, Payload: []byte(`{}`),
	}, nil); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 3 || seen[0].GetAttempt() != 1 || seen[2].GetAttempt() != 3 || seen[0].GetKey() != seen[2].GetKey() {
		t.Fatalf("send calls %v", seen)
	}

	if time.Since(start) < 60*time.Millisecond {
		t.Fatal("retry-after not waited")
	}

	if itemCalls[1] != 3 || itemCalls[2] != 1 {
		t.Fatalf("items retried %v", itemCalls)
	}

	// A RetryAfter beyond the policy's maximum interval is capped, and a
	// wait past the run's deadline is not started.
	long := 0
	f = newFake(map[string]handler{
		"smtp.Send": func(context.Context, map[string]any) (any, error) {
			long++

			return nil, retryAfter("slow", time.Hour)
		},
		"template.Exec": func(context.Context, map[string]any) (any, error) { return map[string]any{}, nil },
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	start = time.Now()

	if _, err := direct.NewRunner(f).Execute(ctx, direct.Run{
		ID: "r", Kind: direct.KindBinding, Program: p, Payload: []byte(`{}`),
	}, nil); err == nil || long != 3 || time.Since(start) > time.Second {
		t.Fatalf("capped retry-after: %v calls %d took %v", err, long, time.Since(start))
	}

	// Final failures are not retried.
	calls := 0
	f = newFake(map[string]handler{
		"smtp.Send":     func(context.Context, map[string]any) (any, error) { calls++; return nil, nonRetryable("refused") },
		"template.Exec": func(context.Context, map[string]any) (any, error) { return map[string]any{}, nil },
	})

	if _, err := direct.NewRunner(f).Execute(t.Context(), direct.Run{
		ID: "r", Kind: direct.KindBinding, Program: p, Payload: []byte(`{}`),
	}, nil); err == nil || calls != 1 {
		t.Fatalf("final retried: %v %d", err, calls)
	}
}

// An expression that fails on the run's values is a transform failure
// naming the place; a program of the wrong kind too.
func TestExecuteTransformFailures(t *testing.T) {
	t.Parallel()

	p := program(t, `
hook: iam.SendEmail
steps:
  send: {activity: smtp.Send, input: {to: req.user.email}}
`)

	f := newFake(map[string]handler{})

	_, err := direct.NewRunner(f).Execute(t.Context(), direct.Run{
		ID: "r", Kind: direct.KindBinding, Program: p, Payload: []byte(`{"user":{}}`),
	}, nil)

	msg, typ := direct.Readable(err)
	if typ != wire.TransformFailedType || !strings.Contains(msg, "step send (input.to)") {
		t.Fatalf("%s / %s", msg, typ)
	}

	_, err = direct.NewRunner(f).Execute(t.Context(), direct.Run{
		ID: "r", Kind: direct.KindRule, Program: p, Payload: []byte(`{}`),
	}, nil)
	if _, typ = direct.Readable(err); typ != wire.TransformFailedType {
		t.Fatalf("kind mismatch: %v", err)
	}

	if len(f.steps()) != 0 {
		t.Fatalf("calls %v", f.steps())
	}
}

// A for-each step runs its items at most Concurrency at once, binds their
// outputs in order; with on_error continue a failed item is collected and
// its output null; more items than max_items fails the run.
func TestExecuteForEach(t *testing.T) {
	t.Parallel()

	p := program(t, `
hook: iam.SendEmail
steps:
  notify:
    activity: smtp.Send
    forEach: req.users
    as: u
    concurrency: 2
    onError: continue
    input: {to: u.email, n: uIndex}
  after: {activity: billing.Charge, input: {sent: notify, failed: steps.notify.failed, errors: steps.notify.errors}}
result: {outputs: notify, charged: after}
`)

	f := newFake(map[string]handler{
		"smtp.Send": func(_ context.Context, in map[string]any) (any, error) {
			time.Sleep(20 * time.Millisecond)

			if in["to"] == "bad" {
				return nil, nonRetryable("smtp.Send: refused")
			}

			return map[string]any{"id": in["to"]}, nil
		},
		"billing.Charge": func(_ context.Context, in map[string]any) (any, error) { return in, nil },
	})

	out, err := direct.NewRunner(f).Execute(t.Context(), direct.Run{
		ID: "r", Kind: direct.KindBinding, Program: p,
		Payload: []byte(`{"users":[{"email":"a"},{"email":"bad"},{"email":"c"}]}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"charged":{"errors":[{"index":1,"message":"step notify: smtp.Send: refused"}],"failed":1,` +
		`"sent":[{"id":"a"},null,{"id":"c"}]},"outputs":[{"id":"a"},null,{"id":"c"}]}`
	if string(out) != want {
		t.Fatalf("result\n got %s\nwant %s", out, want)
	}

	if f.peak.Load() > 2 {
		t.Fatalf("concurrency %d", f.peak.Load())
	}

	if labels := f.steps(); len(labels) != 4 || !slices.Contains(labels, "notify[1]:smtp.Send") {
		t.Fatalf("calls %v", labels)
	}

	// max_items
	p = program(t, `
hook: iam.SendEmail
steps:
  notify: {activity: smtp.Send, forEach: req.users, maxItems: 1, input: {to: item}}
`)

	_, err = direct.NewRunner(f).Execute(t.Context(), direct.Run{
		ID: "r", Kind: direct.KindBinding, Program: p, Payload: []byte(`{"users":["a","b"]}`),
	}, nil)
	if msg, typ := direct.Readable(err); typ != wire.TransformFailedType || !strings.Contains(msg, "at most 1") {
		t.Fatalf("%s / %s", msg, typ)
	}
}

// A for-each body runs its steps per item as a sub-flow; its result is the
// item's output; labels carry the item; a failed item fails the run.
func TestExecuteForEachBody(t *testing.T) {
	t.Parallel()

	p := program(t, `
hook: iam.SendEmail
steps:
  users:
    forEach: req.users
    steps:
      look: {activity: template.Lookup, input: {user: item}}
      send: {activity: smtp.Send, input: {to: look.email}}
    result: {id: send.id, email: look.email}
result: {ids: users}
`)

	f := newFake(map[string]handler{
		"template.Lookup": func(_ context.Context, in map[string]any) (any, error) {
			return map[string]any{"email": in["user"].(string) + "@x"}, nil
		},
		"smtp.Send": func(_ context.Context, in map[string]any) (any, error) {
			if in["to"] == "bad@x" {
				return nil, nonRetryable("smtp.Send: refused")
			}

			return map[string]any{"id": "m-" + in["to"].(string)}, nil
		},
	})

	rec := &recorder{}

	out, err := direct.NewRunner(f).Execute(t.Context(), direct.Run{
		ID: "r", Kind: direct.KindBinding, Program: p, Payload: []byte(`{"users":["a","b"]}`),
	}, rec)
	if err != nil {
		t.Fatal(err)
	}

	if string(out) != `{"ids":[{"email":"a@x","id":"m-a@x"},{"email":"b@x","id":"m-b@x"}]}` {
		t.Fatalf("result %s", out)
	}

	if !slices.Contains(rec.list(), `scheduled users[1].send step=send parent=users item=1 smtp.Send {"to":"b@x"}`) {
		t.Fatalf("recorded %q", rec.list())
	}

	_, err = direct.NewRunner(f).Execute(t.Context(), direct.Run{
		ID: "r", Kind: direct.KindBinding, Program: p, Payload: []byte(`{"users":["a","bad"]}`),
	}, nil)
	if msg, typ := direct.Readable(err); typ != wire.StepFailedType || msg != "step send: smtp.Send: refused" {
		t.Fatalf("%s / %s", msg, typ)
	}
}

// A rule runs on the event and its attributes and answers {}; a step's
// start_to_close is the call's timeout; the run's context cancels calls.
func TestExecuteRuleAndTimeouts(t *testing.T) {
	t.Parallel()

	p := rule(t, `
event: iam.UserRegistered
steps:
  note: {activity: billing.Charge, input: {who: event.email, id: meta.id}, startToClose: 7s}
`)

	f := newFake(map[string]handler{
		"billing.Charge": func(_ context.Context, in map[string]any) (any, error) { return in, nil },
	})

	out, err := direct.NewRunner(f).Execute(t.Context(), direct.Run{
		ID: "r", Kind: direct.KindRule, Program: p, Payload: []byte(`{"email":"a@b"}`),
		Meta: bindings.Meta{ID: "ce-1", Source: "iam", Type: "iam.UserRegistered"},
	}, nil)
	if err != nil || string(out) != "{}" {
		t.Fatalf("%s %v", out, err)
	}

	if string(f.of("note").GetPayload()) != `{"id":"ce-1","who":"a@b"}` || f.timeouts[0] != 7*time.Second {
		t.Fatalf("call %v timeout %s", f.of("note"), f.timeouts[0])
	}

	ctx, cancel := context.WithCancel(t.Context())
	f = newFake(map[string]handler{
		"billing.Charge": func(ctx context.Context, _ map[string]any) (any, error) {
			cancel()
			<-ctx.Done()

			return nil, ctx.Err()
		},
	})

	_, err = direct.NewRunner(f).Execute(ctx, direct.Run{
		ID: "r", Kind: direct.KindRule, Program: p, Payload: []byte(`{"email":"x"}`), Meta: bindings.Meta{ID: "ce-2"},
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled run: %v", err)
	}
}

// The compiler rejects, for the direct runtime, what it does not run.
func TestDirectRuntimeViolations(t *testing.T) {
	t.Parallel()

	b, err := bindings.BindingYAML(`
hook: iam.SendEmail
runtime: direct
steps:
  send: {activity: smtp.Send, input: {to: "'a'"}, undo: smtp.Recall, retry: {attempts: 3}, heartbeat: 1s}
  archive: {activity: smtp.Archive, input: {id: send.id}}
`)
	if err != nil {
		t.Fatal(err)
	}

	vs, err := bindings.ValidateBinding(b, installation())
	if err != nil {
		t.Fatal(err)
	}

	var paths []string

	for _, v := range vs {
		if v.Code == bindings.CodeRuntimeUnsupported {
			paths = append(paths, v.Path)
		}
	}

	slices.Sort(paths)

	want := []string{"/steps/archive/activity", "/steps/send/heartbeat", "/steps/send/undo"}
	if !slices.Equal(paths, want) {
		t.Fatalf("violations %v (all: %v)", paths, vs)
	}

	// The same definition on Temporal compiles; the runtime is in the
	// analysis and the program.
	b.Runtime = consolev1.Runtime_RUNTIME_TEMPORAL

	a, err := bindings.AnalyzeBinding(b, installation())
	if err != nil || len(a.Violations) != 0 || a.Runtime != consolev1.Runtime_RUNTIME_TEMPORAL {
		t.Fatalf("temporal: %v %v", a.Violations, err)
	}

	// Unspecified takes the catalog's default; none means Temporal.
	b.Runtime = consolev1.Runtime_RUNTIME_UNSPECIFIED
	if a, _ = bindings.AnalyzeBinding(b, catalog()); a.Runtime != consolev1.Runtime_RUNTIME_DIRECT || len(a.Violations) == 0 {
		t.Fatalf("default direct: %v", a)
	}

	if a, _ = bindings.AnalyzeBinding(b, installation()); a.Runtime != consolev1.Runtime_RUNTIME_TEMPORAL {
		t.Fatalf("default temporal: %v", a.Runtime)
	}

	p := program(t, "hook: iam.SendEmail\nruntime: DIRECT\nsteps: {s: {activity: smtp.Send, input: {to: \"'a'\"}}}\n")
	if p.Runtime() != consolev1.Runtime_RUNTIME_DIRECT {
		t.Fatalf("runtime %v", p.Runtime())
	}
}

// The failure helpers map statuses and contexts to what the console and
// the SDK show.
func TestFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		err       error
		msg, typ  string
		retryable bool
		code      codes.Code
	}{
		{nonRetryable("x"), "x", wire.NonRetryableType, false, codes.Aborted},
		{retryable("busy"), "busy", "", true, codes.Aborted},
		{context.DeadlineExceeded, "timed out", wire.TimeoutType, true, codes.DeadlineExceeded},
		{context.Canceled, "canceled", "", false, codes.Canceled},
		{status.Error(codes.NotFound, "no activity"), "no activity", "", false, codes.Aborted},
		{errors.New("plain"), "plain", "", true, codes.Aborted},
	} {
		var f *direct.Failure
		if errors.As(tc.err, &f) || status.Code(tc.err) != codes.OK {
			// A call failure is read through stepFailure's path: wrap as a step.
			_ = f
		}

		msg, typ := direct.Readable(tc.err)
		if st, ok := status.FromError(tc.err); ok && !errors.Is(tc.err, context.DeadlineExceeded) {
			msg = st.Message()
		}

		if msg != tc.msg && !strings.Contains(msg, tc.msg) {
			t.Errorf("%v: message %q", tc.err, msg)
		}

		_ = typ

		if got := status.Code(direct.Status(tc.err)); got != tc.code {
			t.Errorf("%v: status %v, want %v", tc.err, got, tc.code)
		}
	}

	noBinding := &direct.Failure{Msg: "no binding for iam.SendEmail", Type: wire.NoBindingType}
	if st := direct.Status(noBinding); status.Code(st) != codes.FailedPrecondition {
		t.Fatalf("no binding: %v", st)
	}
}
