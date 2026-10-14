package ops

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/wire"
)

// ListDeadLetters implements EventService.
func (a EventAPI) ListDeadLetters(
	ctx context.Context, req *consolev1.ListDeadLettersRequest,
) (*consolev1.ListDeadLettersResponse, error) {
	if !serviceName.MatchString(req.GetSubscriber()) {
		return nil, a.o.status(ctx, fmt.Errorf("%w: subscriber is required", ErrInput))
	}

	jet, err := a.o.nats()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	st, s, err := streamState(ctx, jet, wire.DLQStreamName(req.GetSubscriber()))
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	out := &consolev1.ListDeadLettersResponse{Stream: st}
	if s == nil {
		return out, nil
	}

	counts, err := deadCounts(ctx, jet, req.GetSubscriber())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	out.Counts = countsPB(counts)

	filter := wire.DLQStreamSubjects(req.GetSubscriber())
	if req.GetConsumer() != "" {
		filter = wire.DLQSubject(req.GetSubscriber(), req.GetConsumer())
	}

	msgs, next, err := forward(ctx, s, filter, max(req.GetStartSeq(), st.GetFirstSeq()), 0, pageSize(req.GetLimit()))
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	out.NextSeq = next

	for _, m := range msgs {
		out.DeadLetters = append(out.DeadLetters, deadLetterPB(m))
	}

	return out, nil
}

// reactor is a declared reactor: its subscriber, consumer and event.
type reactor struct {
	subscriber, consumer, source, event string
}

func (r reactor) durable() string { return wire.Durable(r.subscriber, r.consumer) }

func (r reactor) dead() string { return wire.DLQSubject(r.subscriber, r.consumer) }

func (r reactor) redrive() string { return wire.RedriveSubject(r.source, r.subscriber, r.consumer) }

// reactorOf is subscriber's reactor consumer as its latest manifest
// declares it.
func (o *Ops) reactorOf(subscriber, consumer string) (reactor, error) {
	if !serviceName.MatchString(subscriber) || consumer == "" {
		return reactor{}, fmt.Errorf("%w: subscriber and consumer are required", ErrInput)
	}

	m, err := o.latest(subscriber)
	if err != nil {
		return reactor{}, err
	}

	for _, sub := range m.GetSubscriptions() {
		if sub.GetConsumer() != consumer {
			continue
		}

		source, _, err := wire.SplitEvent(sub.GetEvent())
		if err != nil {
			return reactor{}, fmt.Errorf("%w: subscription %q: %w", ErrPrecondition, consumer, err)
		}

		return reactor{subscriber: subscriber, consumer: consumer, source: source, event: sub.GetEvent()}, nil
	}

	return reactor{}, fmt.Errorf("%w: %s has no reactor %q", ErrNotDeclared, subscriber, consumer)
}

