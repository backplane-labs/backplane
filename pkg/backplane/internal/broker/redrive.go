package broker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
)

// Errors of Redrive.
var (
	// ErrNoReactor: the service declares no reactor with that consumer.
	ErrNoReactor = errors.New("broker: no such reactor")
	// ErrRedrive: some dead letters failed again and stay dead letters.
	ErrRedrive = errors.New("broker: dead letters failed again")
)

// Redrive runs the handler of the reactor named consumer on its dead
// letters, oldest first, in this process, and deletes each one it handled.
// A dead letter that fails again stays where it is. Dead letters that
// arrive meanwhile are left for the next call. It returns how many were
// handled; ErrRedrive (with the first failure) when some failed.
//
// Only this reactor sees the events again: they are not published to
// their source stream, where every subscriber would get them.
func (b *Broker) Redrive(ctx context.Context, consumer string) (int, error) {
	if b.p.Env == nil {
		return 0, fmt.Errorf("%w: %q", ErrNoReactor, consumer)
	}

	r, ok := b.p.Env.ReactorOf(consumer)
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrNoReactor, consumer)
	}

	re, err := b.reactorOf(r)
	if err != nil {
		return 0, err
	}

	jet, err := b.connected()
	if err != nil {
		return 0, err
	}

	s, err := jet.Stream(ctx, DLQStreamName(b.p.Service))
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return 0, nil
	}

	if err != nil {
		return 0, fmt.Errorf("broker: redrive %s: %w", consumer, err)
	}

	info, err := s.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("broker: redrive %s: %w", consumer, err)
	}

	return b.redrive(ctx, s, re, info.State.FirstSeq, info.State.LastSeq)
}

// redrive handles the dead letters of re in [from, last].
func (b *Broker) redrive(ctx context.Context, s jetstream.Stream, re reactor, from, last uint64) (int, error) {
	var (
		handled, failed int
		first           error
	)

	for seq := max(from, 1); seq <= last; {
		m, err := s.GetMsg(ctx, seq, jetstream.WithGetMsgSubject(re.dead))
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			break
		}

		if err != nil {
			return handled, fmt.Errorf("broker: redrive %s: %w", re.consumer, err)
		}

		if m.Sequence > last {
			break
		}

		seq = m.Sequence + 1

		delivered, _ := strconv.ParseUint(m.Header.Get(HeaderDelivered), 10, 64)
		hctx := otel.GetTextMapPropagator().Extract(ctx, carrier(m.Header))
		start := time.Now()

		if err := b.invoke(hctx, re, m.Subject, m.Data, m.Header, delivered+1); err != nil {
			metrics.ReactorHandled(hctx, re.consumer, metrics.Dead, time.Since(start))
			b.log.Ctx().Warn(hctx, "redrive: dead letter failed again",
				re.fields(xlog.Err(err), xlog.Uint64("dead_seq", m.Sequence))...)

			failed++

			if first == nil {
				first = err
			}

			if ctx.Err() != nil {
				break
			}

			continue
		}

		metrics.ReactorHandled(hctx, re.consumer, metrics.Ack, time.Since(start))

		if err := s.DeleteMsg(ctx, m.Sequence); err != nil && !errors.Is(err, jetstream.ErrMsgNotFound) {
			return handled, fmt.Errorf("broker: redrive %s: delete %d: %w", re.consumer, m.Sequence, err)
		}

		handled++
	}

	if failed > 0 {
		return handled, fmt.Errorf("%w: %d of %d: %w", ErrRedrive, failed, handled+failed, first)
	}

	return handled, ctx.Err() //nolint:wrapcheck // the caller's own ctx
}
