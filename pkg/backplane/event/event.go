// Package event declares the events a service publishes and the reactors it
// runs. Events travel over the service's transport (NATS JetStream with
// CloudEvents metadata, design §8); without one Publish returns
// ErrUnavailable. Reactors are recorded in the manifest as durable
// consumers.
//
// # Payload compatibility
//
// A payload is JSON (protojson for proto messages) and evolves additively:
// a field is added, never repurposed. A reader ignores fields it does not
// know, so a producer may ship a new field before its consumers; a reader
// gets the zero value for a field the payload lacks. Renaming a field is
// adding a new one: publish both until every reactor reads the new one.
// A payload a reactor cannot decode at all is dead-lettered at once.
//
// # Delivery
//
// A reactor is at-least-once: a handler may see the same event again
// (Delivery.ID tells), so it is idempotent. Instances of the service share
// the reactor's durable consumer as competing consumers: each event goes
// to one instance. With Concurrency above 1, or with several instances,
// events of one key are not handled in order; Ordered makes a reactor
// strict at the cost of one event in flight across the whole service.
package event

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"time"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/env"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/link"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
)

// Errors.
var (
	// ErrUnavailable is returned while no transport carries events.
	ErrUnavailable = env.ErrUnavailable
	// ErrOption: a publish option has an invalid value (Publish fails).
	ErrOption = errors.New("event: invalid publish option")
)

var (
	eventName = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	fullName  = regexp.MustCompile(`^[a-z0-9-]+\.[A-Z][A-Za-z0-9]*$`)
	extName   = regexp.MustCompile(`^[a-z0-9]+$`)
)

// reserved are the attributes the SDK sets itself; Header cannot.
//
//nolint:gochecknoglobals // constant set
var reserved = map[string]bool{
	"specversion": true, "id": true, "source": true, "type": true, "time": true, "subject": true,
	"datacontenttype": true, "dataschema": true, "instance": true, "version": true,
}

// Ref is a declared event. The zero Ref is undeclared: Publish fails.
type Ref[T any] struct {
	env  *env.Env
	name string
}

// Option interfaces, one per function.
type (
	// DeclareOption configures Declare.
	DeclareOption interface{ applyDeclare(ev *backplanev1.Event) }
	// PublishOption configures Publish.
	PublishOption interface{ applyPublish(p *publishSettings) }
	// ReactOption configures React.
	ReactOption interface{ applyReact(r *reactSettings) }
)

type publishSettings struct {
	msg env.Message
	err error
}

type reactSettings struct {
	consumer string
	delivery env.Delivery
}

// Declare registers an event the service publishes. Declare it where it is
// raised: any component can. name is CamelCase ([A-Z][A-Za-z0-9]*); on the
// wire it is "<service>.<name>". It panics on an invalid name.
func Declare[T any](scope deps.Scope, name string, opts ...DeclareOption) Ref[T] {
	if !eventName.MatchString(name) {
		panic(fmt.Sprintf("event: name %q must be CamelCase ([A-Z][A-Za-z0-9]*)", name))
	}

	e, _ := decl.Env(scope, "event "+name)

	ev := &backplanev1.Event{Name: name, Schema: decl.Schema[T](scope, e.Service, "event_"+name)}
	for _, o := range opts {
		o.applyDeclare(ev)
	}

	e.Manifest.Event(ev)

	return Ref[T]{env: e, name: name}
}

type describe string

func (d describe) applyDeclare(ev *backplanev1.Event) { ev.Description = string(d) }

// Describe is the event's description in the manifest and the console.
func Describe(s string) DeclareOption { return describe(s) }

// Name is the full event name <service>.<Name>.
func (r Ref[T]) Name() string {
	if r.env == nil {
		return ""
	}

	return r.env.Service + "." + r.name
}

// Publish sends the event and returns once the transport stored it. It
// waits no longer than ctx allows; a ctx without a deadline gets the
// transport's own (nats.publish_timeout, 5s). An invalid option fails
// with ErrOption.
func (r Ref[T]) Publish(ctx context.Context, v T, opts ...PublishOption) error {
	if r.env == nil {
		return fmt.Errorf("event: publish on an undeclared Ref: %w", ErrUnavailable)
	}

	err := r.publish(ctx, v, opts)
	metrics.EventPublished(ctx, r.Name(), outcome(err))

	return err
}

