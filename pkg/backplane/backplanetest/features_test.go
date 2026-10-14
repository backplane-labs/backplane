package backplanetest_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/event"
	"github.com/backplane-labs/backplane/pkg/backplane/hook"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows/workflowtest"
)

// recordingTB keeps the cleanups and errors of a harness for the test to
// run and inspect.
type recordingTB struct {
	*testing.T

	mu       sync.Mutex
	errs     []string
	cleanups []func()
}

func (r *recordingTB) Cleanup(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cleanups = append(r.cleanups, fn)
}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *recordingTB) cleanup() []string {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.errs
}

func TestNameAndStopBudget(t *testing.T) {
	t.Parallel()

	rec := &recordingTB{T: t}
	h := backplanetest.New(rec, backplanetest.Name("greeter"), backplanetest.StopBudget(20*time.Millisecond))

	greeted := event.Declare[Greeted](h.Root(), "Greeted")
	if greeted.Name() != "greeter.Greeted" || h.Service() != "greeter" || h.Manifest().GetService() != "greeter" {
		t.Fatalf("name %q, service %q", greeted.Name(), h.Service())
	}

	release := make(chan struct{})
	defer close(release)

	// A goroutine that ignores its context outlives the budget.
	c := deps.NewComponent(h.Root(), "stuck")
	c.Go(func(context.Context) error { <-release; return nil })
	h.Start()

	if errs := rec.cleanup(); len(errs) != 1 || !strings.Contains(errs[0], "did not stop in time") {
		t.Fatalf("stop errors: %v", errs)
	}

	for _, bad := range []func(){
		func() { backplanetest.Name("Greeter") },
		func() { backplanetest.StopBudget(0) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()

			bad()
		}()
	}
}

func TestEventsWithMeta(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	ref := event.Declare[Greeted](h.Root(), "Greeted")
	h.Start()

	happened := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := ref.Publish(t.Context(), Greeted{Name: "ann"}, event.Key("ann"), event.ID("g-1"),
		event.Time(happened), event.Header("tenant", "t1")); err != nil {
		t.Fatal(err)
	}

	if err := ref.Publish(t.Context(), Greeted{Name: "bob"}); err != nil {
		t.Fatal(err)
	}

	got := backplanetest.EventsWithMeta(h, ref)
	if len(got) != 2 {
		t.Fatalf("events: %+v", got)
	}

	if first := got[0]; first.Value.Name != "ann" || first.Key != "ann" || first.ID != "g-1" ||
		!first.Time.Equal(happened) || first.Headers["tenant"] != "t1" {
		t.Errorf("first: %+v", first)
	}

	if second := got[1]; second.Key != "" || second.ID == "" || second.Time.IsZero() || len(second.Headers) != 0 {
		t.Errorf("second: %+v", second)
	}

	if vals := backplanetest.Events(h, ref); len(vals) != 2 || vals[1].Name != "bob" {
		t.Errorf("values: %+v", vals)
	}
}

func TestFailures(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	ref := event.Declare[Greeted](h.Root(), "Greeted")
	lookup := hook.Declare[LookupIn, LookupOut](h.Root(), "Lookup")
	backplanetest.Answer(h, lookup, func(context.Context, LookupIn) (LookupOut, error) { return LookupOut{Title: "x"}, nil })
	h.Start()

	errBroken := errors.New("broken")
	backplanetest.FailPublish(h, errBroken)

	if err := ref.Publish(t.Context(), Greeted{}); !errors.Is(err, errBroken) {
		t.Fatalf("FailPublish: %v", err)
	}

	backplanetest.FailPublish(h, nil)
	backplanetest.Unavailable(h)

	if err := ref.Publish(t.Context(), Greeted{}); !errors.Is(err, event.ErrUnavailable) {
		t.Fatalf("unavailable publish: %v", err)
	}

	if _, err := lookup.Call(t.Context(), LookupIn{}); !errors.Is(err, hook.ErrUnavailable) {
		t.Fatalf("unavailable call: %v", err)
	}

	backplanetest.Available(h)

	if err := ref.Publish(t.Context(), Greeted{Name: "ok"}); err != nil {
		t.Fatal(err)
	}

	if _, err := lookup.Call(t.Context(), LookupIn{}); err != nil {
		t.Fatal(err)
	}

	if got := backplanetest.Events(h, ref); len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("recorded while failing: %+v", got)
	}
}

