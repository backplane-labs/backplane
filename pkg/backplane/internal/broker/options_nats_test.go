package broker_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	infranats "github.com/backplane-labs/backplane/pkg/backplane/infra/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/broker"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
)

// reacting is the env of a service with one reactor on src.Greeted
// delivered as d.
func reacting(service, src string, d env.Delivery, h env.Handler) (*env.Env, string) {
	e := env.New(service, manifest.New(service, "1.0.0"))
	consumer := "mailer:" + src + ".Greeted"
	e.Reactor(env.Reactor{Event: src + ".Greeted", Consumer: consumer, Handler: h, Delivery: d})

	return e, consumer
}

// deadLetter waits for the dead letter of sub's consumer.
func deadLetter(t *testing.T, jet jetstream.JetStream, sub, consumer string) *jetstream.RawStreamMsg {
	t.Helper()

	subject := broker.DLQSubject(sub, consumer)

	eventually(t, "dead letter", func() bool {
		s, err := jet.Stream(t.Context(), broker.DLQStreamName(sub))
		if err != nil {
			return false
		}

		_, err = s.GetLastMsgForSubject(t.Context(), subject)

		return err == nil
	})

	return lastMsg(t, jet, broker.DLQStreamName(sub), subject)
}

func consumerConfig(t *testing.T, jet jetstream.JetStream, src, durable string) jetstream.ConsumerConfig {
	t.Helper()

	c, err := jet.Consumer(t.Context(), broker.StreamName(src), durable)
	if err != nil {
		t.Fatal(err)
	}

	return c.CachedInfo().Config
}

func TestMaxDeliverAndRedelivery(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	const gap = 300 * time.Millisecond

	var (
		lock  sync.Mutex
		calls []time.Time
	)

	e, consumer := reacting(sub, src, env.Delivery{MaxDeliver: 2, Redelivery: backoff.Policy{Min: gap, Max: gap}},
		func(context.Context, []byte) ([]byte, error) {
			lock.Lock()
			defer lock.Unlock()

			calls = append(calls, time.Now())

			return nil, errBoom
		})

	pub := open(t, url, src, emitter(t, src))
	recv := open(t, url, sub, e)
	durable := broker.Durable(sub, consumer)
	startReactors(t, jet, recv, src, durable)

	if cfg := consumerConfig(t, jet, src, durable); cfg.MaxDeliver != 2+broker.StopDeliveries {
		t.Fatalf("consumer max_deliver %d", cfg.MaxDeliver)
	}

	if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	if m := deadLetter(t, jet, sub, consumer); m.Header.Get("bp-delivered") != "2" {
		t.Fatalf("dead letter headers %v", m.Header)
	}

	time.Sleep(200 * time.Millisecond) // no delivery after the last one

	lock.Lock()
	defer lock.Unlock()

	if len(calls) != 2 {
		t.Fatalf("handler called %d times", len(calls))
	}

	// The broker's test default is 20ms: the reactor's own delay applies.
	if d := calls[1].Sub(calls[0]); d < gap-50*time.Millisecond {
		t.Fatalf("redelivered after %v, want about %v", d, gap)
	}
}

// peak publishes n events to a reactor with the given concurrency whose
// handler takes a while, and returns the most handlers seen in flight.
func peak(t *testing.T, concurrency, n int) int32 {
	t.Helper()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	var (
		now, most atomic.Int32
		done      sync.WaitGroup
	)

	done.Add(n)

	e, consumer := reacting(sub, src, env.Delivery{Concurrency: concurrency}, func(context.Context, []byte) ([]byte, error) {
		defer done.Done()

		cur := now.Add(1)

		for {
			m := most.Load()
			if cur <= m || most.CompareAndSwap(m, cur) {
				break
			}
		}

		time.Sleep(150 * time.Millisecond)
		now.Add(-1)

		return nil, nil
	})

	pub := open(t, url, src, emitter(t, src))
	recv := open(t, url, sub, e)
	startReactors(t, jet, recv, src, broker.Durable(sub, consumer))

	for range n {
		if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}

	finished := make(chan struct{})

	go func() {
		done.Wait()
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(waitFor):
		t.Fatal("not every event handled")
	}

	return most.Load()
}

func TestConcurrency(t *testing.T) {
	t.Parallel()

	t.Run("one", func(t *testing.T) {
		t.Parallel()

		if got := peak(t, 1, 5); got != 1 {
			t.Fatalf("%d handlers in flight, want 1", got)
		}
	})

	t.Run("three", func(t *testing.T) {
		t.Parallel()

		if got := peak(t, 3, 6); got < 2 || got > 3 {
			t.Fatalf("%d handlers in flight, want 2..3", got)
		}
	})
}

