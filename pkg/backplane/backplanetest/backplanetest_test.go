package backplanetest_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/event"
	"github.com/backplane-labs/backplane/pkg/backplane/hook"
)

type (
	Greeted struct {
		Name string `json:"name"`
		Text string `json:"text"`
	}
	LookupIn struct {
		Name string `json:"name"`
	}
	LookupOut struct {
		Title string `json:"title"`
	}
	WelcomeIn struct {
		Name string `json:"name"`
	}
	WelcomeOut struct {
		Text string `json:"text"`
	}
	UserRegistered struct {
		ID string `json:"id"`
	}
)

// welcomer raises an event, calls a hook, implements an activity and reacts
// to another service's event: everything the harness has to drive.
type welcomer struct {
	deps.Component

	greeting *config.Live[string]
	store    deps.Dependency[*sync.Map]
	greeted  event.Ref[Greeted]
	lookup   hook.Ref[LookupIn, LookupOut]

	mu         sync.Mutex
	registered []string
	greetings  []string
}

func newWelcomer(parent deps.Scope, greeting *config.Live[string], store deps.Dependency[*sync.Map]) *welcomer {
	w := &welcomer{Component: deps.NewComponent(parent, "welcomer"), greeting: greeting, store: store}
	w.greeted = event.Declare[Greeted](w, "Greeted")
	w.lookup = hook.Declare[LookupIn, LookupOut](w, "Lookup", hook.Required())

	activity.Handle(w, "Welcome", w.welcome)
	event.React(w, "iam.UserRegistered", func(_ context.Context, u UserRegistered) error {
		w.mu.Lock()
		defer w.mu.Unlock()

		w.registered = append(w.registered, u.ID)

		return nil
	})
	greeting.Watch(func(v string) {
		w.mu.Lock()
		defer w.mu.Unlock()

		w.greetings = append(w.greetings, v)
	})

	return w
}

func (w *welcomer) welcome(ctx context.Context, in WelcomeIn) (WelcomeOut, error) {
	who, err := w.lookup.Call(ctx, LookupIn(in))
	if err != nil {
		return WelcomeOut{}, fmt.Errorf("lookup: %w", err)
	}

	text := w.greeting.Get() + ", " + who.Title + " " + in.Name
	w.store.Get().Store(in.Name, text)

	if err := w.greeted.Publish(ctx, Greeted{Name: in.Name, Text: text}, event.Key(in.Name)); err != nil {
		return WelcomeOut{}, fmt.Errorf("publish: %w", err)
	}

	return WelcomeOut{Text: text}, nil
}

func (w *welcomer) seen() ([]string, []string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([]string(nil), w.registered...), append([]string(nil), w.greetings...)
}

func TestHarness(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)

	var provided, closed atomic.Bool

	store := deps.NewDependency(h.Root(), deps.Func(func(context.Context) (*sync.Map, error) {
		provided.Store(true)

		return &sync.Map{}, nil
	}, deps.WithClose(func(context.Context, *sync.Map) error { closed.Store(true); return nil })), deps.Name("store"))

	greeting := config.LiveOf("Hello")
	w := newWelcomer(h.Root(), &greeting, store)

	// Unanswered, a hook fails as in production without a binding.
	if _, err := w.lookup.Call(t.Context(), LookupIn{Name: "x"}); !errors.Is(err, hook.ErrUnavailable) {
		t.Fatalf("unanswered hook: %v", err)
	}

	backplanetest.Answer(h, w.lookup, func(context.Context, LookupIn) (LookupOut, error) {
		return LookupOut{Title: "dear"}, nil
	})

	// Cleanups run in reverse: this one runs after the harness stopped.
	t.Cleanup(func() {
		if !closed.Load() {
			t.Error("stop did not close the dependency")
		}
	})

	h.Start()

	if !provided.Load() {
		t.Fatal("Start did not provide the dependency")
	}

	out, err := backplanetest.Activity[WelcomeIn, WelcomeOut](t.Context(), h, "Welcome", WelcomeIn{Name: "Ann"})
	if err != nil || out.Text != "Hello, dear Ann" {
		t.Fatalf("activity: %+v %v", out, err)
	}

	if got := backplanetest.Events(h, w.greeted); len(got) != 1 || got[0] != (Greeted{Name: "Ann", Text: out.Text}) {
		t.Fatalf("events: %+v", got)
	}

	if err := backplanetest.React(t.Context(), h, "welcomer", "iam.UserRegistered", UserRegistered{ID: "u1"}); err != nil {
		t.Fatal(err)
	}

	backplanetest.SetLive(&greeting, "Hi")

	registered, greetings := w.seen()
	if fmt.Sprint(registered) != "[u1]" || fmt.Sprint(greetings) != "[Hi]" {
		t.Fatalf("reactor saw %v, watcher saw %v", registered, greetings)
	}

	checkManifest(t, h)
}

