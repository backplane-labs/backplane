package broker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xtrace"

	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

const (
	// ackMargin: the server redelivers an unacknowledged message only after
	// the handler timeout has surely passed.
	ackMargin = 15 * time.Second
	// stopDeliveries: the consumer's max_deliver is the reactor's plus
	// these. A delivery the service's stop interrupted is nacked and
	// counts on the server, but the SDK dead-letters only after a failed
	// last delivery; without the spare deliveries a message interrupted on
	// its last one would never come back.
	stopDeliveries = 5
	// where of backplane.panics.
	panicWhere = "reactor"
)

// Errors of reactors.
var (
	// ErrHandlerPanic: a reactor's handler panicked.
	ErrHandlerPanic = errors.New("broker: reactor handler panicked")
	// ErrStopTimeout: handlers in flight did not finish within the stop
	// budget and were cancelled.
	ErrStopTimeout = errors.New("broker: reactor handlers did not finish in time")
)

// reactors is the state of the running reactors.
type reactors struct {
	mu       sync.Mutex
	stopping bool
	stop     chan struct{}      // closed by StopReactors
	work     context.Context    //nolint:containedctx // parent of every handler's context
	cancel   context.CancelFunc // cancels handlers still running when the stop budget ends
	consumes []jetstream.ConsumeContext
	inflight sync.WaitGroup
}

// reactor is one declared reactor with its NATS names.
type reactor struct {
	event    string // <service>.<Event>
	consumer string
	source   string // stream of the event's service
	subject  string
	redrive  string // where backplane publishes this reactor's dead letters back
	durable  string
	dead     string // dead-letter subject
	handler  env.Handler
	delivery delivery
	pos      *position
}

// delivery is a reactor's env.Delivery over the broker's defaults.
type delivery struct {
	maxDeliver  int
	concurrency int
	timeout     time.Duration
	nak         backoff.Policy
	startAll    bool
	ordered     bool
	inactive    time.Duration
}

func (b *Broker) deliveryOf(d env.Delivery) delivery {
	out := delivery{
		maxDeliver: b.t.maxDeliver, concurrency: b.t.concurrency, timeout: b.t.handlerTimeout,
		nak: b.t.nak, startAll: d.StartAll, ordered: d.Ordered, inactive: d.InactiveThreshold,
	}

	if d.MaxDeliver > 0 {
		out.maxDeliver = d.MaxDeliver
	}

	if d.Concurrency > 0 {
		out.concurrency = d.Concurrency
	}

	if d.Timeout > 0 {
		out.timeout = d.Timeout
	}

	if d.Redelivery.Validate() == nil {
		out.nak = d.Redelivery
	}

	if d.Ordered {
		out.concurrency = 1
	}

	return out
}

func (b *Broker) reactorOf(r env.Reactor) (reactor, error) {
	service, name, err := SplitEvent(r.Event)
	if err != nil {
		return reactor{}, fmt.Errorf("reactor %s: %w", r.Consumer, err)
	}

	subject := Subject(service, name)
	if wire.IsPattern(r.Event) {
		subject = wire.EventsSubject(service)
	}

	return b.reactorAt(r, service, subject), nil
}

// reactorAt is r consuming subject in source's stream.
func (b *Broker) reactorAt(r env.Reactor, source, subject string) reactor {
	return reactor{
		event: r.Event, consumer: r.Consumer, source: source,
		subject: subject, redrive: RedriveSubject(source, b.p.Service, r.Consumer),
		durable: Durable(b.p.Service, r.Consumer),
		dead:    DLQSubject(b.p.Service, r.Consumer), handler: r.Handler, delivery: b.deliveryOf(r.Delivery),
		pos: newPosition(),
	}
}

// fields of the reactor's log lines, then extra. Loggers are not derived
// with With: it races with concurrent writes in xlog's JSON core.
func (re reactor) fields(extra ...xlog.Field) []xlog.Field {
	return append([]xlog.Field{xlog.String("reactor", re.consumer), xlog.String("event", re.event)}, extra...)
}