func TestCallKey(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	lookup := hook.Declare[LookupIn, LookupOut](h.Root(), "Lookup")
	backplanetest.Answer(h, lookup, func(ctx context.Context, _ LookupIn) (LookupOut, error) {
		return LookupOut{Title: backplanetest.CallKey(ctx)}, nil
	})

	if out, err := lookup.Call(t.Context(), LookupIn{}, hook.Key("k1")); err != nil || out.Title != "k1" {
		t.Fatalf("keyed: %+v %v", out, err)
	}

	if out, err := lookup.Call(t.Context(), LookupIn{}); err != nil || out.Title != "" {
		t.Fatalf("unkeyed: %+v %v", out, err)
	}
}

func TestReactPinned(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t, backplanetest.Name("hello"))
	audit := deps.NewComponent(h.Root(), "audit")
	greeted := event.Declare[Greeted](h.Root(), "Greeted")

	deliveries := make(chan event.Delivery, 4)

	event.React(audit, greeted.Name(), func(ctx context.Context, _ Greeted) error {
		d, ok := event.DeliveryOf(ctx)
		if !ok {
			return errors.New("no delivery")
		}

		deliveries <- d

		return nil
	}, event.Consumer("greeted-audit"), event.MaxDeliver(3))
	h.Start()

	// Found by its event although the consumer is pinned.
	if err := backplanetest.React(t.Context(), h, "audit", "hello.Greeted", Greeted{Name: "ann"}); err != nil {
		t.Fatal(err)
	}

	d := <-deliveries
	if d.ID == "" || d.Source != "hello" || d.Type != "hello.Greeted" || d.Attempt != 1 ||
		d.Consumer != "greeted-audit" || d.Time.IsZero() || d.Extensions["version"] == "" || d.Extensions["instance"] == "" {
		t.Errorf("delivery: %+v", d)
	}

	if err := backplanetest.ReactConsumer(t.Context(), h, "greeted-audit", Greeted{Name: "bob"},
		backplanetest.WithKey("bob"), backplanetest.WithID("e-1"), backplanetest.WithAttempt(3),
		backplanetest.WithHeader("tenant", "t1")); err != nil {
		t.Fatal(err)
	}

	d = <-deliveries
	if d.Subject != "bob" || d.ID != "e-1" || d.Attempt != 3 || d.Extensions["tenant"] != "t1" {
		t.Errorf("delivery with options: %+v", d)
	}

	if err := backplanetest.ReactConsumer(t.Context(), h, "nobody", Greeted{}); !errors.Is(err, backplanetest.ErrNotDeclared) {
		t.Errorf("unknown consumer: %v", err)
	}
}

func TestReactAmbiguousPin(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	noop := func(context.Context, Greeted) error { return nil }
	event.React(h.Root(), "iam.UserRegistered", noop, event.Consumer("one"))
	event.React(h.Root(), "iam.UserRegistered", noop, event.Consumer("two"))
	h.Start()

	if err := backplanetest.React(t.Context(), h, "", "iam.UserRegistered", Greeted{}); !errors.Is(err, backplanetest.ErrNotDeclared) {
		t.Fatalf("two pinned reactors: %v", err)
	}

	if err := backplanetest.ReactConsumer(t.Context(), h, "two", Greeted{}); err != nil {
		t.Fatal(err)
	}
}

