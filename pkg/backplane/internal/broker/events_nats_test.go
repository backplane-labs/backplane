package broker_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/broker"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// deadCount is the number of dead letters of sub's consumer.
func deadCount(t *testing.T, jet jetstream.JetStream, sub, consumer string) uint64 {
	t.Helper()

	s, err := jet.Stream(t.Context(), broker.DLQStreamName(sub))
	if err != nil {
		t.Fatal(err)
	}

	info, err := s.Info(t.Context(), jetstream.WithSubjectFilter(broker.DLQSubject(sub, consumer)))
	if err != nil {
		t.Fatal(err)
	}

	var n uint64
	for _, c := range info.State.Subjects {
		n += c
	}

	return n
}

func TestConnectedAndJetStream(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	svc := unique("conn")
	admin(t, url, svc)
	b := open(t, url, svc, emitter(t, svc))

	eventually(t, "connected", b.Connected)

	if js, err := b.JetStream(); err != nil || js == nil {
		t.Fatalf("jetstream %v %v", js, err)
	}
}

// A handler the stop cancelled is nacked, never dead-lettered, even on its
// last delivery; the next instance gets the message again.
func TestStopNaksWithoutDeadLetter(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	started := make(chan struct{})
	e, consumer := reacting(sub, src, env.Delivery{MaxDeliver: 1}, func(ctx context.Context, _ []byte) ([]byte, error) {
		close(started)
		<-ctx.Done()

		return nil, ctx.Err()
	})

	pub := open(t, url, src, emitter(t, src))
	recv := open(t, url, sub, e)
	durable := broker.Durable(sub, consumer)
	startReactors(t, jet, recv, src, durable)

	if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-started:
	case <-time.After(waitFor):
		t.Fatal("handler not started")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	if err := recv.StopReactors(ctx); !errors.Is(err, broker.ErrStopTimeout) {
		t.Fatalf("want ErrStopTimeout, got %v", err)
	}

	// The connection closes right after the stop, as NATS does later in the
	// service's stop: the nak must already be out, or the message would
	// only come back after ack_wait (well past waitFor).
	if err := recv.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	attempts := make(chan int, 1)
	next, _ := reacting(sub, src, env.Delivery{MaxDeliver: 1}, func(ctx context.Context, _ []byte) ([]byte, error) {
		in, _ := env.IncomingOf(ctx)
		attempts <- in.Attempt

		return nil, nil
	})
	startReactors(t, jet, open(t, url, sub, next), src, durable)

	select {
	case n := <-attempts:
		if n != 2 {
			t.Fatalf("attempt %d, want 2", n)
		}
	case <-time.After(waitFor):
		t.Fatal("message not delivered again after the stop")
	}

	if n := deadCount(t, jet, sub, consumer); n != 0 {
		t.Fatalf("%d dead letters", n)
	}
}

