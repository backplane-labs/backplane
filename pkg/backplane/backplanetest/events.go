package backplanetest

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/backplane-labs/backplane/pkg/backplane/event"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
)

// Published is one recorded event with what Publish set on it.
type Published[T any] struct {
	Value T
	// Key is the event.Key it was published with ("" without).
	Key string
	// ID is event.ID, else the new UUID the transport would assign.
	ID string
	// Time is event.Time, else the publish time.
	Time time.Time
	// Headers are the event.Header values by name.
	Headers map[string]string
}

// Events decodes what was published to ref so far.
func Events[T any](h *Harness, ref event.Ref[T]) []T {
	h.t.Helper()

	meta := EventsWithMeta(h, ref)

	out := make([]T, 0, len(meta))
	for _, p := range meta {
		out = append(out, p.Value)
	}

	return out
}

// EventsWithMeta is Events with each event's key, id, time and headers.
func EventsWithMeta[T any](h *Harness, ref event.Ref[T]) []Published[T] {
	h.t.Helper()

	msgs := h.rec.events(ref.Name())
	out := make([]Published[T], 0, len(msgs))

	for _, m := range msgs {
		var v T
		if err := decl.Decode(m.Payload, &v); err != nil {
			h.t.Fatalf("backplanetest: decode %s: %v", ref.Name(), err)
		}

		out = append(out, Published[T]{Value: v, Key: m.Key, ID: m.ID, Time: m.Time, Headers: maps.Clone(m.Extensions)})
	}

	return out
}

// FailPublish makes every Publish fail with err (wrapped as the transport
// wraps its errors) until FailPublish(h, nil); nothing is recorded
// meanwhile.
func FailPublish(h *Harness, err error) {
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()

	h.rec.publishErr = err
}

// Unavailable takes both transports away, as while NATS and Temporal are
// down: Publish fails with event.ErrUnavailable, Call with
// hook.ErrUnavailable (in Workflows, a workflows.CallHook finds no binding),
// until Available.
func Unavailable(h *Harness) { h.rec.setUnavailable(true) }

// Available undoes Unavailable.
func Available(h *Harness) { h.rec.setUnavailable(false) }

// DeliveryOption sets what event.DeliveryOf reports to a reactor run by
// React or ReactConsumer.
type DeliveryOption func(in *env.Incoming)

// WithKey is the event's Key (Delivery.Subject).
func WithKey(k string) DeliveryOption { return func(in *env.Incoming) { in.Subject = k } }

// WithEvent is the event's full name ("<service>.<Event>"), its source
// with it: what a raw reactor (event.ReactRaw, run with ReactConsumer)
// sees for one event of its pattern.
func WithEvent(full string) DeliveryOption {
	return func(in *env.Incoming) {
		in.Type = full
		in.Source, _, _ = strings.Cut(full, ".")
		in.Extensions["instance"] = in.Source + "-1"
	}
}

// WithID is the event's id (Delivery.ID), by default a new UUID: deliver
// the same id twice to test idempotency.
func WithID(id string) DeliveryOption { return func(in *env.Incoming) { in.ID = id } }

// WithAttempt is the delivery's attempt (Delivery.Attempt), 1 by default.
func WithAttempt(n int) DeliveryOption { return func(in *env.Incoming) { in.Attempt = n } }

// WithHeader adds an extension (Delivery.Extensions) as event.Header sets it.
func WithHeader(name, value string) DeliveryOption {
	return func(in *env.Incoming) { in.Extensions[name] = value }
}

// React delivers v to the reactor declared for the event name (full,
// "iam.UserRegistered") on the scope at path ("" for the root), as the
// broker would: once, with the reactor's event.Timeout and a full
// event.Delivery on ctx (a new ID, Attempt 1, the reactor's Consumer,
// extensions instance and version; opts change them). A reactor pinned
// with event.Consumer is found by its event when it is the only pinned
// reactor of it; else use ReactConsumer. The error is the handler's;
// nothing is redelivered or dead-lettered.
func React[T any](ctx context.Context, h *Harness, path, name string, v T, opts ...DeliveryOption) error {
	consumer := name
	if path != "" {
		consumer = path + ":" + name
	}

	r, ok := h.env.ReactorOf(consumer)
	if !ok {
		r, ok = pinned(h.env, name)
	}

	if !ok {
		return fmt.Errorf("%w: reactor %q", ErrNotDeclared, consumer)
	}

	return deliver(ctx, r, v, opts)
}

// ReactConsumer delivers v to the reactor whose consumer is consumer (as
// the manifest's subscriptions list it, pinned or derived), as React does.
func ReactConsumer[T any](ctx context.Context, h *Harness, consumer string, v T, opts ...DeliveryOption) error {
	r, ok := h.env.ReactorOf(consumer)
	if !ok {
		return fmt.Errorf("%w: reactor %q", ErrNotDeclared, consumer)
	}

	return deliver(ctx, r, v, opts)
}

// pinned is the only reactor of event whose consumer was pinned: its name
// is not the one React derives (the event, or <path>:<event>).
func pinned(e *env.Env, name string) (env.Reactor, bool) {
	var (
		found env.Reactor
		n     int
	)

	for _, r := range e.Reactors() {
		if r.Event == name && r.Consumer != name && !strings.HasSuffix(r.Consumer, ":"+name) {
			found = r
			n++
		}
	}

	return found, n == 1
}

func deliver[T any](ctx context.Context, r env.Reactor, v T, opts []DeliveryOption) error {
	source, _, _ := strings.Cut(r.Event, ".")
	in := env.Incoming{
		ID: uuid.NewString(), Source: source, Type: r.Event, Time: time.Now(), Attempt: 1, Consumer: r.Consumer,
		Extensions: map[string]string{"instance": source + "-1", "version": "0.0.0"},
	}

	for _, o := range opts {
		o(&in)
	}

	ctx = env.WithIncoming(ctx, in)

	if r.Delivery.Timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, r.Delivery.Timeout)
		defer cancel()
	}

	_, err := invoke[struct{}](ctx, r.Handler, v)

	return err
}

// recorder is both transports of the harness.
type recorder struct {
	mu          sync.Mutex
	published   map[string][]env.Message
	hooks       map[string]env.Handler
	publishErr  error
	unavailable bool
}

func newRecorder() *recorder {
	return &recorder{published: map[string][]env.Message{}, hooks: map[string]env.Handler{}}
}

func (r *recorder) Publish(_ context.Context, m env.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch {
	case r.unavailable:
		return env.ErrUnavailable
	case r.publishErr != nil:
		return r.publishErr
	}

	if m.ID == "" {
		m.ID = uuid.NewString()
	}

	if m.Time.IsZero() {
		m.Time = time.Now()
	}

	m.Extensions = maps.Clone(m.Extensions)
	r.published[m.Event] = append(r.published[m.Event], m)

	return nil
}

func (r *recorder) Call(ctx context.Context, name string, in []byte) ([]byte, error) {
	r.mu.Lock()
	fn, ok := r.hooks[name]
	down := r.unavailable
	r.mu.Unlock()

	if !ok || down {
		return nil, env.ErrUnavailable
	}

	return fn(ctx, in)
}

func (r *recorder) setUnavailable(down bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.unavailable = down
}

func (r *recorder) events(name string) []env.Message {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]env.Message(nil), r.published[name]...)
}

func (r *recorder) answer(name string, fn env.Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.hooks[name] = fn
}
