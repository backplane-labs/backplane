package broker_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/trace"

	"github.com/gopherex/xlog"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	infranats "github.com/backplane-labs/backplane/pkg/backplane/infra/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/broker"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

const (
	waitFor    = 15 * time.Second
	maxDeliver = 3
)

var errBoom = errors.New("boom")

// group runs node goroutines until the test ends.
type group struct {
	ctx context.Context
	wg  sync.WaitGroup
}

func newGroup(t *testing.T) *group {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	g := &group{ctx: ctx}

	t.Cleanup(func() {
		cancel()
		g.wg.Wait()
	})

	return g
}

func (g *group) Go(fn func(ctx context.Context) error) {
	g.wg.Go(func() { _ = fn(g.ctx) })
}

// emitter is the env of a service that declared the event Greeted.
func emitter(t *testing.T, service string) *env.Env {
	t.Helper()

	e := env.New(service, manifest.New(service, "1.0.0"))
	e.Manifest.Event(&backplanev1.Event{Name: "Greeted"})

	return e
}

// subscriber is the env of a service with one reactor on src.Greeted.
func subscriber(t *testing.T, service, src string, h env.Handler) (*env.Env, string) {
	t.Helper()

	e := env.New(service, manifest.New(service, "1.0.0"))
	consumer := "mailer:" + src + ".Greeted"
	e.Reactor(env.Reactor{Event: src + ".Greeted", Consumer: consumer, Handler: h})

	return e, consumer
}

func natsURL(t *testing.T) string {
	t.Helper()

	addr := os.Getenv("BACKPLANE_TEST_NATS")
	if addr == "" {
		t.Skip("BACKPLANE_TEST_NATS not set (make upstream)")
	}

	return "nats://" + addr
}

func unique(prefix string) string {
	var b [4]byte

	_, _ = rand.Read(b[:])

	return prefix + "-" + hex.EncodeToString(b[:])
}

// admin is a plain JetStream client for assertions; it deletes the streams
// of the given services when the test ends.
func admin(t *testing.T, url string, services ...string) jetstream.JetStream {
	t.Helper()

	conn, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}

	jet, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		for _, s := range services {
			_ = jet.DeleteStream(context.Background(), broker.StreamName(s))
			_ = jet.DeleteStream(context.Background(), broker.DLQStreamName(s))
		}

		conn.Close()
	})

	return jet
}

// open connects a broker with short timings; it closes with the test.
func open(t *testing.T, url, service string, e *env.Env) *broker.Broker {
	t.Helper()

	return openWith(t, broker.Params{Conn: infranats.Config{URL: config.Secret(url)}, Service: service, Env: e})
}

// openWith is open with p; Instance, Version and Log are filled in.
func openWith(t *testing.T, p broker.Params) *broker.Broker {
	t.Helper()

	p.Log = testlog.Discard()
	if os.Getenv("BROKER_TEST_LOG") != "" {
		p.Log = xlog.NewJSON(xlog.WithWriter(os.Stderr))
	}

	p.Instance, p.Version = p.Service+"-1", "1.0.0"

	b := broker.New(p)
	b.Fast(maxDeliver)

	if err := b.Connect(t.Context(), newGroup(t)); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = b.StopReactors(ctx)
		_ = b.Close(ctx)
	})

	return b
}

