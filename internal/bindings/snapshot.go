package bindings

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/gopherex/xlog"
)

// notifyChannel is the PostgreSQL channel every replica's save notifies
// (NotifyBindings) and every Manager listens on: a change on one replica
// reaches the others at once, not at the next poll.
const notifyChannel = "backplane_bindings"

// snapshot is the bindings in force at one moment: what Active answers
// from. Rebuilt whole on every change; never modified in place.
type snapshot struct {
	byHook map[string]BindingVersion
	at     time.Time
}

// changed is what every local write ends with: the snapshot rebuilt before
// the caller returns (it reads its own write), the other replicas told, the
// local subscribers signaled.
func (m *Manager) changed(ctx context.Context) {
	if err := m.refresh(ctx); err != nil {
		m.Log().Warn("bindings snapshot not refreshed", xlog.Err(err))
	}

	if err := m.store.Get().Q.NotifyBindings(ctx); err != nil {
		m.Log().Debug("bindings change not notified", xlog.Err(err))
	}

	m.changes.notify()
}

// refresh rebuilds the snapshot from the store. A failure keeps the one
// there is. One at a time: a read begun before a write cannot land after
// the read that write made.
func (m *Manager) refresh(ctx context.Context) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()

	current, err := m.CurrentAll(ctx)
	if err != nil {
		return err
	}

	byHook := make(map[string]BindingVersion, len(current))
	for i := range current {
		byHook[current[i].Hook] = current[i]
	}

	m.snap.Store(&snapshot{byHook: byHook, at: time.Now()})

	return nil
}

// follow keeps the snapshot in step with Changes: the first build at
// start (retried until the store answers), then one after every signal.
func (m *Manager) follow(ctx context.Context) error {
	changes := m.Changes(ctx)

	for m.snap.Load() == nil {
		if err := m.refresh(ctx); err == nil {
			break
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(m.poll):
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-changes:
		}

		if err := m.refresh(ctx); err != nil && ctx.Err() == nil {
			m.Log().Warn("bindings snapshot not refreshed", xlog.Err(err))
		}
	}
}

// listen takes one connection out of the pool for LISTEN and signals
// Changes on every notification; a lost connection is replaced after the
// poll interval (the poll covers the gap).
func (m *Manager) listen(ctx context.Context) error {
	for {
		err := m.listenOnce(ctx)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // the manager stops
		}

		m.Log().Debug("bindings listener stopped, restarting", xlog.Err(err))

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(m.poll):
		}
	}
}

func (m *Manager) listenOnce(ctx context.Context) error {
	pooled, err := m.store.Get().Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire: %w", err)
	}

	// A connection that listens does not go back to the pool.
	conn := pooled.Hijack()

	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()

		_ = conn.Close(closeCtx)
	}()

	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}

			return fmt.Errorf("wait: %w", err)
		}

		m.changes.notify()
	}
}

// observe registers the snapshot's age: how long since it was last built.
// A growing age with saves going on means the store is unreachable and
// Active answers from the past.
func (m *Manager) observe(meter metric.Meter) {
	if meter == nil {
		return
	}

	age, err := meter.Float64ObservableGauge("backplane.bindings.snapshot_age", metric.WithUnit("s"),
		metric.WithDescription("seconds since the bindings in force were last read from the store"))
	if err != nil {
		return
	}

	_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		if s := m.snap.Load(); s != nil {
			o.ObserveFloat64(age, time.Since(s.at).Seconds())
		}

		return nil
	}, age)
}