// StartReactors starts a durable consumer per declared reactor on g.
func (b *Broker) StartReactors(ctx context.Context, g node.Group) error {
	if b.p.Env == nil {
		return nil
	}

	list := make([]reactor, 0, len(b.p.Env.Reactors()))

	var everywhere []env.Reactor

	for _, r := range b.p.Env.Reactors() {
		if r.Event == wire.AllEvents {
			everywhere = append(everywhere, r)

			continue
		}

		re, err := b.reactorOf(r)
		if err != nil {
			return err
		}

		list = append(list, re)
	}

	st := &b.reactors
	st.mu.Lock()
	st.stop = make(chan struct{})
	st.work, st.cancel = context.WithCancel(context.WithoutCancel(ctx)) //nolint:gosec // StopReactors cancels
	st.mu.Unlock()

	for i := range list {
		g.Go(func(ctx context.Context) error { return b.run(ctx, list[i]) })
	}

	for i := range everywhere {
		g.Go(func(ctx context.Context) error { return b.everyStream(ctx, everywhere[i]) })
	}

	return nil
}

// everyStream runs r, a reactor of every service's events, on the stream
// of each service: the streams there are now and the ones created later,
// looked for every discoverEvery. One durable consumer per stream, all of
// the same name.
func (b *Broker) everyStream(ctx context.Context, r env.Reactor) error {
	var running sync.WaitGroup
	defer running.Wait()

	started := map[string]bool{}

	for {
		for _, source := range b.services(ctx) {
			if started[source] {
				continue
			}

			started[source] = true
			re := b.reactorAt(r, source, wire.EventsSubject(source))

			running.Go(func() { _ = b.run(ctx, re) })
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(b.t.discover):
		}
	}
}

// services are those whose event streams are on the server now; none
// while NATS is unreachable.
func (b *Broker) services(ctx context.Context) []string {
	jet, err := b.connected()
	if err != nil {
		return nil
	}

	lctx, cancel := context.WithTimeout(ctx, b.t.publishTimeout)
	defer cancel()

	names := jet.StreamNames(lctx)

	var out []string

	for name := range names.Name() {
		if service, ok := wire.ServiceOfStream(name); ok {
			out = append(out, service)
		}
	}

	if err := names.Err(); err != nil {
		b.log.Debug("event streams", xlog.Err(err))
	}

	return out
}

// run ensures the reactor's streams and consumer, retrying while NATS is
// unreachable, then consumes until ctx ends. A consumer deleted on the
// server is recreated at the reactor's position and consumption resumes.
func (b *Broker) run(ctx context.Context, re reactor) error {
	var resume uint64 // 0: the consumer's own position

	for {
		var (
			cc   jetstream.ConsumeContext
			lost <-chan error
		)

		err := backoff.Retry(ctx, b.t.retry, func(ctx context.Context) error {
			c, l, err := b.consume(ctx, re, resume)
			cc, lost = c, l

			return err
		}, func(err error, in time.Duration) {
			b.log.Warn("reactor not started, retrying", re.fields(xlog.Err(err), xlog.Duration("retry_in", in))...)
		})
		if err != nil {
			return nil //nolint:nilerr // only the node's stop ends the retries
		}

		if !b.track(cc) {
			return nil
		}

		b.log.Info("reactor consuming", re.fields(xlog.String("durable", re.durable))...)

		gone := b.watch(ctx, re, lost)

		cc.Stop()
		b.untrack(cc)

		if !gone {
			return nil
		}

		resume = re.pos.resume()
		b.log.Warn("reactor consumer deleted on the server, recreating",
			re.fields(xlog.String("durable", re.durable), xlog.Uint64("resume_seq", resume))...)
	}
}

// watch waits until ctx ends (false) or the consumer is gone from the
// server (true). A missed heartbeat alone may be a network blip: the
// consumer is looked up before it counts as gone.
func (b *Broker) watch(ctx context.Context, re reactor, lost <-chan error) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case err := <-lost:
			if !errors.Is(err, jetstream.ErrNoHeartbeat) || b.missing(ctx, re) {
				return true
			}
		}
	}
}

// missing reports whether the reactor's consumer is surely not on the
// server.
func (b *Broker) missing(ctx context.Context, re reactor) bool {
	jet, err := b.connected()
	if err != nil {
		return false
	}

	lctx, cancel := context.WithTimeout(ctx, b.t.publishTimeout)
	defer cancel()

	_, err = jet.Consumer(lctx, StreamName(re.source), re.durable)

	return errors.Is(err, jetstream.ErrConsumerNotFound) || errors.Is(err, jetstream.ErrStreamNotFound)
}

