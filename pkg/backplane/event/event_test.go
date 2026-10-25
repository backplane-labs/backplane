package event_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/event"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

type Greeted struct {
	Name string `json:"name"`
}

var errRejected = errors.New("rejected")

// bare is a service tree with no transport installed.
func bare(t *testing.T) (deps.Component, *env.Env) {
	t.Helper()

	e := env.New("svc", manifest.New("svc", "0.0.0"))
	app := node.New("svc", testlog.Discard(), e).Child("svc", node.Root, false)

	return link.Scope(app).(deps.Component), e
}

// keys records the messages events were published as.
type keys struct {
	mu   sync.Mutex
	keys []string
	msgs []env.Message
}

func (k *keys) Publish(_ context.Context, m env.Message) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.keys = append(k.keys, m.Key)
	k.msgs = append(k.msgs, m)

	return nil
}

func (*keys) Call(context.Context, string, []byte) ([]byte, error) { return nil, env.ErrUnavailable }

func TestDeclareRecordsEvent(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	ref := event.Declare[Greeted](h.Root(), "Greeted")

	if ref.Name() != "test.Greeted" {
		t.Fatalf("name %q", ref.Name())
	}

	events := h.Manifest().GetEvents()
	if len(events) != 1 || events[0].GetName() != "Greeted" || events[0].GetSchema() == nil {
		t.Fatalf("events: %v", events)
	}
}

func TestDuplicateEventFailsManifest(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	event.Declare[Greeted](root, "Greeted")
	event.Declare[Greeted](deps.NewComponent(root, "other"), "Greeted")

	if _, err := e.Manifest.Build(); !errors.Is(err, manifest.ErrDuplicate) {
		t.Fatalf("want duplicate, got %v", err)
	}
}

func TestPublishWithoutTransport(t *testing.T) {
	t.Parallel()

	root, _ := bare(t)
	ref := event.Declare[Greeted](root, "Greeted")

	if err := ref.Publish(t.Context(), Greeted{Name: "x"}); !errors.Is(err, event.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestPublishRecorded(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	ref := event.Declare[Greeted](h.Root(), "Greeted")
	h.Start()

	for _, name := range []string{"ann", "bob"} {
		if err := ref.Publish(t.Context(), Greeted{Name: name}); err != nil {
			t.Fatal(err)
		}
	}

	got := backplanetest.Events(h, ref)
	if !slices.Equal(got, []Greeted{{Name: "ann"}, {Name: "bob"}}) {
		t.Fatalf("events %v", got)
	}
}

func TestPublishKey(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	rec := &keys{}
	e.SetBroker(rec)

	ref := event.Declare[Greeted](root, "Greeted")
	if err := ref.Publish(t.Context(), Greeted{}, event.Key("user-1")); err != nil {
		t.Fatal(err)
	}

	if err := ref.Publish(t.Context(), Greeted{}); err != nil {
		t.Fatal(err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if !slices.Equal(rec.keys, []string{"user-1", ""}) {
		t.Fatalf("keys %q", rec.keys)
	}
}

func TestReact(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	mailer := deps.NewComponent(h.Root(), "mailer")

	got := make(chan Greeted, 1)

	event.React(mailer, "iam.UserRegistered", func(ctx context.Context, v Greeted) error {
		if v.Name == "" {
			return errRejected
		}

		if d, ok := event.DeliveryOf(ctx); !ok || d.Attempt != 1 || d.Consumer != "mailer:iam.UserRegistered" ||
			d.Source != "iam" || d.ID == "" {
			return fmt.Errorf("delivery %+v", d)
		}

		got <- v

		return nil
	})
	event.React(h.Root(), "iam.UserRegistered", func(context.Context, Greeted) error { return nil })

	subs := h.Manifest().GetSubscriptions()
	if len(subs) != 2 || subs[0].GetEvent() != "iam.UserRegistered" ||
		subs[0].GetConsumer() != "mailer:iam.UserRegistered" || subs[1].GetConsumer() != "iam.UserRegistered" {
		t.Fatalf("subscriptions: %v", subs)
	}

	h.Start()

	if err := backplanetest.React(t.Context(), h, "mailer", "iam.UserRegistered", Greeted{Name: "ann"}); err != nil {
		t.Fatal(err)
	}

	if v := <-got; v.Name != "ann" {
		t.Fatalf("delivered %v", v)
	}

	if err := backplanetest.React(t.Context(), h, "mailer", "iam.UserRegistered", Greeted{}); !errors.Is(err, errRejected) {
		t.Fatalf("handler error: %v", err)
	}

	if err := backplanetest.React(t.Context(), h, "", "iam.UserRegistered", Greeted{}); err != nil {
		t.Fatalf("root reactor: %v", err)
	}

	if err := backplanetest.React(t.Context(), h, "nobody", "iam.UserRegistered", Greeted{}); err == nil {
		t.Fatal("delivered to an undeclared reactor")
	}
}

// A raw reactor gets any event of its pattern undecoded, with its delivery.
func TestReactRaw(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)

	got := make(chan event.Raw, 1)

	event.ReactRaw(h.Root(), "journal", "*", func(_ context.Context, ev event.Raw) error {
		got <- ev

		return nil
	})

	subs := h.Manifest().GetSubscriptions()
	if len(subs) != 1 || subs[0].GetEvent() != "*" || subs[0].GetConsumer() != "journal" {
		t.Fatalf("subscriptions: %v", subs)
	}

	if err := backplanetest.ReactConsumer(t.Context(), h, "journal", Greeted{Name: "ann"},
		backplanetest.WithEvent("chat.MessageSent")); err != nil {
		t.Fatal(err)
	}

	ev := <-got
	if ev.Type != "chat.MessageSent" || ev.Source != "chat" || string(ev.Data) != `{"name":"ann"}` || ev.ID == "" {
		t.Fatalf("raw: %+v %s", ev.Delivery, ev.Data)
	}

	for _, bad := range [][2]string{{"journal", "chat.MessageSent"}, {"journal", "chat"}, {"", "*"}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("ReactRaw(%q, %q) did not panic", bad[0], bad[1])
				}
			}()

			event.ReactRaw(h.Root(), bad[0], bad[1], func(context.Context, event.Raw) error { return nil })
		}()
	}
}