func (r Ref[T]) publish(ctx context.Context, v T, opts []PublishOption) error {
	p := publishSettings{msg: env.Message{Event: r.Name()}}
	for _, o := range opts {
		o.applyPublish(&p)
	}

	if p.err != nil {
		return fmt.Errorf("event %s: %w", r.Name(), p.err)
	}

	t := r.env.Broker()
	if t == nil {
		return fmt.Errorf("event %s: %w", r.Name(), ErrUnavailable)
	}

	payload, err := decl.Encode(v)
	if err != nil {
		return fmt.Errorf("event %s: encode: %w", r.Name(), err)
	}

	p.msg.Payload = payload

	if err := t.Publish(ctx, p.msg); err != nil {
		return fmt.Errorf("event %s: %w", r.Name(), err)
	}

	return nil
}

// outcome of a publish for backplane.event.published.
func outcome(err error) string {
	switch {
	case err == nil:
		return metrics.OK
	case errors.Is(err, ErrUnavailable):
		return metrics.Unavailable
	case errors.Is(err, context.DeadlineExceeded):
		return metrics.Timeout
	default:
		return metrics.Error
	}
}

type key string

func (k key) applyPublish(p *publishSettings) { p.msg.Key = string(k) }

// Key sets the CloudEvents subject (ordering key).
func Key(k string) PublishOption { return key(k) }

type id string

func (i id) applyPublish(p *publishSettings) {
	if i == "" {
		p.err = errors.Join(p.err, fmt.Errorf("%w: empty ID", ErrOption))

		return
	}

	p.msg.ID = string(i)
}

// ID sets the event's id (ce-id), by default a new UUID. JetStream drops a
// second publish with the same id within the stream's dedup window
// (nats.dedup_window), so an outbox that republishes a row under the row's
// id publishes it once. It must not be empty.
func ID(s string) PublishOption { return id(s) }

type at time.Time

func (t at) applyPublish(p *publishSettings) { p.msg.Time = time.Time(t) }

// Time sets when the event happened (ce-time), by default the publish
// time; the zero time keeps the default.
func Time(t time.Time) PublishOption { return at(t) }

type header struct{ name, value string }

func (h header) applyPublish(p *publishSettings) {
	switch {
	case !extName.MatchString(h.name):
		p.err = errors.Join(p.err, fmt.Errorf("%w: header %q: name must be lowercase letters and digits", ErrOption, h.name))
	case reserved[h.name]:
		p.err = errors.Join(p.err, fmt.Errorf("%w: header %q is set by the SDK", ErrOption, h.name))
	case !validValue(h.value):
		p.err = errors.Join(p.err, fmt.Errorf("%w: header %q: value has a line break", ErrOption, h.name))
	default:
		if p.msg.Extensions == nil {
			p.msg.Extensions = map[string]string{}
		}

		p.msg.Extensions[h.name] = h.value
	}
}

// Header sets a CloudEvents extension attribute, sent as header ce-<name>
// and read back from Delivery.Extensions. name is lowercase letters and
// digits (CloudEvents); the SDK's own attributes (id, source, type, time,
// subject, instance, version, ...) are not accepted, nor a value with a
// line break.
func Header(name, value string) PublishOption { return header{name: name, value: value} }

func validValue(v string) bool {
	for i := range len(v) {
		if v[i] == '\r' || v[i] == '\n' {
			return false
		}
	}

	return true
}

// Terminal marks a handler's error as final: the message is dead-lettered
// at once instead of being delivered again. Use it for input no retry can
// fix. Terminal(nil) is nil.
func Terminal(err error) error {
	if err == nil {
		return nil
	}

	return env.NonRetryableError{Err: err}
}

// Delivery is the metadata of the event a reactor's handler runs for.
type Delivery struct {
	ID       string    // ce-id: the same on every redelivery
	Source   string    // service that published it
	Type     string    // full event name "<service>.<Event>"
	Subject  string    // the Key it was published with
	Time     time.Time // when it happened (ce-time)
	Attempt  int       // delivery of this message, 1 for the first
	Consumer string    // the reactor's consumer (manifest subscriptions)
	// Extensions are the other ce-* headers by name without the prefix:
	// Header values and the SDK's instance and version.
	Extensions map[string]string
}

