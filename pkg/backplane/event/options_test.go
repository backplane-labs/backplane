package event_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	natsdriver "github.com/backplane-labs/backplane/pkg/backplane/drivers/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/event"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()

	defer func() {
		t.Helper()

		r, _ := recover().(string)
		if !strings.Contains(r, want) {
			t.Fatalf("panic %q, want %q", r, want)
		}
	}()

	fn()
}

func TestDeclareDescribe(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	event.Declare[Greeted](h.Root(), "Greeted", event.Describe("someone was greeted"))

	if got := h.Manifest().GetEvents()[0].GetDescription(); got != "someone was greeted" {
		t.Fatalf("description %q", got)
	}
}

func TestNamesValidated(t *testing.T) {
	t.Parallel()

	root, _ := bare(t)
	noop := func(context.Context, Greeted) error { return nil }

	for _, name := range []string{"greeted", "Greeted.V2", "Mail_Sent", "", "1st"} {
		mustPanic(t, "must be CamelCase", func() { event.Declare[Greeted](root, name) })
	}

	for _, name := range []string{"UserRegistered", "iam.userRegistered", "IAM.UserRegistered", "iam.", ".X", "iam.a.B"} {
		mustPanic(t, "want <service>.<Event>", func() { event.React(root, name, noop) })
	}

	event.Declare[Greeted](root, "MailSent2")
	event.React(root, "mail-sender.MailSent", noop)
}

func TestPublishOptions(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	rec := &keys{}
	e.SetBroker(rec)

	ref := event.Declare[Greeted](root, "Greeted")
	published := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := ref.Publish(t.Context(), Greeted{Name: "ann"}, event.Key("k"), event.ID("row-1"), event.Time(published),
		event.Header("tenant", "t1"), event.Header("region", "eu")); err != nil {
		t.Fatal(err)
	}

	rec.mu.Lock()
	m := rec.msgs[0]
	rec.mu.Unlock()

	if m.Event != "svc.Greeted" || m.Key != "k" || m.ID != "row-1" || !m.Time.Equal(published) ||
		m.Extensions["tenant"] != "t1" || m.Extensions["region"] != "eu" || string(m.Payload) != `{"name":"ann"}` {
		t.Fatalf("message %+v", m)
	}

	for name, opt := range map[string]event.PublishOption{
		"empty id":      event.ID(""),
		"upper name":    event.Header("Tenant", "x"),
		"dash name":     event.Header("x-tenant", "x"),
		"empty name":    event.Header("", "x"),
		"reserved":      event.Header("source", "x"),
		"sdk extension": event.Header("instance", "x"),
		"line break":    event.Header("tenant", "a\r\nb"),
	} {
		if err := ref.Publish(t.Context(), Greeted{}, opt); !errors.Is(err, event.ErrOption) {
			t.Errorf("%s: %v", name, err)
		}
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if len(rec.msgs) != 1 {
		t.Fatalf("invalid options published %d", len(rec.msgs)-1)
	}
}

func TestTerminal(t *testing.T) {
	t.Parallel()

	if event.Terminal(nil) != nil {
		t.Fatal("Terminal(nil) is not nil")
	}

	err := event.Terminal(errRejected)

	var marked env.NonRetryableError
	if !errors.As(err, &marked) || !errors.Is(err, errRejected) {
		t.Fatalf("terminal %v", err)
	}
}

func TestDecodeErrorIsTerminal(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	event.React(root, "iam.UserRegistered", func(context.Context, Greeted) error { return nil })

	_, err := e.Reactors()[0].Handler(t.Context(), []byte(`{"name":7}`))

	var marked env.NonRetryableError
	if !errors.As(err, &marked) || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("decode error %v", err)
	}
}