func startReactors(t *testing.T, jet jetstream.JetStream, b *broker.Broker, src, durable string) {
	t.Helper()

	if err := b.StartReactors(t.Context(), newGroup(t)); err != nil {
		t.Fatal(err)
	}

	eventually(t, "consumer "+durable, func() bool {
		_, err := jet.Consumer(t.Context(), broker.StreamName(src), durable)

		return err == nil
	})
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

func lastMsg(t *testing.T, jet jetstream.JetStream, stream, subject string) *jetstream.RawStreamMsg {
	t.Helper()

	s, err := jet.Stream(t.Context(), stream)
	if err != nil {
		t.Fatal(err)
	}

	m, err := s.GetLastMsgForSubject(t.Context(), subject)
	if err != nil {
		t.Fatal(err)
	}

	return m
}

func TestPublishIntoStream(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	svc := unique("pub")
	jet := admin(t, url, svc)
	b := open(t, url, svc, emitter(t, svc))

	if err := b.PublishRaw(t.Context(), svc+".Greeted", "user-1", []byte(`{"name":"ann"}`)); err != nil {
		t.Fatal(err)
	}

	m := lastMsg(t, jet, broker.StreamName(svc), broker.Subject(svc, "Greeted"))
	if string(m.Data) != `{"name":"ann"}` {
		t.Fatalf("data %s", m.Data)
	}

	for k, want := range map[string]string{
		"ce-specversion": "1.0", "ce-source": svc, "ce-type": svc + ".Greeted", "ce-subject": "user-1",
		"ce-datacontenttype": "application/json", "ce-instance": svc + "-1", "ce-version": "1.0.0",
	} {
		if got := m.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}

	id := m.Header.Get("ce-id")
	if id == "" || m.Header.Get(jetstream.MsgIDHeader) != id {
		t.Fatalf("ids %q %q", id, m.Header.Get(jetstream.MsgIDHeader))
	}

	if _, err := time.Parse(time.RFC3339Nano, m.Header.Get("ce-time")); err != nil {
		t.Fatal(err)
	}

	// A retry of the same event (same ce-id) is dropped by JetStream.
	ack, err := jet.PublishMsg(t.Context(), &nats.Msg{Subject: m.Subject, Data: m.Data, Header: m.Header})
	if err != nil || !ack.Duplicate {
		t.Fatalf("duplicate: %+v %v", ack, err)
	}

	s, err := jet.Stream(t.Context(), broker.StreamName(svc))
	if err != nil {
		t.Fatal(err)
	}

	if info, err := s.Info(t.Context()); err != nil || info.State.Msgs != 1 {
		t.Fatalf("stream holds %+v %v", info, err)
	}
}

func TestReactorReceives(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	type got struct {
		payload string
		trace   trace.TraceID
	}

	received := make(chan got, 1)
	e, consumer := subscriber(t, sub, src, func(ctx context.Context, in []byte) ([]byte, error) {
		received <- got{payload: string(in), trace: trace.SpanContextFromContext(ctx).TraceID()}

		return nil, nil
	})

	pub := open(t, url, src, emitter(t, src))
	recv := open(t, url, sub, e)
	durable := broker.Durable(sub, consumer)
	startReactors(t, jet, recv, src, durable)

	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{9, 9, 9}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled,
	})
	if err := pub.PublishRaw(trace.ContextWithSpanContext(t.Context(), spanCtx), src+".Greeted", "", []byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}

	select {
	case g := <-received:
		if g.payload != `{"n":1}` || g.trace != spanCtx.TraceID() {
			t.Fatalf("got %+v", g)
		}
	case <-time.After(waitFor):
		t.Fatal("not received")
	}

	eventually(t, "acked", func() bool {
		c, err := jet.Consumer(t.Context(), broker.StreamName(src), durable)
		if err != nil {
			return false
		}

		info, err := c.Info(t.Context())

		return err == nil && info.NumAckPending == 0 && info.NumPending == 0 && info.AckFloor.Stream == 1
	})
}

func TestDeadLetter(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	var calls atomic.Int32

	e, consumer := subscriber(t, sub, src, func(context.Context, []byte) ([]byte, error) {
		calls.Add(1)

		return nil, errBoom
	})

	pub := open(t, url, src, emitter(t, src))
	recv := open(t, url, sub, e)
	startReactors(t, jet, recv, src, broker.Durable(sub, consumer))

	if err := pub.PublishRaw(t.Context(), src+".Greeted", "k", []byte(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}

	subject := broker.DLQSubject(sub, consumer)

	eventually(t, "dead letter", func() bool {
		s, err := jet.Stream(t.Context(), broker.DLQStreamName(sub))
		if err != nil {
			return false
		}

		_, err = s.GetLastMsgForSubject(t.Context(), subject)

		return err == nil
	})

	m := lastMsg(t, jet, broker.DLQStreamName(sub), subject)
	orig := lastMsg(t, jet, broker.StreamName(src), broker.Subject(src, "Greeted"))

	if string(m.Data) != `{"n":2}` || m.Header.Get("ce-id") != orig.Header.Get("ce-id") || m.Header.Get("ce-subject") != "k" {
		t.Fatalf("dead letter %s %v", m.Data, m.Header)
	}

	if !strings.Contains(m.Header.Get("bp-error"), "boom") || m.Header.Get("bp-consumer") != consumer ||
		m.Header.Get("bp-delivered") != "3" {
		t.Fatalf("dead letter headers %v", m.Header)
	}

	time.Sleep(200 * time.Millisecond) // no delivery after the last one

	if n := calls.Load(); n != 3 {
		t.Fatalf("handler called %d times", n)
	}
}

func streamBy(t *testing.T, jet jetstream.JetStream, service string) string {
	t.Helper()

	s, err := jet.Stream(t.Context(), broker.StreamName(service))
	if err != nil {
		t.Fatal(err)
	}

	return s.CachedInfo().Config.Metadata["bp.ensured-by"]
}

func TestSubscriberCreatesStream(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub, other := unique("src"), unique("sub"), unique("sub")
	jet := admin(t, url, src, sub, other)

	e, consumer := subscriber(t, sub, src, func(context.Context, []byte) ([]byte, error) { return nil, nil })
	startReactors(t, jet, open(t, url, sub, e), src, broker.Durable(sub, consumer))

	if by := streamBy(t, jet, src); by != "subscriber" {
		t.Fatalf("created by %q", by)
	}

	// The emitter's start takes the stream over.
	open(t, url, src, emitter(t, src))

	if by := streamBy(t, jet, src); by != "emitter" {
		t.Fatalf("after emitter start: %q", by)
	}

	// Another subscriber leaves it alone.
	e2, consumer2 := subscriber(t, other, src, func(context.Context, []byte) ([]byte, error) { return nil, nil })
	startReactors(t, jet, open(t, url, other, e2), src, broker.Durable(other, consumer2))

	if by := streamBy(t, jet, src); by != "emitter" {
		t.Fatalf("after second subscriber: %q", by)
	}
}

// proxy forwards to target once enabled; before that it drops connections.
type proxy struct {
	ln      net.Listener
	target  string
	enabled atomic.Bool

	mu     sync.Mutex
	active map[net.Conn]bool
}

func newProxy(t *testing.T, target string) *proxy {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	p := &proxy{ln: ln, target: target, active: map[net.Conn]bool{}}

	var conns sync.WaitGroup

	t.Cleanup(func() {
		_ = ln.Close()

		conns.Wait()
	})

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}

			conns.Go(func() { p.serve(c) })
		}
	}()

	return p
}