func TestDuplicateReactorFailsManifest(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	noop := func(context.Context, Greeted) error { return nil }

	event.React(root, "iam.UserRegistered", noop)
	event.React(root, "iam.UserRegistered", noop)

	if _, err := e.Manifest.Build(); !errors.Is(err, manifest.ErrDuplicate) {
		t.Fatalf("want duplicate, got %v", err)
	}
}

func TestZeroRef(t *testing.T) {
	t.Parallel()

	var ref event.Ref[Greeted]
	if ref.Name() != "" {
		t.Fatalf("name %q", ref.Name())
	}

	if err := ref.Publish(t.Context(), Greeted{}); !errors.Is(err, event.ErrUnavailable) {
		t.Fatalf("zero ref publish: %v", err)
	}
}

func TestDeclareOnZeroScopePanics(t *testing.T) {
	t.Parallel()

	for what, declare := range map[string]func(){
		"event Greeted": func() { event.Declare[Greeted](deps.Component{}, "Greeted") },
		"reactor of iam.UserRegistered": func() {
			event.React(deps.Component{}, "iam.UserRegistered", func(context.Context, Greeted) error { return nil })
		},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if r, _ := recover().(string); !strings.Contains(r, what+" declared on a zero scope") {
					t.Fatalf("panic %q", r)
				}
			}()

			declare()
		})
	}
}

func TestReactOptions(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	noop := func(context.Context, Greeted) error { return nil }

	event.React(root, "iam.UserRegistered", noop)
	event.React(deps.NewComponent(root, "mailer"), "iam.UserRegistered", noop,
		event.MaxDeliver(2), event.Concurrency(1), event.Timeout(time.Second),
		event.Redelivery(time.Millisecond, time.Second), event.StartAt(event.StartAll))
	event.React(deps.NewComponent(root, "late"), "iam.UserRegistered", noop,
		event.StartAt(event.StartAll), event.StartAt(event.StartNew))

	declared := e.Reactors()
	if len(declared) != 3 {
		t.Fatalf("reactors %d", len(declared))
	}

	if declared[0].Delivery != (env.Delivery{}) {
		t.Errorf("no options: %+v", declared[0].Delivery)
	}

	want := env.Delivery{
		MaxDeliver: 2, Concurrency: 1, Timeout: time.Second,
		Redelivery: backoff.Policy{Min: time.Millisecond, Max: time.Second}, StartAll: true,
	}
	if declared[1].Delivery != want {
		t.Errorf("options: %+v, want %+v", declared[1].Delivery, want)
	}

	if declared[2].Delivery.StartAll {
		t.Error("the last StartAt wins")
	}
}

func TestReactOptionsValidated(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		want string
		opt  func()
	}{
		"max deliver":     {"MaxDeliver must be >= 1", func() { event.MaxDeliver(0) }},
		"concurrency":     {"Concurrency must be >= 1", func() { event.Concurrency(-1) }},
		"timeout":         {"Timeout must be positive", func() { event.Timeout(0) }},
		"redelivery bent": {"event: redelivery: backoff", func() { event.Redelivery(time.Second, time.Millisecond) }},
		"redelivery zero": {"event: redelivery: backoff", func() { event.Redelivery(0, time.Second) }},
		"start":           {"unknown Start", func() { event.StartAt(event.Start(7)) }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if r, _ := recover().(string); !strings.Contains(r, c.want) {
					t.Fatalf("panic %q, want %q", r, c.want)
				}
			}()

			c.opt()
		})
	}

	event.MaxDeliver(1)
	event.Concurrency(1)
	event.Timeout(time.Nanosecond)
	event.Redelivery(time.Second, time.Second)
	event.StartAt(event.StartNew)
}