// consume ensures the source and dead-letter streams and the durable
// consumer, and starts consuming. resume > 0 starts a consumer that has to
// be created at that stream sequence. lost delivers the consume errors that
// may mean the consumer is gone.
func (b *Broker) consume(
	ctx context.Context, re reactor, resume uint64,
) (jetstream.ConsumeContext, <-chan error, error) {
	jet, err := b.connected()
	if err != nil {
		return nil, nil, err
	}

	if err := ensureExists(ctx, jet, b.sourceStream(re.source)); err != nil {
		return nil, nil, err
	}

	if err := b.ensureDead(ctx, jet); err != nil {
		return nil, nil, err
	}

	stream, err := jet.Stream(ctx, StreamName(re.source))
	if err != nil {
		return nil, nil, fmt.Errorf("broker: stream %s: %w", StreamName(re.source), err)
	}

	created := stream.CachedInfo().Created

	cfg, err := b.startAt(ctx, jet, re, resume, created)
	if err != nil {
		return nil, nil, err
	}

	cons, err := jet.CreateOrUpdateConsumer(ctx, StreamName(re.source), cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("broker: consumer %s: %w", re.durable, err)
	}

	re.pos.started(cons.CachedInfo().AckFloor.Stream, created)

	sem := make(chan struct{}, re.delivery.concurrency)
	lost := make(chan error, 1)

	cc, err := cons.Consume(func(msg jetstream.Msg) { b.dispatch(re, sem, msg) },
		jetstream.PullMaxMessages(re.delivery.concurrency),
		jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
			b.log.Debug("reactor consume", re.fields(xlog.Err(err))...)

			if errors.Is(err, jetstream.ErrConsumerDeleted) || errors.Is(err, jetstream.ErrConsumerNotFound) ||
				errors.Is(err, jetstream.ErrNoHeartbeat) {
				select {
				case lost <- err:
				default:
				}
			}
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("broker: consume %s: %w", re.durable, err)
	}

	return cc, lost, nil
}

// startAt is the consumer configuration with its start position. The
// server does not let an existing consumer's start change, so an existing
// consumer keeps its own; a missing one starts at resume when set, else
// where the reactor asks: new messages or the whole stream. resume counts
// in the stream the reactor consumed; when the stream (created at created)
// is another one — deleted and created again since — every message in it
// came after the loss, so the consumer starts at its first.
func (b *Broker) startAt(
	ctx context.Context, jet jetstream.JetStream, re reactor, resume uint64, created time.Time,
) (jetstream.ConsumerConfig, error) {
	cfg := b.consumerConfig(re)

	cons, err := jet.Consumer(ctx, StreamName(re.source), re.durable)

	switch {
	case err == nil:
		have := cons.CachedInfo().Config
		cfg.DeliverPolicy, cfg.OptStartSeq, cfg.OptStartTime = have.DeliverPolicy, have.OptStartSeq, have.OptStartTime
	case !errors.Is(err, jetstream.ErrConsumerNotFound):
		return cfg, fmt.Errorf("broker: consumer %s: %w", re.durable, err)
	case resume > 0 && re.pos.in(created):
		cfg.DeliverPolicy, cfg.OptStartSeq = jetstream.DeliverByStartSequencePolicy, resume
	case resume > 0:
		cfg.DeliverPolicy = jetstream.DeliverAllPolicy
	case re.delivery.startAll:
		cfg.DeliverPolicy = jetstream.DeliverAllPolicy
	}

	return cfg, nil
}

// consumerConfig: explicit acks and bounded deliveries. The consumer
// filters on its event's subject and on its redrive subject, where
// backplane publishes the reactor's dead letters back to it alone (§8,
// Redrive from the console). A failed handler
// is redelivered after a delay growing with the delivery count (NakWithDelay,
// see handle); a message never acknowledged (the process died) comes back
// after AckWait. The consumer's own BackOff is not used: the server measures
// a NAK delay against AckWait, so with BackOff the delays would drift. The
// SDK decides the dead letter; the server's MaxDeliver leaves room for
// deliveries a stop interrupted (stopDeliveries). An ordered reactor has
// one message in flight across all instances (MaxAckPending 1).
func (b *Broker) consumerConfig(re reactor) jetstream.ConsumerConfig {
	pending := 0 // the server's default
	if re.delivery.ordered {
		pending = 1
	}

	return jetstream.ConsumerConfig{
		Durable:           re.durable,
		Description:       "backplane reactor " + re.consumer + " of " + b.p.Service,
		FilterSubjects:    []string{re.subject, re.redrive},
		DeliverPolicy:     jetstream.DeliverNewPolicy,
		AckPolicy:         jetstream.AckExplicitPolicy,
		AckWait:           re.delivery.timeout + ackMargin,
		MaxDeliver:        re.delivery.maxDeliver + stopDeliveries,
		MaxAckPending:     pending,
		InactiveThreshold: re.delivery.inactive,
		Metadata: map[string]string{
			metaService: b.p.Service, wire.MetaConsumer: re.consumer, wire.MetaEvent: re.event,
		},
	}
}

