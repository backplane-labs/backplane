// Package env is what every node of one service shares: the manifest being
// declared into and the wiring of events, hooks and activities to a
// transport. Declaration packages reach it through the node a Scope wraps,
// so nothing is process-global.
package env

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/backoff"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

// ErrUnavailable: no transport carries this call (yet).
var ErrUnavailable = errors.New("backplane: transport unavailable")

// ErrNoBinding: backplane has no binding for the hook.
var ErrNoBinding = errors.New("no binding")

// NonRetryableError marks a handler error the transport must not retry:
// the input is wrong, retrying cannot help.
type NonRetryableError struct{ Err error }

func (e NonRetryableError) Error() string { return e.Err.Error() }

func (e NonRetryableError) Unwrap() error { return e.Err }

// RetryAfterError marks a handler error retryable no sooner than Delay.
type RetryAfterError struct {
	Err   error
	Delay time.Duration
}

func (e RetryAfterError) Error() string { return e.Err.Error() }

func (e RetryAfterError) Unwrap() error { return e.Err }

// Broker carries events (NATS JetStream; a recorder in tests).
type Broker interface {
	Publish(ctx context.Context, m Message) error
}

// Caller carries hook calls (Temporal Nexus; a stub in tests). Payloads are
// encoded JSON.
type Caller interface {
	Call(ctx context.Context, hook string, in []byte) ([]byte, error)
}

// Handler serves an activity or a reactor; payloads are encoded JSON.
type Handler func(ctx context.Context, in []byte) ([]byte, error)

// Reactor is a declared durable consumer of another service's event.
type Reactor struct {
	Event    string // full name "<service>.<Event>"
	Consumer string // unique within the service
	Handler  Handler
	Delivery Delivery
}

// Delivery is how a reactor's messages are delivered, as its author
// declared it; a zero field means the transport's default.
type Delivery struct {
	MaxDeliver  int            // deliveries before the dead letter
	Concurrency int            // handlers in flight at once
	Timeout     time.Duration  // of one handler call
	Redelivery  backoff.Policy // delay after a failed delivery
	StartAll    bool           // a new consumer starts at the stream's first message
	// Ordered: one message in flight across every instance (strict order).
	Ordered bool
	// InactiveThreshold: the server deletes the consumer after this long
	// without an instance pulling; zero never.
	InactiveThreshold time.Duration
}

// Env of one service.
type Env struct {
	Service  string
	Manifest *manifest.Builder

	mu         sync.RWMutex
	broker     Broker
	caller     Caller
	activities map[string]Handler
	own        map[string]Handler // activities only the service's workflows run
	reactors   []Reactor
	registers  []func(registry any)
	workflows  func() (any, error)

	hookTimeout time.Duration // platform default of a hook call

	// bound is the service's hooks with a binding in force (short names),
	// nil until backplane's set is known.
	bound     map[string]bool
	boundSubs map[chan struct{}]struct{}

	// followed: the section names the service follows, each with the
	// functions told of its published set.
	followed map[string][]func(raw []byte)
}

// New creates the env of service.
func New(service string, m *manifest.Builder) *Env {
	return &Env{
		Service: service, Manifest: m, activities: map[string]Handler{}, own: map[string]Handler{},
		boundSubs: map[chan struct{}]struct{}{}, followed: map[string][]func([]byte){},
	}
}

// FollowSection has fn told of section name's published set (a
// backplanev1.SectionSet; nil when there is none) while the service runs.
func (e *Env) FollowSection(name string, fn func(raw []byte)) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.followed[name] = append(e.followed[name], fn)
}

// Followed is every followed section name with its functions.
func (e *Env) Followed() map[string][]func(raw []byte) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make(map[string][]func([]byte), len(e.followed))
	for name, fns := range e.followed {
		out[name] = slices.Clone(fns)
	}

	return out
}

// Of returns the env of the tree n belongs to.
func Of(n *node.Node) *Env {
	e, _ := n.Env().(*Env)

	return e
}

// SetBroker installs the event transport; nil removes it.
func (e *Env) SetBroker(b Broker) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.broker = b
}