// cut disables the proxy and breaks the connections it carries.
func (p *proxy) cut() {
	p.enabled.Store(false)

	p.mu.Lock()
	defer p.mu.Unlock()

	for c := range p.active {
		_ = c.Close()
	}
}

func (p *proxy) serve(c net.Conn) {
	defer c.Close()

	if !p.enabled.Load() {
		return
	}

	p.mu.Lock()
	p.active[c] = true
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.active, c)
		p.mu.Unlock()
	}()

	upstream, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", p.target)
	if err != nil {
		return
	}
	defer upstream.Close()

	done := make(chan struct{}, 2)

	go func() { _, _ = io.Copy(upstream, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, upstream); done <- struct{}{} }()

	<-done
}

func TestUnreachableThenReachable(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	svc := unique("late")
	jet := admin(t, url, svc)
	p := newProxy(t, strings.TrimPrefix(url, "nats://"))

	start := time.Now()
	b := open(t, "nats://"+p.ln.Addr().String(), svc, emitter(t, svc))

	if took := time.Since(start); took > time.Second {
		t.Fatalf("connect blocked %v", took)
	}

	if err := b.PublishRaw(t.Context(), svc+".Greeted", "", []byte(`{}`)); !errors.Is(err, env.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}

	p.enabled.Store(true)

	eventually(t, "publish after reconnect", func() bool {
		return b.PublishRaw(t.Context(), svc+".Greeted", "", []byte(`{"late":true}`)) == nil
	})

	if m := lastMsg(t, jet, broker.StreamName(svc), broker.Subject(svc, "Greeted")); string(m.Data) != `{"late":true}` {
		t.Fatalf("data %s", m.Data)
	}
}

func TestStopWaitsForHandler(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	started, release := make(chan struct{}), make(chan struct{})
	e, consumer := subscriber(t, sub, src, func(context.Context, []byte) ([]byte, error) {
		close(started)
		<-release

		return nil, nil
	})

	pub := open(t, url, src, emitter(t, src))
	recv := open(t, url, sub, e)
	durable := broker.Durable(sub, consumer)
	startReactors(t, jet, recv, src, durable)

	if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-started:
	case <-time.After(waitFor):
		t.Fatal("handler not started")
	}

	stopped := make(chan error, 1)

	go func() { stopped <- recv.StopReactors(context.Background()) }()

	select {
	case err := <-stopped:
		t.Fatalf("stop returned with a handler in flight: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(waitFor):
		t.Fatal("stop did not return")
	}

	eventually(t, "acked", func() bool {
		c, err := jet.Consumer(t.Context(), broker.StreamName(src), durable)
		if err != nil {
			return false
		}

		info, err := c.Info(t.Context())

		return err == nil && info.NumAckPending == 0 && info.AckFloor.Stream == 1
	})
}