// track records a started consume; false (and stopped) when the reactors
// are already stopping.
func (b *Broker) track(cc jetstream.ConsumeContext) bool {
	st := &b.reactors
	st.mu.Lock()
	defer st.mu.Unlock()

	if st.stopping {
		cc.Stop()

		return false
	}

	st.consumes = append(st.consumes, cc)

	return true
}

// untrack forgets a stopped consume.
func (b *Broker) untrack(cc jetstream.ConsumeContext) {
	st := &b.reactors
	st.mu.Lock()
	defer st.mu.Unlock()

	for i, c := range st.consumes {
		if c == cc {
			st.consumes = append(st.consumes[:i], st.consumes[i+1:]...)

			return
		}
	}
}

// dispatch runs the handler of one message on its own goroutine, at most
// the reactor's concurrency at once. JetStream calls it sequentially, so
// a full semaphore holds back the next message.
func (b *Broker) dispatch(re reactor, sem chan struct{}, msg jetstream.Msg) {
	st := &b.reactors

	select {
	case sem <- struct{}{}:
	case <-st.stop:
		_ = msg.Nak() // redelivered after the restart either way

		return
	}

	st.mu.Lock()
	if st.stopping {
		st.mu.Unlock()
		<-sem

		_ = msg.Nak() // redelivered after the restart either way

		return
	}

	st.inflight.Add(1)
	st.mu.Unlock()

	go func() {
		defer st.inflight.Done()
		defer func() { <-sem }()

		b.handle(st.work, re, msg)
	}()
}

// handle runs the handler in the trace of the event and settles the
// message: ack; nak with a delay; dead-letter after the last delivery or
// on a terminal error; a plain nak, never a dead letter, when the stop
// cancelled the handler.
func (b *Broker) handle(work context.Context, re reactor, msg jetstream.Msg) {
	delivered, seq := uint64(1), uint64(0)
	if md, err := msg.Metadata(); err == nil {
		delivered, seq = md.NumDelivered, md.Sequence.Stream
		re.pos.delivered(seq)
	}

	ctx := otel.GetTextMapPropagator().Extract(work, carrier(msg.Headers()))
	start := time.Now()
	err := b.invoke(ctx, re, msg.Subject(), msg.Data(), msg.Headers(), delivered)
	took := time.Since(start)

	log := b.log.Ctx()

	var terminal env.NonRetryableError

	switch {
	case err == nil:
		metrics.ReactorHandled(ctx, re.consumer, metrics.Ack, took)

		if err := msg.Ack(); err != nil {
			log.Warn(ctx, "reactor ack", re.fields(xlog.Err(err))...)

			return
		}

		re.pos.settled(seq)
	case work.Err() != nil:
		// The stop cancelled the handler: not the message's failure.
		metrics.ReactorHandled(ctx, re.consumer, metrics.Nak, took)
		log.Info(ctx, "reactor stopped mid-handler, message returned",
			re.fields(xlog.Err(err), xlog.Uint64("delivered", delivered))...)

		if err := msg.Nak(); err != nil {
			log.Warn(ctx, "reactor nak", re.fields(xlog.Err(err))...)
		}
	case errors.As(err, &terminal):
		metrics.ReactorHandled(ctx, re.consumer, metrics.Terminal, took)
		b.deadLetter(ctx, re, msg, err, delivered)
		re.pos.settled(seq)
	case delivered < uint64(re.delivery.maxDeliver): //nolint:gosec // positive
		metrics.ReactorHandled(ctx, re.consumer, metrics.Nak, took)
		log.Warn(ctx, "reactor failed, redelivering", re.fields(xlog.Err(err), xlog.Uint64("delivered", delivered))...)

		if err := msg.NakWithDelay(nakDelay(re.delivery.nak, delivered)); err != nil {
			log.Warn(ctx, "reactor nak", re.fields(xlog.Err(err))...)
		}
	default:
		metrics.ReactorHandled(ctx, re.consumer, metrics.Dead, took)
		b.deadLetter(ctx, re, msg, err, delivered)
		re.pos.settled(seq)
	}
}