// DeliveryOf returns the metadata of the event being handled; false
// outside a reactor's handler.
func DeliveryOf(ctx context.Context) (Delivery, bool) {
	in, ok := env.IncomingOf(ctx)
	if !ok {
		return Delivery{}, false
	}

	return Delivery{
		ID: in.ID, Source: in.Source, Type: in.Type, Subject: in.Subject, Time: in.Time,
		Attempt: in.Attempt, Consumer: in.Consumer, Extensions: maps.Clone(in.Extensions),
	}, true
}

// React subscribes fn to another service's event by full name
// ("iam.UserRegistered": a service name, a dot, a CamelCase event); it
// panics on another shape. The durable consumer is named after the scope's
// node path and the event ("<path>:<event>", the event alone on the Root),
// so each reactor keeps its own position. Renaming or moving the component
// renames the consumer: the new one starts afresh (StartAt) and the old one
// stays on the server with its position. Consumer pins the name instead.
//
// A handler that returns an error (or panics) gets the message again after
// a growing delay; when the last delivery fails too, or the error is
// Terminal, the message goes to the service's dead letters; a payload that
// does not decode is Terminal. A handler cancelled by the service's stop
// does not count as failed: the message comes back, never dead-lettered.
// The handler's ctx carries the event's Delivery. Options change the
// defaults: MaxDeliver 5, Concurrency 4, Timeout 30s, Redelivery 1s..1m,
// StartAt(StartNew). They panic on values that make no sense, at
// declaration.
func React[T any](scope deps.Scope, event string, fn func(ctx context.Context, v T) error, opts ...ReactOption) {
	if !fullName.MatchString(event) {
		panic(fmt.Sprintf("event: react to %q: want <service>.<Event> ([a-z0-9-]+ dot [A-Z][A-Za-z0-9]*)", event))
	}

	e, n := decl.Env(scope, "reactor of "+event)

	var s reactSettings
	for _, o := range opts {
		o.applyReact(&s)
	}

	if s.delivery.Ordered && s.delivery.Concurrency > 1 {
		panic(fmt.Sprintf("event: react to %q: Ordered runs one handler at a time, Concurrency(%d) contradicts it",
			event, s.delivery.Concurrency))
	}

	consumer := s.consumer
	if consumer == "" {
		consumer = event
		if n.Path() != "" {
			consumer = n.Path() + ":" + event
		}
	}

	e.Manifest.Subscription(&backplanev1.Subscription{Event: event, Consumer: consumer})

	handler := func(ctx context.Context, in []byte) ([]byte, error) {
		var v T
		if err := decl.Decode(in, &v); err != nil {
			return nil, Terminal(fmt.Errorf("reactor %s: decode: %w", consumer, err))
		}

		return nil, fn(ctx, v)
	}

	e.Reactor(env.Reactor{Event: event, Consumer: consumer, Handler: handler, Delivery: s.delivery})
}

type consumerName string

func (c consumerName) applyReact(r *reactSettings) { r.consumer = string(c) }

// Consumer pins the reactor's consumer name, unique within the service,
// so that renaming or moving the component keeps the consumer and its
// position. To adopt an existing consumer, pin its current name. It panics
// on an empty name.
func Consumer(name string) ReactOption {
	if name == "" {
		panic("event: Consumer name must not be empty")
	}

	return consumerName(name)
}

type maxDeliver int

func (m maxDeliver) applyReact(r *reactSettings) { r.delivery.MaxDeliver = int(m) }

// MaxDeliver bounds the deliveries of one message, the first included;
// after the n-th failure it is dead-lettered. MaxDeliver(1) never retries.
// It panics unless n >= 1.
func MaxDeliver(n int) ReactOption {
	if n < 1 {
		panic(fmt.Sprintf("event: MaxDeliver must be >= 1, got %d", n))
	}

	return maxDeliver(n)
}

type concurrency int

func (c concurrency) applyReact(r *reactSettings) { r.delivery.Concurrency = int(c) }

// Concurrency bounds the handler calls in flight at once, per instance.
// Concurrency(1) runs them one at a time in one instance; a failed message
// still comes back after its delay, behind the ones that followed it, and
// other instances run theirs meanwhile (see Ordered). It panics unless
// n >= 1.
func Concurrency(n int) ReactOption {
	if n < 1 {
		panic(fmt.Sprintf("event: Concurrency must be >= 1, got %d", n))
	}

	return concurrency(n)
}