func checkManifest(t *testing.T, h *backplanetest.Harness) {
	t.Helper()

	m := h.Manifest()

	if len(m.GetEvents()) != 1 || m.GetEvents()[0].GetName() != "Greeted" || m.GetEvents()[0].GetSchema() == nil {
		t.Errorf("events: %v", m.GetEvents())
	}

	if len(m.GetHooks()) != 1 || !m.GetHooks()[0].GetRequired() || m.GetHooks()[0].GetInput() == nil {
		t.Errorf("hooks: %v", m.GetHooks())
	}

	if len(m.GetActivities()) != 1 || m.GetActivities()[0].GetName() != "Welcome" {
		t.Errorf("activities: %v", m.GetActivities())
	}

	subs := m.GetSubscriptions()
	if len(subs) != 1 || subs[0].GetEvent() != "iam.UserRegistered" || subs[0].GetConsumer() != "welcomer:iam.UserRegistered" {
		t.Errorf("subscriptions: %v", subs)
	}
}

func TestHarnessMisses(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	event.React(h.Root(), "iam.UserDeleted", func(context.Context, UserRegistered) error { return nil })
	h.Start()

	if _, err := backplanetest.Activity[WelcomeIn, WelcomeOut](t.Context(), h, "Nope", WelcomeIn{}); !errors.Is(err, backplanetest.ErrNotDeclared) {
		t.Fatal("undeclared activity must fail")
	}

	if err := backplanetest.React(t.Context(), h, "welcomer", "iam.UserDeleted", UserRegistered{}); !errors.Is(err, backplanetest.ErrNotDeclared) {
		t.Fatal("reactor on the wrong path must fail")
	}

	// Declared on the root: the consumer is the bare event name.
	if err := backplanetest.React(t.Context(), h, "", "iam.UserDeleted", UserRegistered{}); err != nil {
		t.Fatalf("root reactor: %v", err)
	}
}

func TestHarnessReactOptions(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	event.React(h.Root(), "iam.UserRegistered", func(ctx context.Context, _ UserRegistered) error {
		if _, has := ctx.Deadline(); !has {
			return errors.New("no deadline from event.Timeout")
		}

		<-ctx.Done()

		return ctx.Err()
	}, event.Timeout(20*time.Millisecond), event.MaxDeliver(1), event.Concurrency(1),
		event.Redelivery(time.Millisecond, time.Second), event.StartAt(event.StartAll))
	h.Start()

	err := backplanetest.React(t.Context(), h, "", "iam.UserRegistered", UserRegistered{ID: "u1"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the Timeout to end the handler, got %v", err)
	}
}

// An unanswered hook is not bound; Answer binds it and signals the change.
func TestHarnessBound(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t, backplanetest.Name("bound"))
	root := h.Root()
	lookup := hook.Declare[LookupIn, LookupOut](root, "Lookup")

	if lookup.Bound() {
		t.Fatal("bound before Answer")
	}

	changes := lookup.BindingChanges(t.Context())

	backplanetest.Answer(h, lookup, func(context.Context, LookupIn) (LookupOut, error) { return LookupOut{}, nil })

	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("no change signal")
	}

	if !lookup.Bound() {
		t.Fatal("not bound after Answer")
	}

	var unset hook.Ref[LookupIn, LookupOut]
	if unset.Bound() {
		t.Fatal("an undeclared Ref is bound")
	}
}