// RedriveDeadLetters implements EventService. Each dead letter is
// published back to its reactor alone — to the reactor's redrive subject
// bp.<source>._redrive.<subscriber>.<consumer> in the source stream, which
// the reactor's consumer filters on next to its event (§8) — and then
// removed from the dead-letter stream. The reactor handles it as a new
// delivery (attempt 1, its full MaxDeliver again; ce-id unchanged, so an
// idempotent handler recognizes it); failing again, it becomes a new dead
// letter. Nats-Msg-Id redrive:<durable>:<seq> keeps a retried redrive of
// the same dead letter single within the dedup window.
func (a EventAPI) RedriveDeadLetters(
	ctx context.Context, req *consolev1.RedriveDeadLettersRequest,
) (*consolev1.RedriveDeadLettersResponse, error) {
	if len(req.GetSeqs()) == 0 && !req.GetAll() {
		return nil, a.o.status(ctx, fmt.Errorf("%w: seqs or all is required", ErrInput))
	}

	re, err := a.o.reactorOf(req.GetSubscriber(), req.GetConsumer())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	jet, err := a.o.nats()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	if err := redrivable(ctx, jet, re); err != nil {
		return nil, a.o.status(ctx, err)
	}

	msgs, failed, err := a.deadLetters(ctx, jet, re.subscriber, re.dead(), req.GetSeqs())
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	out := &consolev1.RedriveDeadLettersResponse{Subject: re.redrive(), Failed: failed}

	s, err := jet.Stream(ctx, wire.DLQStreamName(re.subscriber))
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	for _, m := range msgs {
		if _, err := jet.PublishMsg(ctx, redriveMsg(re, m)); err != nil {
			out.Failed = append(out.Failed, &consolev1.SeqError{Seq: m.Sequence, Error: err.Error()})

			continue
		}

		out.Redriven++

		if err := s.DeleteMsg(ctx, m.Sequence); err != nil && !errors.Is(err, jetstream.ErrMsgNotFound) {
			out.Failed = append(out.Failed, &consolev1.SeqError{
				Seq: m.Sequence, Error: "redriven, but not removed from the dead letters: " + err.Error(),
			})
		}
	}

	a.o.audit(ctx, "deadletters.redrive", re.subscriber+"/"+re.consumer,
		xlog.Uint64("redriven", out.GetRedriven()), xlog.Int("failed", len(out.GetFailed())), xlog.Bool("all", req.GetAll()))

	return out, nil
}

// redrivable: the reactor's consumer is on the server and filters on its
// redrive subject (an SDK that knows redrive created it).
func redrivable(ctx context.Context, jet jetstream.JetStream, re reactor) error {
	c, err := jet.Consumer(ctx, wire.StreamName(re.source), re.durable())
	if errors.Is(err, jetstream.ErrConsumerNotFound) || errors.Is(err, jetstream.ErrStreamNotFound) {
		return fmt.Errorf("%w: consumer %s of reactor %s/%s is not on the server: start %s first",
			ErrPrecondition, re.durable(), re.subscriber, re.consumer, re.subscriber)
	}

	if err != nil {
		return fmt.Errorf("consumer %s: %w", re.durable(), err)
	}

	for _, f := range filters(c.CachedInfo().Config) {
		if f == re.redrive() {
			return nil
		}
	}

	return fmt.Errorf("%w: consumer %s does not take redrives (its filters lack %s: %s runs an SDK "+
		"without console redrive; use event.Redrive in the service)",
		ErrPrecondition, re.durable(), re.redrive(), re.subscriber)
}

// redriveMsg is dead letter m published back to its reactor: the original
// headers and payload without the dead letter's own headers.
func redriveMsg(re reactor, m *jetstream.RawStreamMsg) *nats.Msg {
	h := nats.Header{}

	for k, vs := range m.Header {
		if strings.HasPrefix(k, "Nats-") || strings.EqualFold(k, wire.HeaderError) ||
			strings.EqualFold(k, wire.HeaderConsumer) || strings.EqualFold(k, wire.HeaderDelivered) {
			continue
		}

		h[k] = append([]string(nil), vs...)
	}

	seq := strconv.FormatUint(m.Sequence, 10)
	h.Set(wire.HeaderRedriven, seq)
	h.Set(jetstream.MsgIDHeader, "redrive:"+re.durable()+":"+seq)

	return &nats.Msg{Subject: re.redrive(), Header: h, Data: m.Data}
}