// Broker is the installed event transport or nil.
func (e *Env) Broker() Broker {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.broker
}

// SetCaller installs the hook transport; nil removes it.
func (e *Env) SetCaller(c Caller) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.caller = c
}

// Caller is the installed hook transport or nil.
func (e *Env) Caller() Caller {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.caller
}

// Activity stores the handler of a declared activity. A name an own
// activity has panics: both are one Temporal activity name on the queue.
func (e *Env) Activity(name string, h Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, ok := e.own[name]; ok {
		panic("backplane: activity " + name + " declared twice: by workflows.HandleActivity and as a platform activity")
	}

	e.activities[name] = h
}

// OwnActivity stores the handler of an activity only the service's own
// workflows run: registered on the worker as a declared one is, not in the
// manifest and not callable by the platform. A name taken panics.
func (e *Env) OwnActivity(name string, h Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()

	_, platform := e.activities[name]
	if _, own := e.own[name]; own || platform {
		panic("backplane: activity " + name + " declared twice")
	}

	e.own[name] = h
}

// OwnActivities lists the activities of OwnActivity by name.
func (e *Env) OwnActivities() map[string]Handler {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return maps.Clone(e.own)
}

// Reactor stores a declared reactor.
func (e *Env) Reactor(r Reactor) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.reactors = append(e.reactors, r)
}

// Reactors lists the declared reactors in declaration order.
func (e *Env) Reactors() []Reactor {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return append([]Reactor(nil), e.reactors...)
}

// Activities lists the declared activities by name.
func (e *Env) Activities() map[string]Handler {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make(map[string]Handler, len(e.activities))
	for k, v := range e.activities {
		out[k] = v
	}

	return out
}

// ActivityHandler returns the handler of a declared activity.
func (e *Env) ActivityHandler(name string) (Handler, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	h, ok := e.activities[name]

	return h, ok
}

// ReactorOf returns the declared reactor named consumer.
func (e *Env) ReactorOf(consumer string) (Reactor, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, r := range e.reactors {
		if r.Consumer == consumer {
			return r, true
		}
	}

	return Reactor{}, false
}

// RegisterWorker adds fn, called with the service's Temporal worker
// (worker.Registry) whenever the worker is built.
func (e *Env) RegisterWorker(fn func(registry any)) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.registers = append(e.registers, fn)
}

// WorkerRegistrations are the functions given to RegisterWorker.
func (e *Env) WorkerRegistrations() []func(registry any) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return append([]func(any){}, e.registers...)
}

// SetWorkflowClient installs how the Temporal client is reached.
func (e *Env) SetWorkflowClient(fn func() (any, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.workflows = fn
}

// WorkflowClient is the connected Temporal client (a client.Client).
func (e *Env) WorkflowClient() (any, error) {
	e.mu.RLock()
	fn := e.workflows
	e.mu.RUnlock()

	if fn == nil {
		return nil, fmt.Errorf("temporal is not configured: %w", ErrUnavailable)
	}

	return fn()
}

// SetBound installs the service's hooks with a binding in force (names
// without the service prefix) and signals BoundChanges when it differs;
// nil returns to unknown.
func (e *Env) SetBound(hooks []string) {
	var set map[string]bool
	if hooks != nil {
		set = make(map[string]bool, len(hooks))
		for _, h := range hooks {
			set[h] = true
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if (set == nil) == (e.bound == nil) && maps.Equal(set, e.bound) {
		return
	}

	e.bound = set

	for ch := range e.boundSubs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Bound reports whether hook (its short name) may have a binding: false
// only when backplane's set is known and lacks it.
func (e *Env) Bound(hook string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.bound == nil || e.bound[hook]
}

// BoundChanges signals after every change of the bound set until ctx
// ends; one pending signal at most.
func (e *Env) BoundChanges(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)

	e.mu.Lock()
	e.boundSubs[ch] = struct{}{}
	e.mu.Unlock()

	context.AfterFunc(ctx, func() {
		e.mu.Lock()
		delete(e.boundSubs, ch)
		e.mu.Unlock()
		close(ch)
	})

	return ch
}
