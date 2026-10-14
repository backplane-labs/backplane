package broker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/backplane-labs/backplane/internal/wire"
)

// Platform defaults of the streams (DefaultStreams). The emitter owns its
// stream's configuration; a subscriber creates a missing stream of another
// service with these defaults and never overwrites an existing one.
const (
	eventsMaxAge = 7 * 24 * time.Hour
	deadMaxAge   = 30 * 24 * time.Hour
	dedupWindow  = 2 * time.Minute

	metaService = wire.MetaService
	metaBy      = wire.MetaBy
	metaKind    = wire.MetaKind
	byEmitter   = wire.ByEmitter
	bySubscribe = wire.BySubscriber
	kindEvents  = wire.KindEvents
	kindDead    = wire.KindDead
)

// ErrNotConnected: NATS is not reachable right now.
var ErrNotConnected = errors.New("broker: nats not connected")

// eventStream is the configuration of service's event stream with limits
// lim; owner records who ensured it last (emitter or subscriber).
func eventStream(service, owner string, lim Streams) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:        StreamName(service),
		Description: "backplane events of " + service,
		Subjects:    []string{StreamSubjects(service)},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		Discard:     jetstream.DiscardOld,
		MaxAge:      lim.MaxAge,
		MaxBytes:    unlimited(lim.MaxBytes),
		Duplicates:  lim.Duplicates,
		Replicas:    lim.Replicas,
		Metadata:    map[string]string{metaService: service, metaBy: owner, metaKind: kindEvents},
	}
}

// deadStream is the configuration of subscriber's dead-letter stream.
func deadStream(subscriber string, lim Streams) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:        DLQStreamName(subscriber),
		Description: "backplane dead letters of " + subscriber,
		Subjects:    []string{DLQStreamSubjects(subscriber)},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		Discard:     jetstream.DiscardOld,
		MaxAge:      lim.DeadMaxAge,
		Duplicates:  window(lim.DeadMaxAge),
		Replicas:    lim.Replicas,
		Metadata:    map[string]string{metaService: subscriber, metaBy: bySubscribe, metaKind: kindDead},
	}
}

// unlimited maps 0 (no limit) to JetStream's -1.
func unlimited(n int64) int64 {
	if n == 0 {
		return -1
	}

	return n
}

// window is the dedup window of a stream kept for maxAge: the server wants
// it no longer than the retention.
func window(maxAge time.Duration) time.Duration {
	if maxAge == 0 {
		return dedupWindow
	}

	return min(dedupWindow, maxAge)
}

// ensureOwn creates or updates the service's own stream: the emitter's
// configuration wins.
func (b *Broker) ensureOwn(ctx context.Context) error {
	jet, err := b.connected()
	if err != nil {
		return err
	}

	if _, err := jet.CreateOrUpdateStream(ctx, eventStream(b.p.Service, byEmitter, b.p.Streams)); err != nil {
		return fmt.Errorf("broker: stream %s: %w", StreamName(b.p.Service), err)
	}

	return nil
}

// ensureDead creates or updates the service's dead-letter stream: the
// service owns it.
func (b *Broker) ensureDead(ctx context.Context, jet jetstream.JetStream) error {
	if _, err := jet.CreateOrUpdateStream(ctx, deadStream(b.p.Service, b.p.Streams)); err != nil {
		return fmt.Errorf("broker: stream %s: %w", DLQStreamName(b.p.Service), err)
	}

	return nil
}

// ensureExists creates a missing stream and leaves an existing one alone.
func ensureExists(ctx context.Context, jet jetstream.JetStream, cfg jetstream.StreamConfig) error {
	_, err := jet.Stream(ctx, cfg.Name)
	if err == nil {
		return nil
	}

	if !errors.Is(err, jetstream.ErrStreamNotFound) {
		return fmt.Errorf("broker: stream %s: %w", cfg.Name, err)
	}

	if _, err := jet.CreateStream(ctx, cfg); err != nil && !errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
		return fmt.Errorf("broker: create stream %s: %w", cfg.Name, err)
	}

	return nil
}