// deadLetters are the dead letters of subject in subscriber's dead-letter
// stream: the given sequences (one of another subject fails), or all of
// them present now when seqs is empty.
func (a EventAPI) deadLetters(
	ctx context.Context, jet jetstream.JetStream, subscriber, subject string, seqs []uint64,
) ([]*jetstream.RawStreamMsg, []*consolev1.SeqError, error) {
	s, err := jet.Stream(ctx, wire.DLQStreamName(subscriber))
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return nil, nil, nil
	}

	if err != nil {
		return nil, nil, fmt.Errorf("stream %s: %w", wire.DLQStreamName(subscriber), err)
	}

	if len(seqs) == 0 {
		info, err := s.Info(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("stream %s: %w", wire.DLQStreamName(subscriber), err)
		}

		limit := int(min(info.State.Msgs, maxBatch)) //nolint:gosec // bounded by maxBatch
		msgs, _, err := forward(ctx, s, subject, info.State.FirstSeq, info.State.LastSeq, limit)

		return msgs, nil, err
	}

	var (
		msgs   []*jetstream.RawStreamMsg
		failed []*consolev1.SeqError
	)

	for _, seq := range seqs {
		m, err := s.GetMsg(ctx, seq)

		switch {
		case err != nil:
			failed = append(failed, &consolev1.SeqError{Seq: seq, Error: err.Error()})
		case m.Subject != subject:
			failed = append(failed, &consolev1.SeqError{Seq: seq, Error: "not a dead letter of this reactor: " + m.Subject})
		default:
			msgs = append(msgs, m)
		}
	}

	return msgs, failed, nil
}

// maxBatch bounds the dead letters one redrive with all handles; the rest
// wait for the next call.
const maxBatch = 10000

// PurgeDeadLetters implements EventService.
func (a EventAPI) PurgeDeadLetters(
	ctx context.Context, req *consolev1.PurgeDeadLettersRequest,
) (*consolev1.PurgeDeadLettersResponse, error) {
	subscriber := req.GetSubscriber()
	if !serviceName.MatchString(subscriber) {
		return nil, a.o.status(ctx, fmt.Errorf("%w: subscriber is required", ErrInput))
	}

	if len(req.GetSeqs()) > 0 && req.GetConsumer() == "" {
		return nil, a.o.status(ctx, fmt.Errorf("%w: seqs need their consumer", ErrInput))
	}

	jet, err := a.o.nats()
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	out, err := a.purge(ctx, jet, req)
	if err != nil {
		return nil, a.o.status(ctx, err)
	}

	a.o.audit(ctx, "deadletters.purge", subscriber+"/"+req.GetConsumer(),
		xlog.Uint64("purged", out.GetPurged()), xlog.Int("seqs", len(req.GetSeqs())))

	return out, nil
}

func (a EventAPI) purge(
	ctx context.Context, jet jetstream.JetStream, req *consolev1.PurgeDeadLettersRequest,
) (*consolev1.PurgeDeadLettersResponse, error) {
	name := wire.DLQStreamName(req.GetSubscriber())

	s, err := jet.Stream(ctx, name)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return &consolev1.PurgeDeadLettersResponse{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("stream %s: %w", name, err)
	}

	out := &consolev1.PurgeDeadLettersResponse{}

	if len(req.GetSeqs()) > 0 {
		msgs, failed, err := a.deadLetters(ctx, jet, req.GetSubscriber(),
			wire.DLQSubject(req.GetSubscriber(), req.GetConsumer()), req.GetSeqs())
		if err != nil {
			return nil, err
		}

		out.Failed = failed

		for _, m := range msgs {
			if err := s.DeleteMsg(ctx, m.Sequence); err != nil {
				out.Failed = append(out.Failed, &consolev1.SeqError{Seq: m.Sequence, Error: err.Error()})

				continue
			}

			out.Purged++
		}

		return out, nil
	}

	filter := wire.DLQStreamSubjects(req.GetSubscriber())
	if req.GetConsumer() != "" {
		filter = wire.DLQSubject(req.GetSubscriber(), req.GetConsumer())
	}

	counts, err := subjectCounts(ctx, s, filter)
	if err != nil {
		return nil, err
	}

	for _, n := range counts {
		out.Purged += n
	}

	if err := s.Purge(ctx, jetstream.WithPurgeSubject(filter)); err != nil {
		return nil, fmt.Errorf("purge %s: %w", filter, err)
	}

	return out, nil
}