func TestDeliveryOf(t *testing.T) {
	t.Parallel()

	if _, ok := event.DeliveryOf(t.Context()); ok {
		t.Fatal("delivery outside a handler")
	}

	happened := time.Now()
	ctx := env.WithIncoming(t.Context(), env.Incoming{
		ID: "id", Source: "iam", Type: "iam.X", Subject: "k", Time: happened, Attempt: 2, Consumer: "c",
		Extensions: map[string]string{"tenant": "t1"},
	})

	d, ok := event.DeliveryOf(ctx)
	if !ok || d.ID != "id" || d.Source != "iam" || d.Type != "iam.X" || d.Subject != "k" || !d.Time.Equal(happened) ||
		d.Attempt != 2 || d.Consumer != "c" || d.Extensions["tenant"] != "t1" {
		t.Fatalf("delivery %+v", d)
	}

	d.Extensions["tenant"] = "changed"

	if again, _ := event.DeliveryOf(ctx); again.Extensions["tenant"] != "t1" {
		t.Fatal("extensions shared between calls")
	}
}

func TestReactConsumerOrderedInactive(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	noop := func(context.Context, Greeted) error { return nil }

	event.React(deps.NewComponent(root, "mailer"), "iam.UserRegistered", noop,
		event.Consumer("welcome-mail"), event.Ordered(), event.InactiveThreshold(time.Hour))

	subs, err := e.Manifest.Build()
	if err != nil {
		t.Fatal(err)
	}

	if got := subs.GetSubscriptions()[0].GetConsumer(); got != "welcome-mail" {
		t.Fatalf("manifest consumer %q", got)
	}

	r, ok := e.ReactorOf("welcome-mail")
	if !ok || !r.Delivery.Ordered || r.Delivery.InactiveThreshold != time.Hour {
		t.Fatalf("reactor %+v", r)
	}

	mustPanic(t, "Consumer name must not be empty", func() { event.Consumer("") })
	mustPanic(t, "InactiveThreshold must be positive", func() { event.InactiveThreshold(0) })
	mustPanic(t, "Ordered runs one handler at a time", func() {
		event.React(root, "iam.UserDeleted", noop, event.Ordered(), event.Concurrency(2))
	})

	event.React(root, "iam.UserBlocked", noop, event.Ordered(), event.Concurrency(1))
}

func TestNativeWithoutNATS(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)

	if _, err := natsdriver.JetStream(h.Root()); !errors.Is(err, event.ErrUnavailable) {
		t.Fatalf("jetstream: %v", err)
	}

	if _, err := event.Redrive(t.Context(), h.Root(), "c"); !errors.Is(err, event.ErrUnavailable) {
		t.Fatalf("redrive: %v", err)
	}

	if _, err := natsdriver.JetStream(deps.Component{}); !errors.Is(err, event.ErrUnavailable) {
		t.Fatalf("zero scope: %v", err)
	}
}

// A proto payload travels as protojson; a Ref of a message pointer
// decodes into a fresh message, and a field the reader does not know is
// ignored.
func TestProtoPayload(t *testing.T) {
	t.Parallel()

	root, e := bare(t)
	rec := &keys{}
	e.SetBroker(rec)

	ref := event.Declare[*backplanev1.NodeStatus](root, "NodeSeen")
	if err := ref.Publish(t.Context(), &backplanev1.NodeStatus{Path: "db", Kind: backplanev1.NodeKind_NODE_KIND_COMPONENT}); err != nil {
		t.Fatal(err)
	}

	got := make(chan *backplanev1.NodeStatus, 1)

	event.React(root, "svc.NodeSeen", func(_ context.Context, n *backplanev1.NodeStatus) error {
		got <- n

		return nil
	})

	rec.mu.Lock()
	payload := rec.msgs[0].Payload
	rec.mu.Unlock()

	newer := append([]byte(`{"addedLater":{"x":1},`), payload[1:]...)
	if _, err := e.Reactors()[0].Handler(t.Context(), newer); err != nil {
		t.Fatalf("payload %s: %v", newer, err)
	}

	if n := <-got; n.GetPath() != "db" || n.GetKind() != backplanev1.NodeKind_NODE_KIND_COMPONENT {
		t.Fatalf("decoded %v", n)
	}
}