func TestStopBudgetCancelsHandler(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	started, cancelled := make(chan struct{}), make(chan struct{})
	e, consumer := subscriber(t, sub, src, func(ctx context.Context, _ []byte) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)

		return nil, ctx.Err()
	})

	pub := open(t, url, src, emitter(t, src))
	recv := open(t, url, sub, e)
	startReactors(t, jet, recv, src, broker.Durable(sub, consumer))

	if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	<-started

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	if err := recv.StopReactors(ctx); !errors.Is(err, broker.ErrStopTimeout) {
		t.Fatalf("want ErrStopTimeout, got %v", err)
	}

	select {
	case <-cancelled:
	case <-time.After(waitFor):
		t.Fatal("handler not cancelled")
	}
}

func TestReactorAfterReconnect(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)
	p := newProxy(t, strings.TrimPrefix(url, "nats://"))
	p.enabled.Store(true)

	received := make(chan string, 2)
	e, consumer := subscriber(t, sub, src, func(_ context.Context, in []byte) ([]byte, error) {
		received <- string(in)

		return nil, nil
	})

	pub := open(t, url, src, emitter(t, src))
	recv := open(t, "nats://"+p.ln.Addr().String(), sub, e)
	startReactors(t, jet, recv, src, broker.Durable(sub, consumer))

	for i, payload := range []string{`{"n":1}`, `{"n":2}`} {
		if i == 1 {
			p.cut()
			time.Sleep(300 * time.Millisecond)
			p.enabled.Store(true)
		}

		if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(payload)); err != nil {
			t.Fatal(err)
		}

		select {
		case got := <-received:
			if got != payload {
				t.Fatalf("got %s, want %s", got, payload)
			}
		case <-time.After(waitFor):
			t.Fatalf("%s not received", payload)
		}
	}
}

// A consumer deleted on the server is recreated where the reactor stopped:
// an event published while it was gone is still handled, and a restart
// keeps the recreated consumer.
func TestReactorRecreatesDeletedConsumer(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	received := make(chan string, 4)
	e, consumer := subscriber(t, sub, src, func(_ context.Context, in []byte) ([]byte, error) {
		received <- string(in)

		return nil, nil
	})

	pub := open(t, url, src, emitter(t, src))
	recv := open(t, url, sub, e)
	durable := broker.Durable(sub, consumer)
	startReactors(t, jet, recv, src, durable)

	expect := func(payload string) {
		t.Helper()

		if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(payload)); err != nil {
			t.Fatal(err)
		}

		select {
		case got := <-received:
			if got != payload {
				t.Fatalf("got %s, want %s", got, payload)
			}
		case <-time.After(waitFor):
			t.Fatalf("%s not received", payload)
		}
	}

	expect(`{"n":1}`)

	if err := jet.DeleteConsumer(t.Context(), broker.StreamName(src), durable); err != nil {
		t.Fatal(err)
	}

	expect(`{"n":2}`) // published while the consumer may still be missing

	// A restart finds the recreated consumer and keeps its start position.
	if err := recv.StopReactors(t.Context()); err != nil {
		t.Fatal(err)
	}

	startReactors(t, jet, open(t, url, sub, e), src, durable)
	expect(`{"n":3}`)
}

// A stream deleted and created again numbers from 1: the reactor's
// consumer, recreated, starts at the new stream's first message instead of
// waiting for the old stream's sequence.
func TestReactorFollowsRecreatedStream(t *testing.T) {
	t.Parallel()

	url := natsURL(t)
	src, sub := unique("src"), unique("sub")
	jet := admin(t, url, src, sub)

	received := make(chan string, 8)
	e, consumer := subscriber(t, sub, src, func(_ context.Context, in []byte) ([]byte, error) {
		received <- string(in)

		return nil, nil
	})

	pub := open(t, url, src, emitter(t, src))
	recv := open(t, url, sub, e)
	durable := broker.Durable(sub, consumer)
	startReactors(t, jet, recv, src, durable)

	publish := func(payload string) {
		t.Helper()

		if err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(payload string) {
		t.Helper()

		select {
		case got := <-received:
			if got != payload {
				t.Fatalf("got %s, want %s", got, payload)
			}
		case <-time.After(waitFor):
			t.Fatalf("%s not received", payload)
		}
	}

	for _, payload := range []string{`{"n":1}`, `{"n":2}`, `{"n":3}`} {
		publish(payload)
		expect(payload)
	}

	if err := jet.DeleteStream(t.Context(), broker.StreamName(src)); err != nil {
		t.Fatal(err)
	}

	// The publisher and the reactor both create the stream again; either way
	// its first message is sequence 1, below where the reactor stopped.
	deadline := time.Now().Add(waitFor)

	for {
		err := pub.PublishRaw(t.Context(), src+".Greeted", "", []byte(`{"n":4}`))
		if err == nil {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal(err)
		}

		time.Sleep(50 * time.Millisecond)
	}

	expect(`{"n":4}`)
}
