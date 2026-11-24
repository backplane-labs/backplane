package broker_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/infra/nats/natstest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/broker"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

func TestDeadLetterWaitsForPublishAck(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		cause error
		calls int32
	}{
		{"exhausted", errBoom, 2},
		{"terminal", env.NonRetryableError{Err: errBoom}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			url := natstest.Start(t).ClientURL()
			src, sub := unique("src"), unique("sub")
			jet := admin(t, url, src, sub)
			ctx := t.Context()

			const maxDeliver = 2

			var calls atomic.Int32

			e, consumer := reacting(sub, src, env.Delivery{
				MaxDeliver: maxDeliver, Redelivery: backoff.Policy{Min: 25 * time.Millisecond, Max: 50 * time.Millisecond},
			}, func(context.Context, []byte) ([]byte, error) {
				calls.Add(1)

				return nil, tc.cause
			})
			pub := open(t, url, src, emitter(t, src))
			durable := broker.Durable(sub, consumer)
			startReactors(t, jet, open(t, url, sub, e), src, durable)

			dlq, err := jet.Stream(ctx, broker.DLQStreamName(sub))
			if err != nil {
				t.Fatal(err)
			}

			cfg := dlq.CachedInfo().Config
			unavailable := cfg
			unavailable.MaxMsgSize = 1 // JetStream rejects every actual dead-letter message.

			if _, err := jet.UpdateStream(ctx, unavailable); err != nil {
				t.Fatal(err)
			}

			const payload = `{"n":2}`
			if err := pub.PublishRaw(ctx, src+".Greeted", "", []byte(payload)); err != nil {
				t.Fatal(err)
			}

			cons, err := jet.Consumer(ctx, broker.StreamName(src), durable)
			if err != nil {
				t.Fatal(err)
			}

			eventually(t, "source survives past its former server delivery limit", func() bool {
				info, err := cons.Info(ctx)
				if err != nil {
					return false
				}

				if info.AckFloor.Stream != 0 {
					t.Fatal("source message was settled without a DLQ publish ack")
				}

				return info.NumAckPending == 1 && info.Delivered.Consumer >= 9
			})

			if n := calls.Load(); n != tc.calls {
				t.Fatalf("handler ran %d times while the DLQ publish retried; want %d", n, tc.calls)
			}

			if _, err := jet.UpdateStream(ctx, cfg); err != nil {
				t.Fatal(err)
			}

			m := deadLetter(t, jet, sub, consumer)
			if string(m.Data) != payload || m.Header.Get("bp-error") != "boom" {
				t.Fatalf("dead letter: payload=%s headers=%v", m.Data, m.Header)
			}

			eventually(t, "source is settled after the DLQ accepts the message", func() bool {
				info, err := cons.Info(ctx)

				return err == nil && info.NumAckPending == 0 && info.AckFloor.Stream == 1
			})

			info, err := dlq.Info(ctx)
			if err != nil || info.State.Msgs != 1 || calls.Load() != tc.calls {
				t.Fatalf("want one dead letter and %d handler calls; got %+v, %d, %v", tc.calls, info, calls.Load(), err)
			}
		})
	}
}
