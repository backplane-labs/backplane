package event_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	natsdriver "github.com/backplane-labs/backplane/pkg/backplane/drivers/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/event"
	infranats "github.com/backplane-labs/backplane/pkg/backplane/infra/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/broker"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

const waitFor = 15 * time.Second

type group struct {
	ctx context.Context
	wg  sync.WaitGroup
}

func (g *group) Go(fn func(ctx context.Context) error) { g.wg.Go(func() { _ = fn(g.ctx) }) }

// live is a service tree wired to NATS; the reactors start with start.
type live struct {
	root  deps.Component
	env   *env.Env
	b     *broker.Broker
	start func()
}

func connect(t *testing.T, service string) live {
	t.Helper()

	addr := os.Getenv("BACKPLANE_TEST_NATS")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_NATS not set (make upstream)")
	}

	e := env.New(service, manifest.New(service, "1.0.0"))
	app := node.New(service, testlog.Discard(), e).Child(service, node.Root, false)

	ctx, cancel := context.WithCancel(context.Background())
	g := &group{ctx: ctx}
	b := broker.New(broker.Params{Conn: infranats.Config{URL: config.Secret("nats://" + addr)}, Service: service, Instance: service + "-1", Env: e})

	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()

		_ = b.StopReactors(sctx)
		_ = b.Close(sctx)

		cancel()
		g.wg.Wait()
	})

	return live{root: link.Scope(app).(deps.Component), env: e, b: b, start: func() {
		if err := b.Connect(t.Context(), g); err != nil {
			t.Fatal(err)
		}

		e.SetBroker(b)

		if err := b.StartReactors(t.Context(), g); err != nil {
			t.Fatal(err)
		}
	}}
}

// The whole path through NATS: a pinned consumer, the delivery metadata
// on the handler's ctx, a payload that does not decode dead-lettered at
// once, the native client and a redrive.
func TestLiveReactor(t *testing.T) {
	t.Parallel()

	src, sub := "evsrc-"+suffix(), "evsub-"+suffix()
	emit, recv := connect(t, src), connect(t, sub)

	ref := event.Declare[Greeted](emit.root, "Greeted")
	bad := event.Declare[string](emit.root, "Garbled")

	got := make(chan event.Delivery, 1)

	event.React(deps.NewComponent(recv.root, "mailer"), src+".Greeted", func(ctx context.Context, _ Greeted) error {
		d, _ := event.DeliveryOf(ctx)
		got <- d

		return nil
	}, event.Consumer("welcome"))

	event.React(recv.root, src+".Garbled", func(context.Context, Greeted) error { return nil },
		event.Consumer("garbled"))

	emit.start()
	recv.start()

	native, err := natsdriver.JetStream(recv.root)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		for _, s := range []string{broker.StreamName(src), broker.StreamName(sub), broker.DLQStreamName(sub)} {
			_ = native.DeleteStream(context.Background(), s)
		}
	})

	for _, c := range []string{"welcome", "garbled"} {
		durable := broker.Durable(sub, c)
		eventually(t, "consumer "+durable, func() bool {
			_, err := native.Consumer(t.Context(), broker.StreamName(src), durable)

			return err == nil
		})
	}

	published := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := ref.Publish(t.Context(), Greeted{Name: "ann"}, event.ID("greet-1"), event.Key("ann"),
		event.Time(published), event.Header("tenant", "t1")); err != nil {
		t.Fatal(err)
	}

	select {
	case d := <-got:
		if d.ID != "greet-1" || d.Source != src || d.Type != src+".Greeted" || d.Subject != "ann" ||
			!d.Time.Equal(published) || d.Attempt != 1 || d.Consumer != "welcome" || d.Extensions["tenant"] != "t1" {
			t.Fatalf("delivery %+v", d)
		}
	case <-time.After(waitFor):
		t.Fatal("not received")
	}

	// "a string" does not decode into Greeted: dead-lettered on the first
	// delivery, and a redrive fails the same way.
	if err := bad.Publish(t.Context(), "a string"); err != nil {
		t.Fatal(err)
	}

	eventually(t, "dead letter", func() bool {
		s, err := native.Stream(t.Context(), broker.DLQStreamName(sub))
		if err != nil {
			return false
		}

		m, err := s.GetLastMsgForSubject(t.Context(), broker.DLQSubject(sub, "garbled"))

		return err == nil && m.Header.Get("bp-delivered") == "1"
	})

	if n, err := event.Redrive(t.Context(), recv.root, "garbled"); n != 0 || err == nil {
		t.Fatalf("redrive of an undecodable payload: %d %v", n, err)
	}

	if _, err := native.Stream(t.Context(), broker.StreamName(src)); err != nil {
		t.Fatalf("native client: %v", err)
	}
}

func suffix() string {
	var b [4]byte

	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("%s: not reached", what)
}