// invoke runs the reactor's handler on one event: in a consumer span, with
// the reactor's timeout and the event's metadata (env.Incoming) on ctx.
func (b *Broker) invoke(
	ctx context.Context, re reactor, subject string, data []byte, h nats.Header, attempt uint64,
) error {
	ctx = env.WithIncoming(ctx, incoming(h, re.consumer, attempt))

	hctx, cancel := context.WithTimeout(ctx, re.delivery.timeout)
	defer cancel()

	//nolint:wrapcheck // the handler's error as it is
	return xtrace.Run(hctx, otel.Tracer(instrumentation), "process "+re.event,
		func(ctx context.Context, _ trace.Span) error { return call(ctx, re.handler, data) },
		xtrace.WithSpanOptions(trace.WithSpanKind(trace.SpanKindConsumer)), xtrace.WithAttrs(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination.name", subject),
			attribute.String("messaging.consumer.group.name", re.durable),
			attribute.Int64("messaging.message.delivery_count", int64(min(attempt, math.MaxInt64))), //nolint:gosec // bounded
		))
}

// deadLetter publishes the message to the reactor's dead-letter subject and
// terminates it. It publishes even when the stop cancelled ctx.
func (b *Broker) deadLetter(ctx context.Context, re reactor, msg jetstream.Msg, cause error, n uint64) {
	id := re.durable + ":" + msg.Subject()
	if md, err := msg.Metadata(); err == nil {
		id = re.durable + ":" + md.Stream + ":" + strconv.FormatUint(md.Sequence.Stream, 10)
	}

	dead := &nats.Msg{
		Subject: re.dead, Data: msg.Data(), Header: deadHeaders(msg.Headers(), id, re.consumer, cause.Error(), n),
	}

	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), b.t.publishTimeout)
	defer cancel()

	jet, err := b.connected()
	if err == nil {
		_, err = jet.PublishMsg(pctx, dead)
	}

	log := b.log.Ctx()

	if err != nil {
		// The server will not deliver it again: the event stays in its
		// stream only.
		log.Error(ctx, "reactor gave up, dead letter not published", re.fields(
			xlog.Err(err), xlog.String("cause", cause.Error()), xlog.String("dead_letter", id), xlog.Uint64("delivered", n))...)
	} else {
		log.Error(ctx, "reactor gave up, dead-lettered", re.fields(
			xlog.Err(cause), xlog.String("subject", re.dead), xlog.Uint64("delivered", n))...)
	}

	if err := msg.TermWithReason("dead-lettered"); err != nil {
		log.Warn(ctx, "reactor term", re.fields(xlog.Err(err))...)
	}
}

// call runs h; a panic is an error, counted in backplane.panics.
func call(ctx context.Context, h env.Handler, in []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			metrics.Panic(ctx, panicWhere)

			err = fmt.Errorf("%w: %v\n%s", ErrHandlerPanic, r, debug.Stack())
		}
	}()

	_, err = h(ctx, in)

	return err
}

// nakDelay after the n-th failed delivery: p.Min doubling up to p.Max.
func nakDelay(p backoff.Policy, n uint64) time.Duration {
	d := p.Min
	for i := uint64(1); i < n && d < p.Max; i++ {
		d *= 2
	}

	return min(d, p.Max)
}

// nakGrace is how long cancelled handlers get to nak at stop.
const nakGrace = time.Second

// StopReactors stops consuming and waits for handlers in flight; when ctx
// ends first, their contexts are cancelled and get nakGrace to nak while
// the connection is still open.
func (b *Broker) StopReactors(ctx context.Context) error {
	st := &b.reactors

	st.mu.Lock()
	if st.stopping || st.stop == nil {
		st.mu.Unlock()

		return nil
	}

	st.stopping = true
	close(st.stop)
	consumes := st.consumes
	st.consumes = nil
	st.mu.Unlock()

	for _, cc := range consumes {
		cc.Stop()
	}

	done := make(chan struct{})

	go func() {
		st.inflight.Wait()
		close(done)
	}()

	defer st.cancel()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}

	// Out of time: cancel the handlers and give them a moment to nak while
	// the connection is still open, so their messages come back at once
	// instead of after ack_wait.
	st.cancel()

	grace := time.NewTimer(nakGrace)
	defer grace.Stop()

	select {
	case <-done:
	case <-grace.C:
	}

	return fmt.Errorf("%w: %w", ErrStopTimeout, ctx.Err())
}