func TestTimeoutCancelsHandler(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	const timeout = 200 * time.Millisecond

	ended := make(chan error, 1)
	e, consumer := reacting(sub, src, env.Delivery{MaxDeliver: 1, Timeout: timeout},
		func(ctx context.Context, _ []byte) ([]byte, error) {
			<-ctx.Done()

			ended <- ctx.Err()

			return nil, ctx.Err()
		})

	pub := open(t, url, src, emitter(t, src))
	recv := open(t, url, sub, e)
	durable := broker.Durable(sub, consumer)
	startReactors(t, jet, recv, src, durable)

	if cfg := consumerConfig(t, jet, src, durable); cfg.AckWait != timeout+15*time.Second || cfg.MaxDeliver != 1+broker.StopDeliveries {
		t.Fatalf("consumer ack_wait %v max_deliver %d", cfg.AckWait, cfg.MaxDeliver)
	}

	start := time.Now()

	if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-ended:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("handler ctx ended with %v", err)
		}

		// The broker's test default is 5s: the reactor's own timeout applies.
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("cancelled after %v", d)
		}
	case <-time.After(waitFor):
		t.Fatal("handler not cancelled")
	}

	if m := deadLetter(t, jet, sub, consumer); !strings.Contains(m.Header.Get("bp-error"), "deadline exceeded") ||
		m.Header.Get("bp-delivered") != "1" {
		t.Fatalf("dead letter headers %v", m.Header)
	}
}

func TestStartAll(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	pub := open(t, url, src, emitter(t, src))

	for _, v := range []string{`{"n":1}`, `{"n":2}`} {
		if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(v)); err != nil {
			t.Fatal(err)
		}
	}

	got := make(chan string, 10)
	handler := func(_ context.Context, in []byte) ([]byte, error) {
		got <- string(in)

		return nil, nil
	}

	e, consumer := reacting(sub, src, env.Delivery{StartAll: true}, handler)
	recv := open(t, url, sub, e)
	durable := broker.Durable(sub, consumer)
	startReactors(t, jet, recv, src, durable)

	seen := map[string]bool{}

	for range 2 {
		select {
		case v := <-got:
			seen[v] = true // handlers run concurrently: any order
		case <-time.After(waitFor):
			t.Fatalf("earlier events not replayed, got %v", seen)
		}
	}

	if !seen[`{"n":1}`] || !seen[`{"n":2}`] {
		t.Fatalf("replayed %v", seen)
	}

	eventually(t, "acked", func() bool {
		c, err := jet.Consumer(t.Context(), broker.StreamName(src), durable)
		if err != nil {
			return false
		}

		info, err := c.Info(t.Context())

		return err == nil && info.AckFloor.Stream == 2
	})

	if err := recv.StopReactors(t.Context()); err != nil {
		t.Fatal(err)
	}

	// The existing consumer keeps its position: nothing is replayed.
	e2, _ := reacting(sub, src, env.Delivery{StartAll: true}, handler)
	startReactors(t, jet, open(t, url, sub, e2), src, durable)

	if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(`{"n":3}`)); err != nil {
		t.Fatal(err)
	}

	select {
	case v := <-got:
		if v != `{"n":3}` {
			t.Fatalf("replayed %s", v)
		}
	case <-time.After(waitFor):
		t.Fatal("new event not received")
	}
}

func TestStreamSettings(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	svc := unique("ops")
	jet := admin(t, url, svc)

	lim := broker.Streams{
		MaxAge: time.Hour, MaxBytes: 1 << 20, Replicas: 1, Duplicates: 30 * time.Second, DeadMaxAge: 3 * time.Hour,
	}

	// Emits and reacts to its own event: it ensures both of its streams.
	e := emitter(t, svc)
	e.Reactor(env.Reactor{
		Event: svc + ".Greeted", Consumer: "self", Handler: func(context.Context, []byte) ([]byte, error) { return nil, nil },
	})

	b := openWith(t, broker.Params{Conn: infranats.Config{URL: config.Secret(url)}, Service: svc, Env: e, Streams: lim})
	startReactors(t, jet, b, svc, broker.Durable(svc, "self"))

	events, err := jet.Stream(t.Context(), broker.StreamName(svc))
	if err != nil {
		t.Fatal(err)
	}

	if c := events.CachedInfo().Config; c.MaxAge != time.Hour || c.MaxBytes != 1<<20 || c.Duplicates != 30*time.Second {
		t.Fatalf("event stream %+v", c)
	}

	dead, err := jet.Stream(t.Context(), broker.DLQStreamName(svc))
	if err != nil {
		t.Fatal(err)
	}

	if c := dead.CachedInfo().Config; c.MaxAge != 3*time.Hour {
		t.Fatalf("dead-letter stream %+v", c)
	}
}