func TestActivityInfo(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	infos := make(chan activity.Info, 2)

	activity.Handle(h.Root(), "Probe", func(ctx context.Context, _ WelcomeIn) (WelcomeOut, error) {
		info, ok := activity.InfoOf(ctx)
		if !ok {
			return WelcomeOut{}, errors.New("no info")
		}

		activity.Heartbeat(ctx, "progress")

		infos <- info

		return WelcomeOut{}, nil
	})
	h.Start()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	for range 2 {
		if _, err := backplanetest.Activity[WelcomeIn, WelcomeOut](ctx, h, "Probe", WelcomeIn{}); err != nil {
			t.Fatal(err)
		}
	}

	first, second := <-infos, <-infos
	if first.Attempt != 1 || !strings.HasPrefix(first.Key, "backplanetest/Probe/") || first.Key == second.Key ||
		first.Deadline.IsZero() {
		t.Fatalf("infos: %+v %+v", first, second)
	}
}

func TestReady(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)

	var healthy atomic.Bool

	errDown := errors.New("db down")

	deps.NewDependency(h.Root(), deps.Func(func(context.Context) (int, error) { return 1, nil },
		deps.WithProbe(func(context.Context, int) error {
			if healthy.Load() {
				return nil
			}

			return errDown
		})), deps.Name("db"))
	// An optional dependency never counts, provided or not.
	deps.NewOptional(h.Root(), deps.Func(func(context.Context) (int, error) { return 0, errDown }), deps.Name("cache"))

	if err := backplanetest.Ready(h); !errors.Is(err, backplanetest.ErrNotReady) || !errors.Is(err, deps.ErrNotReady) {
		t.Fatalf("before start: %v", err)
	}

	h.Start()

	err := backplanetest.Ready(h)
	if !errors.Is(err, backplanetest.ErrNotReady) || !errors.Is(err, errDown) || !strings.Contains(err.Error(), "db") {
		t.Fatalf("failing probe: %v", err)
	}

	healthy.Store(true)

	if err := backplanetest.Ready(h); err != nil {
		t.Fatalf("healthy: %v", err)
	}
}

type (
	sectionConfig struct {
		Greeting config.Live[string] `json:"greeting" schemapb:"default=Hello"`
		Limit    int                 `json:"limit"    schemapb:"default=3"`
	}
	serviceConfig struct {
		Section sectionConfig `json:"section"`
		Name    string        `json:"name"    schemapb:"default=svc"`
	}
)

func (s sectionConfig) Validate() error {
	if s.Limit > 10 {
		return errors.New("limit over 10")
	}

	return nil
}

func TestConfig(t *testing.T) {
	t.Parallel()

	sec := backplanetest.Config[sectionConfig](t, nil)
	if sec.Greeting.Get() != "Hello" || sec.Limit != 3 {
		t.Fatalf("defaults: %q %d", sec.Greeting.Get(), sec.Limit)
	}

	svc := backplanetest.Config[serviceConfig](t, map[string]string{"SECTION_GREETING": "Hi", "NAME": "x"})
	if svc.Section.Greeting.Get() != "Hi" || svc.Name != "x" || svc.Section.Limit != 3 {
		t.Fatalf("vars: %q %q", svc.Section.Greeting.Get(), svc.Name)
	}

	if _, err := backplanetest.LoadConfig[serviceConfig](t.Context(), map[string]string{"SECTION_LIMIT": "11"}); err == nil ||
		!strings.Contains(err.Error(), "section: limit over 10") {
		t.Fatalf("validate: %v", err)
	}

	if _, err := backplanetest.LoadConfig[serviceConfig](t.Context(), map[string]string{"NAEM": "typo"}); err == nil {
		t.Fatal("an unknown name must fail")
	}
}

// The process environment is not read.
func TestConfigIgnoresEnvironment(t *testing.T) {
	t.Setenv("TEST_NAME", "from-os")
	t.Setenv("BACKPLANETEST_NAME", "from-os")

	if svc := backplanetest.Config[serviceConfig](t, nil); svc.Name != "svc" {
		t.Fatalf("read the process environment: %q", svc.Name)
	}
}

// greetFlow raises the hook from workflow code, then runs an activity the
// author registered and one declared with activity.Handle.
type greetFlow struct {
	lookup hook.Ref[LookupIn, LookupOut]
}