// Metadata set at publish reaches the handler's env.Incoming.
func TestPublishMetadataReachesHandler(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	got := make(chan env.Incoming, 1)
	e, consumer := subscriber(t, sub, src, func(ctx context.Context, _ []byte) ([]byte, error) {
		in, ok := env.IncomingOf(ctx)
		if !ok {
			return nil, errBoom
		}

		got <- in

		return nil, nil
	})

	pub := open(t, url, src, emitter(t, src))
	startReactors(t, jet, open(t, url, sub, e), src, broker.Durable(sub, consumer))

	published := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	if err := pub.Publish(t.Context(), env.Message{
		Event: src + ".Greeted", Key: "user-7", ID: "order-42", Time: published, Payload: []byte(`{}`),
		Extensions: map[string]string{"tenant": "t1"},
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case in := <-got:
		if in.ID != "order-42" || in.Source != src || in.Type != src+".Greeted" || in.Subject != "user-7" ||
			!in.Time.Equal(published) || in.Attempt != 1 || in.Consumer != consumer || in.Extensions["tenant"] != "t1" ||
			in.Extensions["instance"] != src+"-1" {
			t.Fatalf("incoming %+v", in)
		}
	case <-time.After(waitFor):
		t.Fatal("not received")
	}
}

// A publish repeated with the same id is stored once.
func TestPublishIDDeduplicates(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	svc := unique("dedup")
	jet := admin(t, url, svc)
	b := open(t, url, svc, emitter(t, svc))

	for range 3 {
		if err := b.Publish(t.Context(), env.Message{Event: svc + ".Greeted", ID: "row-1", Payload: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}

	if err := b.Publish(t.Context(), env.Message{Event: svc + ".Greeted", ID: "row-2", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}

	s, err := jet.Stream(t.Context(), broker.StreamName(svc))
	if err != nil {
		t.Fatal(err)
	}

	if info, err := s.Info(t.Context()); err != nil || info.State.Msgs != 2 {
		t.Fatalf("stream holds %+v %v", info.State, err)
	}
}

// A terminal error dead-letters on the first delivery.
func TestTerminalDeadLettersAtOnce(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	var calls atomic.Int32

	e, consumer := subscriber(t, sub, src, func(context.Context, []byte) ([]byte, error) {
		calls.Add(1)

		return nil, env.NonRetryableError{Err: errBoom}
	})

	pub := open(t, url, src, emitter(t, src))
	startReactors(t, jet, open(t, url, sub, e), src, broker.Durable(sub, consumer))

	if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	m := deadLetter(t, jet, sub, consumer)
	if m.Header.Get("bp-delivered") != "1" {
		t.Fatalf("delivered %q", m.Header.Get("bp-delivered"))
	}

	time.Sleep(200 * time.Millisecond) // no redelivery

	if n := calls.Load(); n != 1 {
		t.Fatalf("handler called %d times", n)
	}
}

// An ordered reactor has one message in flight across instances and
// handles them in stream order.
func TestOrderedAcrossInstances(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	const events = 8

	var (
		inflight, peak atomic.Int32
		lock           sync.Mutex
		order          []int
		done           = make(chan struct{})
	)

	handler := func(_ context.Context, in []byte) ([]byte, error) {
		n := inflight.Add(1)
		defer inflight.Add(-1)

		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}

		time.Sleep(20 * time.Millisecond)

		v, _ := strconv.Atoi(string(in))

		lock.Lock()
		defer lock.Unlock()

		order = append(order, v)
		if len(order) == events {
			close(done)
		}

		return nil, nil
	}

	d := env.Delivery{Ordered: true}
	e1, consumer := reacting(sub, src, d, handler)
	e2, _ := reacting(sub, src, d, handler)
	durable := broker.Durable(sub, consumer)

	pub := open(t, url, src, emitter(t, src))
	startReactors(t, jet, open(t, url, sub, e1), src, durable)
	startReactors(t, jet, open(t, url, sub, e2), src, durable)

	if cfg := consumerConfig(t, jet, src, durable); cfg.MaxAckPending != 1 {
		t.Fatalf("max_ack_pending %d", cfg.MaxAckPending)
	}

	for i := range events {
		if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case <-done:
	case <-time.After(waitFor):
		t.Fatal("not all handled")
	}

	lock.Lock()
	defer lock.Unlock()

	if !slices.IsSorted(order) || peak.Load() != 1 {
		t.Fatalf("order %v, peak in flight %d", order, peak.Load())
	}
}

func TestInactiveThreshold(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	e, consumer := reacting(sub, src, env.Delivery{InactiveThreshold: time.Hour},
		func(context.Context, []byte) ([]byte, error) { return nil, nil })
	durable := broker.Durable(sub, consumer)
	startReactors(t, jet, open(t, url, sub, e), src, durable)

	if cfg := consumerConfig(t, jet, src, durable); cfg.InactiveThreshold != time.Hour {
		t.Fatalf("inactive_threshold %v", cfg.InactiveThreshold)
	}
}

// Redrive runs the reactor's handler on its dead letters, deletes those it
// handled and keeps the ones that fail again.
func TestRedrive(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	var (
		broken atomic.Bool
		lock   sync.Mutex
		seen   []string
	)

	broken.Store(true)

	e, consumer := subscriber(t, sub, src, func(ctx context.Context, in []byte) ([]byte, error) {
		if broken.Load() || string(in) == `"poison"` {
			return nil, env.NonRetryableError{Err: errBoom}
		}

		d, _ := env.IncomingOf(ctx)

		lock.Lock()
		defer lock.Unlock()

		seen = append(seen, fmt.Sprintf("%s@%d", in, d.Attempt))

		return nil, nil
	})

	pub := open(t, url, src, emitter(t, src))
	recv := open(t, url, sub, e)
	startReactors(t, jet, recv, src, broker.Durable(sub, consumer))

	if n, err := recv.Redrive(t.Context(), consumer); n != 0 || err != nil {
		t.Fatalf("empty redrive: %d %v", n, err)
	}

	for _, v := range []string{`"a"`, `"poison"`, `"b"`} {
		if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(v)); err != nil {
			t.Fatal(err)
		}
	}

	eventually(t, "3 dead letters", func() bool { return deadCount(t, jet, sub, consumer) == 3 })

	broken.Store(false)

	n, err := recv.Redrive(t.Context(), consumer)
	if n != 2 || !errors.Is(err, broker.ErrRedrive) || !errors.Is(err, errBoom) {
		t.Fatalf("redrive: %d %v", n, err)
	}

	lock.Lock()
	got := slices.Clone(seen)
	lock.Unlock()

	if !slices.Equal(got, []string{`"a"@2`, `"b"@2`}) {
		t.Fatalf("handled %v", got)
	}

	if left := deadCount(t, jet, sub, consumer); left != 1 {
		t.Fatalf("%d dead letters left", left)
	}

	if _, err := recv.Redrive(t.Context(), "nobody"); !errors.Is(err, broker.ErrNoReactor) {
		t.Fatalf("unknown consumer: %v", err)
	}
}