type ordered struct{}

func (ordered) applyReact(r *reactSettings) { r.delivery.Ordered = true }

// Ordered handles the reactor's events strictly in stream order across all
// instances: one event in flight for the whole service (the consumer's
// max_ack_pending is 1, Concurrency 1). A failed event blocks the ones
// after it until it succeeds or is dead-lettered. The throughput is one
// handler call at a time, round trip included, whatever the replica count.
// Combined with Concurrency above 1 it panics.
func Ordered() ReactOption { return ordered{} }

type timeout time.Duration

func (t timeout) applyReact(r *reactSettings) { r.delivery.Timeout = time.Duration(t) }

// Timeout of one handler call: its ctx is cancelled after d and the call
// counts as failed. A message nobody settled (the instance died) comes back
// after d plus a margin. It panics unless d > 0.
func Timeout(d time.Duration) ReactOption {
	if d <= 0 {
		panic(fmt.Sprintf("event: Timeout must be positive, got %v", d))
	}

	return timeout(d)
}

type redelivery backoff.Policy

func (r redelivery) applyReact(s *reactSettings) { s.delivery.Redelivery = backoff.Policy(r) }

// Redelivery bounds the delay before a failed message comes back: minDelay
// after the first failure, doubling with every delivery up to maxDelay. It
// panics unless 0 < minDelay <= maxDelay.
func Redelivery(minDelay, maxDelay time.Duration) ReactOption {
	p := backoff.Policy{Min: minDelay, Max: maxDelay}
	if err := p.Validate(); err != nil {
		panic("event: redelivery: " + err.Error())
	}

	return redelivery(p)
}

type inactive time.Duration

func (i inactive) applyReact(r *reactSettings) { r.delivery.InactiveThreshold = time.Duration(i) }

// InactiveThreshold lets the server delete the reactor's consumer after d
// without any instance consuming, e.g. a reactor that was removed or
// renamed. A consumer deleted so is recreated at the next start where
// StartAt says, and what was published meanwhile is skipped with
// StartNew. It panics unless d > 0.
func InactiveThreshold(d time.Duration) ReactOption {
	if d <= 0 {
		panic(fmt.Sprintf("event: InactiveThreshold must be positive, got %v", d))
	}

	return inactive(d)
}

// Start is where a reactor's consumer starts when it is created.
type Start int

// Starts of StartAt.
const (
	// StartNew: events published after the consumer is created (default).
	StartNew Start = iota
	// StartAll: every event the source stream still retains.
	StartAll
)

type startAt Start

func (s startAt) applyReact(r *reactSettings) { r.delivery.StartAll = Start(s) == StartAll }

// StartAt chooses where the reactor's consumer starts when it is first
// created. It applies at creation only: an existing consumer keeps its
// position, so adding StartAt(StartAll) to a running reactor replays
// nothing. It panics on an unknown Start.
func StartAt(s Start) ReactOption {
	if s != StartNew && s != StartAll {
		panic(fmt.Sprintf("event: unknown Start %d", s))
	}

	return startAt(s)
}

// Redrive runs the handler of the service's reactor consumer (its name in
// the manifest's subscriptions) on the reactor's dead letters, oldest
// first, in this instance, and deletes each one it handles; one that fails
// again stays a dead letter. Other subscribers of the event do not see the
// events again. It returns how many were handled and an error when some
// failed again. Call it once, from one instance: two concurrent calls may
// handle a dead letter twice.
func Redrive(ctx context.Context, scope deps.Scope, consumer string) (int, error) {
	r, ok := transport[interface {
		Redrive(ctx context.Context, consumer string) (int, error)
	}](scope)
	if !ok {
		return 0, fmt.Errorf("event: redrive: %w", ErrUnavailable)
	}

	return r.Redrive(ctx, consumer) //nolint:wrapcheck // the broker wraps
}

// transport returns the scope's event transport as I.
func transport[I any](scope deps.Scope) (I, bool) {
	var zero I

	n := link.NodeOf(scope)
	if n == nil {
		return zero, false
	}

	e := env.Of(n)
	if e == nil {
		return zero, false
	}

	t, ok := e.Broker().(I)

	return t, ok
}