func (f greetFlow) Welcome(ctx workflow.Context, in WelcomeIn) (WelcomeOut, error) {
	who, err := workflows.CallHook(ctx, f.lookup, LookupIn(in))
	if err != nil {
		return WelcomeOut{}, err
	}

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})

	var upper string
	if err := workflow.ExecuteActivity(ctx, "Upper", who.Title).Get(ctx, &upper); err != nil {
		return WelcomeOut{}, err
	}

	var res backplanev1.ActivityResult
	if err := workflow.ExecuteActivity(ctx, "Stamp", &backplanev1.ActivityCall{
		Activity: "Stamp", Payload: []byte(`{"name":"` + upper + " " + in.Name + `"}`),
	}).Get(ctx, &res); err != nil {
		return WelcomeOut{}, err
	}

	return WelcomeOut{Text: string(res.GetPayload())}, nil
}

func TestWorkflows(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t, backplanetest.Name("hello"))
	flow := greetFlow{lookup: hook.Declare[LookupIn, LookupOut](h.Root(), "Lookup")}

	workflows.Declare(h.Root(), "Welcome", flow.Welcome)
	workflows.Register(h.Root(), func(r worker.Registry) {
		r.RegisterActivityWithOptions(func(_ context.Context, s string) (string, error) {
			return strings.ToUpper(s), nil
		}, tactivity.RegisterOptions{Name: "Upper"})
	})
	activity.Handle(h.Root(), "Stamp", func(ctx context.Context, in WelcomeIn) (WelcomeOut, error) {
		info, ok := activity.InfoOf(ctx)
		if !ok || info.Attempt != 1 || !strings.Contains(info.Key, "/") {
			return WelcomeOut{}, fmt.Errorf("info %+v", info)
		}

		return WelcomeOut{Text: "[" + in.Name + "]"}, nil
	})
	workflows.Activity(h.Root(), "Twice", func(ctx workflow.Context, in LookupIn) (LookupOut, error) {
		who, err := workflows.CallHook(ctx, flow.lookup, in)
		if errors.Is(err, hook.ErrNoBinding) {
			return LookupOut{Title: "none"}, nil
		}

		if err != nil {
			return LookupOut{}, err
		}

		return LookupOut{Title: who.Title + who.Title}, nil
	})
	h.Start()

	// Unanswered: no binding.
	w := workflowtest.New(t, h)
	w.ExecuteWorkflow("Welcome", WelcomeIn{Name: "Ann"})

	if err := w.GetWorkflowError(); err == nil || !strings.Contains(err.Error(), "hook hello.Lookup: no binding") {
		t.Fatalf("unanswered: %v", err)
	}

	// In workflow code the error matches hook.ErrNoBinding.
	none, err := workflowtest.WorkflowActivity[LookupIn, LookupOut](workflowtest.New(t, h), "Twice", LookupIn{})
	if err != nil || none.Title != "none" {
		t.Fatalf("no binding: %+v %v", none, err)
	}

	backplanetest.Answer(h, flow.lookup, func(_ context.Context, in LookupIn) (LookupOut, error) {
		if in.Name == "bad" {
			return LookupOut{}, errors.New("unknown person")
		}

		return LookupOut{Title: "dr"}, nil
	})

	w = workflowtest.New(t, h)
	w.ExecuteWorkflow("Welcome", WelcomeIn{Name: "Ann"})

	var out WelcomeOut
	if err := w.GetWorkflowResult(&out); err != nil || out.Text != `{"text":"[DR Ann]"}` {
		t.Fatalf("welcome: %+v %v", out, err)
	}

	twice, err := workflowtest.WorkflowActivity[LookupIn, LookupOut](workflowtest.New(t, h), "Twice", LookupIn{Name: "x"})
	if err != nil || twice.Title != "drdr" {
		t.Fatalf("workflow activity: %+v %v", twice, err)
	}

	_, err = workflowtest.WorkflowActivity[LookupIn, LookupOut](workflowtest.New(t, h), "Twice", LookupIn{Name: "bad"})
	if err == nil || !strings.Contains(err.Error(), "unknown person") {
		t.Fatalf("failing answer: %v", err)
	}
}
