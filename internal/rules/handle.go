package rules

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
)

// Redelivery of an event whose run did not start: nakFirst after the first
// failure, doubling up to nakMax. Deliveries are not bounded: while
// Temporal is down the events wait in their stream.
const (
	nakFirst = time.Second
	nakMax   = time.Minute
)

// program is what a consumer evaluates: swapped whole when the rule
// changes, read by every delivery.
type program struct {
	prog    *bindings.Program
	version int64
}

// consumer is a rule's durable consumer being consumed.
type consumer struct {
	id    uuid.UUID
	state running
	prog  atomic.Pointer[program]
	cc    jetstream.ConsumeContext
	sem   chan struct{}
	// lost is set when consumption reported the consumer gone; the next
	// reconciliation looks it up.
	lost atomic.Bool
}

// message is what handling needs of a delivery (jetstream.Msg).
type message interface {
	Data() []byte
	Headers() nats.Header
	Metadata() (*jetstream.MsgMetadata, error)
	Ack() error
	Nak() error
	NakWithDelay(delay time.Duration) error
	TermWithReason(reason string) error
}

var _ message = jetstream.Msg(nil)

// dispatch handles one delivery on its own goroutine, at most the
// engine's concurrency per rule at once; JetStream calls it sequentially,
// so a full semaphore holds back the next delivery.
func (e *Engine) dispatch(work context.Context, c *consumer, msg message) {
	select {
	case c.sem <- struct{}{}:
	case <-work.Done():
		_ = msg.Nak() // redelivered after the restart either way

		return
	}

	e.mu.Lock()
	if e.stopping {
		e.mu.Unlock()
		<-c.sem

		_ = msg.Nak()

		return
	}

	e.inflight.Add(1)
	e.mu.Unlock()

	go func() {
		defer e.inflight.Done()
		defer func() { <-c.sem }()

		e.handle(work, c.id, c.prog.Load(), msg)
	}()
}

// handle evaluates one delivery and settles it: ack after a start, a
// skip or a duplicate; nak with backoff when the start failed (a plain
// nak when the stop interrupted it); term when the event itself is bad.
func (e *Engine) handle(work context.Context, id uuid.UUID, p *program, msg message) outcome {
	delivered := uint64(1)
	if md, err := msg.Metadata(); err == nil {
		delivered = md.NumDelivered
	}

	h := msg.Headers()
	ctx := otel.GetTextMapPropagator().Extract(work, carrier(h))
	rule := id.String()

	out, meta, err := e.decide(ctx, id, p, msg.Data(), h)
	e.metrics.record(ctx, rule, out)

	log := e.Log().Ctx()
	fields := []xlog.Field{xlog.String("rule", rule), xlog.String("ce_id", meta.ID), xlog.Int64("version", p.version)}

	var settle error

	switch out {
	case outcomeSkipped, outcomeStarted, outcomeDedup:
		if out == outcomeStarted {
			log.Debug(ctx, "rule run started", fields...)
		}

		settle = msg.Ack()
	case outcomeInvalid:
		log.Warn(ctx, "rule event dropped", append(fields, xlog.Err(err))...)

		settle = msg.TermWithReason(truncate(err.Error(), maxReason))
	case outcomeFailed:
		if work.Err() != nil {
			// The stop interrupted the start: not the event's failure.
			settle = msg.Nak()

			break
		}

		log.Warn(ctx, "rule run not started, redelivering",
			append(fields, xlog.Err(err), xlog.Uint64("delivered", delivered))...)

		settle = msg.NakWithDelay(nakDelay(delivered))
	}

	if settle != nil {
		log.Warn(ctx, "rule settle", append(fields, xlog.String("outcome", string(out)), xlog.Err(settle))...)
	}

	return out
}

// decide is what one event means for the rule: evaluate `when` on it and,
// when true, start the run rule/<id>/<ce-id>.
func (e *Engine) decide(
	ctx context.Context, id uuid.UUID, p *program, data []byte, h nats.Header,
) (outcome, bindings.Meta, error) {
	meta, err := ParseMeta(h)
	if err != nil {
		return outcomeInvalid, meta, err
	}

	scope, err := p.prog.StartEvent(data, meta)
	if err != nil {
		return outcomeInvalid, meta, err //nolint:wrapcheck // an EvalError says where
	}

	ok, err := p.prog.Match(scope)
	if err != nil {
		return outcomeInvalid, meta, err //nolint:wrapcheck // an EvalError says where
	}

	if !ok {
		return outcomeSkipped, meta, nil
	}

	starter, timeout := e.starterOf(p.prog)

	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rule := id.String()

	err = starter.StartRule(sctx, rule, p.version, p.prog, data, meta, TraceOf(h), RunID(rule, meta.ID))

	switch {
	case alreadyStarted(err):
		return outcomeDedup, meta, nil
	case err != nil:
		return outcomeFailed, meta, err //nolint:wrapcheck // the starter wraps
	}

	return outcomeStarted, meta, nil
}

// starterOf is the starter of a program's runtime and how long a start is
// given: Temporal's returns once the run is started, the direct one once
// it is over.
func (e *Engine) starterOf(p *bindings.Program) (Starter, time.Duration) { //nolint:ireturn // one of two
	if e.direct != nil && p.Runtime() == consolev1.Runtime_RUNTIME_DIRECT {
		return e.direct.Rules(), e.direct.RunTimeout() + ackSlack
	}

	return e.starter, e.startTimeout
}

// nakDelay after the n-th failed delivery: nakFirst doubling up to nakMax.
func nakDelay(n uint64) time.Duration {
	d := nakFirst
	for i := uint64(1); i < n && d < nakMax; i++ {
		d *= 2
	}

	return min(d, nakMax)
}

// maxReason bounds a term reason: it travels in a header.
const maxReason = 1024

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n]
}
