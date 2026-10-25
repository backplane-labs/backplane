// Package rules is the rules engine (§8.1): every active rule — a binding
// whose source is an event — gets a durable consumer on its event's
// stream, and every event its `when` matches starts one run of the
// executor's binding workflow, rule/<id>/<ce-id>.
//
// The consumer is backplane__rule-<id> on bp_<service>, filtered on
// bp.<service>.<Event>, explicit acks, shared by all backplane replicas
// (competing consumers). A brand-new consumer starts at events published
// after it is created (deliver policy new): saving a rule does not replay
// the stream's history through it. An existing consumer keeps its
// position: a new version of the rule applies from the next event on, a
// pause stops consuming and keeps the consumer (the events wait), a
// resume continues where it stopped. A deleted rule's consumer is
// deleted.
//
// Per event: the CloudEvents headers become meta, `when` is evaluated;
// false — ack; true — the run starts (reuse policy "reject duplicate", so
// a redelivered or republished event finds its run and is acked); the ack
// follows the start, from then on durability is Temporal's. A start that
// fails is redelivered with backoff, without bound; an event without
// ce-id, with a payload that is not JSON or on which `when` fails is
// terminated and logged (rules have no DLQ).
//
// The engine reconciles on rule changes (bindings.Manager.Changes), on
// manifest changes (the registry) and periodically. Without NATS or
// Temporal it logs and does nothing; readiness does not depend on it.
package rules

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"go.temporal.io/sdk/client"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/direct"
	"github.com/backplane-labs/backplane/internal/registry"
	"github.com/backplane-labs/backplane/internal/wire"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	natsdriver "github.com/backplane-labs/backplane/pkg/backplane/drivers/nats"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
)

// Defaults of the engine.
const (
	// defaultConcurrency: deliveries of one rule handled at once in one
	// replica (and the pull batch).
	defaultConcurrency = 8
	// defaultResync: a full reconciliation (with the sweep of orphaned
	// consumers) runs this often even without changes.
	defaultResync = time.Minute
	// defaultRetry: a reconciliation that failed is repeated after this.
	defaultRetry = 5 * time.Second
	// defaultStartTimeout bounds one start of a run.
	defaultStartTimeout = 10 * time.Second
	// ackWait: an event whose delivery was not settled comes back after
	// this (the replica died mid-start).
	ackWait = time.Minute
	// ackSlack past a direct run's timeout before its event comes back.
	ackSlack = 30 * time.Second
	// opTimeout bounds one NATS call of reconciliation.
	opTimeout = 10 * time.Second
)

// Platform defaults of an event stream created by a subscriber (§8): the
// emitter overwrites them with its own at its start.
const (
	streamMaxAge = 7 * 24 * time.Hour
	streamDedup  = 2 * time.Minute
)

// Source is where rules come from: *bindings.Manager.
type Source interface {
	// Rules is every rule with its current version, deleted ones too.
	Rules(ctx context.Context) ([]bindings.RuleEntry, error)
	// RuleVersion is one version of a rule; 0: the current one.
	RuleVersion(ctx context.Context, id uuid.UUID, version int64) (bindings.RuleVersion, error)
	// Changes signals after every save, delete, pause and resume.
	Changes(ctx context.Context) <-chan struct{}
	// Catalog is the latest manifests.
	Catalog() (bindings.Catalog, error)
	// RuleAPI is RuleService without the run RPCs.
	RuleAPI() bindings.RuleAPI
}

var _ Source = (*bindings.Manager)(nil)

// Runs is the console's view of Temporal runs the run RPCs delegate to:
// ops.WorkflowAPI.
type Runs interface {
	ListRuns(ctx context.Context, req *consolev1.ListRunsRequest) (*consolev1.ListRunsResponse, error)
	GetRun(ctx context.Context, req *consolev1.GetRunRequest) (*consolev1.GetRunResponse, error)
	CancelRun(ctx context.Context, req *consolev1.CancelRunRequest) (*consolev1.CancelRunResponse, error)
}

// JetStreamFunc gives the NATS client; TemporalFunc the Temporal one.
type (
	JetStreamFunc func() (jetstream.JetStream, error)
	TemporalFunc  func() (client.Client, error)
)

// Option configures New.
type Option func(*Engine)

// WithJetStream replaces the SDK's NATS client (tests).
func WithJetStream(fn JetStreamFunc) Option { return func(e *Engine) { e.jet = fn } }

// WithTemporal replaces the SDK's Temporal client (tests).
func WithTemporal(fn TemporalFunc) Option { return func(e *Engine) { e.temporal = fn } }

// WithStarter starts runs through s (the executor's) instead of the
// engine's own TemporalStarter.
func WithStarter(s Starter) Option { return func(e *Engine) { e.starter = s } }

// WithRuns serves ListRuleRuns, GetRuleRun and CancelRuleRun through r
// (ops.WorkflowAPI); without it they are UNAVAILABLE.
func WithRuns(r Runs) Option { return func(e *Engine) { e.runs = r } }

// WithDirect installs the direct runtime: rules compiled for it run
// there, inline in the delivery; their runs are listed first.
func WithDirect(d *direct.Service) Option { return func(e *Engine) { e.direct = d } }

// TemporalEnabled says whether the installation has Temporal (default
// true); without it rules on the temporal runtime do not run and say so.
func TemporalEnabled(enabled bool) Option { return func(e *Engine) { e.temporalEnabled = enabled } }

// WithQueue is the task queue runs start on and are listed by; default:
// backplane's own (workflows.Queue).
func WithQueue(q string) Option { return func(e *Engine) { e.queue = q } }

// WithWorkflow is the workflow type of the TemporalStarter; default
// Workflow.
func WithWorkflow(name string) Option { return func(e *Engine) { e.workflow = name } }

// Concurrency is how many deliveries of one rule a replica handles at once
// (default 8).
func Concurrency(n int) Option {
	return func(e *Engine) {
		if n > 0 {
			e.concurrency = n
		}
	}
}

// StreamReplicas is the replica count of an event stream the engine
// creates because it is missing (default 1): the installation's
// nats.replicas, so a stream made for a subscriber on a JetStream cluster
// is replicated before its emitter applies its own configuration.
func StreamReplicas(n int) Option {
	return func(e *Engine) {
		if n > 0 {
			e.replicas = n
		}
	}
}

// Resync is how often a full reconciliation runs without changes
// (default 1 min).
func Resync(d time.Duration) Option {
	return func(e *Engine) {
		if d > 0 {
			e.resync = d
		}
	}
}

// Engine is the component. It holds consumers and goroutines: share it by
// pointer.
type Engine struct {
	deps.Component

	src      Source
	reg      registry.Source
	jet      JetStreamFunc
	temporal TemporalFunc
	// codec decodes payloads of history read through the raw API.
	codec   func() temporal.Codec
	starter Starter
	direct  *direct.Service
	// temporalEnabled: the installation has Temporal; false marks rules on
	// that runtime broken instead of starting runs that cannot start.
	temporalEnabled bool
	runs            Runs
	queue           string
	workflow        string
	concurrency     int
	replicas        int
	resync          time.Duration
	retry           time.Duration
	startTimeout    time.Duration
	metrics         metrics

	kick chan struct{}

	mu       sync.Mutex
	stopping bool
	inflight sync.WaitGroup

	// Owned by the reconciliation goroutine.
	consumers map[uuid.UUID]*consumer
	broken    map[uuid.UUID]string // last logged reason a rule does not run
	waiting   string               // last logged reason nothing runs
}

// New creates the engine under parent: rules from src, manifest changes
// from reg.
func New(parent deps.Scope, src Source, reg registry.Source, opts ...Option) *Engine {
	e := &Engine{Component: deps.NewComponent(parent, "rules")}
	e.init(src, reg, opts)
	e.jetDefault(func() (jetstream.JetStream, error) { return natsdriver.JetStream(e) })
	e.temporalDefault(func() (client.Client, error) { return workflows.Client(e) }, workflows.Queue(parent))
	e.codec = func() temporal.Codec { return workflows.Codec(e) }
	e.finish()

	e.Go(e.run)

	return e
}

// Detached is an engine outside the tree — it logs nowhere and does not
// consume — for the run RPCs in tests and tools.
func Detached(src Source, opts ...Option) *Engine {
	e := &Engine{}
	e.init(src, nil, opts)
	e.jetDefault(func() (jetstream.JetStream, error) { return nil, errNotConfigured })
	e.temporalDefault(func() (client.Client, error) { return nil, errNotConfigured }, Subscriber)
	e.codec = func() temporal.Codec { return temporal.Plain }
	e.finish()

	return e
}

var errNotConfigured = errors.New("not configured")

func (e *Engine) init(src Source, reg registry.Source, opts []Option) {
	e.src, e.reg = src, reg
	e.concurrency, e.resync, e.retry, e.startTimeout = defaultConcurrency, defaultResync, defaultRetry, defaultStartTimeout
	e.replicas = 1
	e.workflow = Workflow
	e.temporalEnabled = true
	e.kick = make(chan struct{}, 1)
	e.consumers = map[uuid.UUID]*consumer{}
	e.broken = map[uuid.UUID]string{}

	for _, o := range opts {
		o(e)
	}
}

func (e *Engine) jetDefault(fn JetStreamFunc) {
	if e.jet == nil {
		e.jet = fn
	}
}

func (e *Engine) temporalDefault(fn TemporalFunc, queue string) {
	if e.temporal == nil {
		e.temporal = fn
	}

	if e.queue == "" {
		e.queue = queue
	}
}

func (e *Engine) finish() {
	e.metrics = newMetrics(e.Meter())

	if e.starter == nil {
		e.starter = NewTemporalStarter(e.temporal, e.queue, e.workflow)
	}
}

// Queue is the task queue rule runs start on.
func (e *Engine) Queue() string { return e.queue }

// run reconciles until ctx ends, then stops consuming and waits for the
// deliveries in flight.
func (e *Engine) run(ctx context.Context) error {
	defer e.stopAll()

	rules := e.src.Changes(ctx)
	manifests := e.reg.Changes(ctx)

	timer := time.NewTimer(0)
	defer timer.Stop()

	sweep := true

	for {
		select {
		case <-ctx.Done():
		case <-rules:
			sweep = true
		case <-manifests:
		case <-e.kick:
		case <-timer.C:
			sweep = true
		}

		if ctx.Err() != nil {
			return nil
		}

		next := e.resync

		if err := e.reconcile(ctx, sweep); err != nil {
			next = e.retry

			if ctx.Err() == nil {
				e.Log().Debug("rules reconcile", xlog.Err(err))
			}
		} else {
			sweep = false
		}

		timer.Reset(next)
	}
}

// errWaiting: NATS, Temporal or the registry is not there yet.
var errWaiting = errors.New("rules: waiting")

// reconcile takes the consumers to what the rules want. sweep also
// deletes rule consumers this replica is not consuming whose rule is
// deleted or moved to another stream.
func (e *Engine) reconcile(ctx context.Context, sweep bool) error {
	jet, err := e.ready()
	if err != nil {
		return err
	}

	cat, err := e.src.Catalog()
	if err != nil {
		e.wait(fmt.Sprintf("manifests: %v", err))

		return fmt.Errorf("%w: %w", errWaiting, err)
	}

	entries, err := e.src.Rules(ctx)
	if err != nil {
		return fmt.Errorf("rules: %w", err)
	}

	e.wait("")

	e.checkLost(ctx, jet)

	want := plan(entries, cat, e.temporalEnabled)
	e.report(want)

	var errs []error

	actions := diff(e.current(), want)
	for i := range actions {
		if err := e.apply(ctx, jet, actions[i]); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", actions[i], err))
		}
	}

	if sweep {
		if err := e.sweep(ctx, jet, want); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// ready is the NATS client when both NATS and Temporal are configured
// and connected; otherwise it logs once and reports errWaiting.
func (e *Engine) ready() (jetstream.JetStream, error) {
	jet, err := e.jet()
	if err != nil {
		e.wait(fmt.Sprintf("nats: %v", err))

		return nil, fmt.Errorf("%w: nats: %w", errWaiting, err)
	}

	// Temporal is needed only by rules on its runtime; without it in the
	// installation the direct runtime runs the rest.
	if e.temporalEnabled {
		if _, err := e.temporal(); err != nil {
			e.wait(fmt.Sprintf("temporal: %v", err))

			return nil, fmt.Errorf("%w: temporal: %w", errWaiting, err)
		}
	}

	return jet, nil
}

// wait logs why rules do not run when the reason changes; "" — they do.
func (e *Engine) wait(reason string) {
	if reason == e.waiting {
		return
	}

	switch reason {
	case "":
		e.Log().Info("rules running")
	default:
		e.Log().Info("rules not running", xlog.String("reason", reason))
	}

	e.waiting = reason
}

// report logs rules that do not run for a reason other than a pause,
// once per reason.
func (e *Engine) report(want map[uuid.UUID]desired) {
	for id, d := range want { //nolint:gocritic // rangeValCopy: a map value is copied either way
		reason := ""
		if d.err != nil && !d.deleted && !d.paused {
			reason = d.err.Error()
		}

		if e.broken[id] == reason {
			continue
		}

		if reason != "" {
			e.Log().Warn("rule does not run", xlog.String("rule", id.String()), xlog.Int64("version", d.version),
				xlog.String("event", d.event), xlog.String("reason", reason))
		}

		if reason == "" {
			delete(e.broken, id)
		} else {
			e.broken[id] = reason
		}
	}

	for id := range e.broken {
		if _, ok := want[id]; !ok {
			delete(e.broken, id)
		}
	}
}

// current is what the consumers being consumed are.
func (e *Engine) current() map[uuid.UUID]running {
	out := make(map[uuid.UUID]running, len(e.consumers))
	for id, c := range e.consumers {
		out[id] = c.state
	}

	return out
}

// checkLost looks up consumers consumption reported gone: one deleted on
// the server stops being consumed, and the diff creates it again (at new
// events: its position went with it).
func (e *Engine) checkLost(ctx context.Context, jet jetstream.JetStream) {
	for id, c := range e.consumers {
		if !c.lost.Load() {
			continue
		}

		lctx, cancel := context.WithTimeout(ctx, opTimeout)
		_, err := jet.Consumer(lctx, c.state.stream, c.state.durable)

		cancel()

		switch {
		case err == nil:
			c.lost.Store(false)
		case errors.Is(err, jetstream.ErrConsumerNotFound):
			e.Log().Warn("rule consumer deleted on the server, recreating at new events",
				xlog.String("rule", id.String()), xlog.String("durable", c.state.durable))
			c.cc.Stop()
			delete(e.consumers, id)
		}
	}
}

// apply carries out one action.
func (e *Engine) apply(ctx context.Context, jet jetstream.JetStream, a action) error {
	actx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	switch a.kind {
	case actStart:
		return e.start(ctx, actx, jet, a.want)
	case actUpdate:
		return e.update(actx, jet, a.want)
	case actStop:
		e.stop(a.id)
		e.Log().Info("rule stopped, consumer kept", xlog.String("rule", a.id.String()), xlog.Bool("paused", a.want.paused))
	case actDrop:
		e.stop(a.id)

		err := jet.DeleteConsumer(actx, a.stream, a.durable)
		if err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) {
			return fmt.Errorf("delete consumer %s: %w", a.durable, err)
		}

		e.Log().Info("rule consumer deleted", xlog.String("rule", a.id.String()), xlog.String("durable", a.durable))
	}

	return nil
}

// start ensures the event's stream and the rule's consumer and consumes
// it; work is the context of the deliveries.
func (e *Engine) start(work, ctx context.Context, jet jetstream.JetStream, d desired) error {
	if err := ensureStream(ctx, jet, e.eventStream(d.service)); err != nil {
		return err
	}

	cons, err := e.ensureConsumer(ctx, jet, d)
	if err != nil {
		return err
	}

	c := &consumer{
		id:    d.id,
		state: running{stream: d.stream, subject: d.subject, durable: d.durable, version: d.version, spec: d.spec},
		sem:   make(chan struct{}, e.concurrency),
	}
	c.prog.Store(&program{prog: d.prog, version: d.version})

	cc, err := cons.Consume(func(msg jetstream.Msg) { e.dispatch(work, c, msg) },
		jetstream.PullMaxMessages(e.concurrency),
		jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
			if errors.Is(err, jetstream.ErrConsumerDeleted) || errors.Is(err, jetstream.ErrConsumerNotFound) ||
				errors.Is(err, jetstream.ErrNoHeartbeat) {
				c.lost.Store(true)

				select {
				case e.kick <- struct{}{}:
				default:
				}
			}
		}),
	)
	if err != nil {
		return fmt.Errorf("consume %s: %w", d.durable, err)
	}

	c.cc = cc
	e.consumers[d.id] = c

	e.Log().Info("rule consuming", xlog.String("rule", d.id.String()), xlog.Int64("version", d.version),
		xlog.String("event", d.event), xlog.String("durable", d.durable))

	return nil
}

// update rewrites the consumer (its position stays) and swaps the program:
// deliveries from now on evaluate the new version.
func (e *Engine) update(ctx context.Context, jet jetstream.JetStream, d desired) error {
	c := e.consumers[d.id]
	if c == nil {
		return nil
	}

	// The program first: once the consumer says the new version, its
	// deliveries evaluate it. A failed rewrite is retried by the next
	// reconciliation (the state still differs).
	c.prog.Store(&program{prog: d.prog, version: d.version})

	if _, err := e.ensureConsumer(ctx, jet, d); err != nil {
		return err
	}

	c.state = running{stream: d.stream, subject: d.subject, durable: d.durable, version: d.version, spec: d.spec}

	e.Log().Info("rule updated", xlog.String("rule", d.id.String()), xlog.Int64("version", d.version),
		xlog.String("event", d.event))

	return nil
}

// stop stops consuming the rule's consumer; deliveries in flight finish.
func (e *Engine) stop(id uuid.UUID) {
	if c := e.consumers[id]; c != nil {
		c.cc.Stop()
		delete(e.consumers, id)
	}
}

// stopAll stops every consumer and waits for the deliveries in flight.
func (e *Engine) stopAll() {
	e.mu.Lock()
	e.stopping = true
	e.mu.Unlock()

	for id := range e.consumers {
		e.stop(id)
	}

	e.inflight.Wait()
}

// sweep deletes rule consumers of deleted rules and consumers left on a
// stream the rule's event no longer lives on (see orphan).
func (e *Engine) sweep(ctx context.Context, jet jetstream.JetStream, want map[uuid.UUID]desired) error {
	names := jet.StreamNames(ctx)

	var errs []error

	for stream := range names.Name() {
		if !strings.HasPrefix(stream, "bp_") || strings.HasPrefix(stream, wire.DLQStreamName("")) {
			continue
		}

		errs = append(errs, e.sweepStream(ctx, jet, stream, want)...)
	}

	if err := names.Err(); err != nil {
		errs = append(errs, fmt.Errorf("streams: %w", err))
	}

	return errors.Join(errs...)
}

// sweepStream deletes the orphaned rule consumers of one stream.
func (e *Engine) sweepStream(
	ctx context.Context, jet jetstream.JetStream, stream string, want map[uuid.UUID]desired,
) []error {
	prefix := wire.Token(Subscriber) + "__rule-"

	s, err := jet.Stream(ctx, stream)
	if err != nil {
		if !errors.Is(err, jetstream.ErrStreamNotFound) {
			return []error{fmt.Errorf("stream %s: %w", stream, err)}
		}

		return nil
	}

	var errs []error

	infos := s.ListConsumers(ctx)
	for info := range infos.Info() {
		if !strings.HasPrefix(info.Name, prefix) {
			continue
		}

		id, version, ok := ruleOf(info.Config.Metadata)
		if !ok || !orphan(want, id, stream, version) {
			continue
		}

		if c := e.consumers[id]; c != nil && c.state.stream == stream {
			continue
		}

		if err := jet.DeleteConsumer(ctx, stream, info.Name); err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) {
			errs = append(errs, fmt.Errorf("delete consumer %s: %w", info.Name, err))

			continue
		}

		e.Log().Info("orphaned rule consumer deleted", xlog.String("rule", id.String()),
			xlog.String("stream", stream), xlog.String("durable", info.Name))
	}

	if err := infos.Err(); err != nil {
		errs = append(errs, fmt.Errorf("consumers of %s: %w", stream, err))
	}

	return errs
}

// ruleOf reads a rule consumer's metadata: the rule and the version that
// wrote it.
func ruleOf(metadata map[string]string) (uuid.UUID, int64, bool) {
	id, err := uuid.Parse(metadata[MetaRule])
	if err != nil {
		return uuid.Nil, 0, false
	}

	version, err := strconv.ParseInt(metadata[MetaVersion], 10, 64)
	if err != nil {
		return uuid.Nil, 0, false
	}

	return id, version, true
}

// eventStream is the configuration of service's event stream a subscriber
// creates when it is missing: the platform's defaults (§8), replicated as
// the installation's streams are.
func (e *Engine) eventStream(service string) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:        wire.StreamName(service),
		Description: "backplane events of " + service,
		Subjects:    []string{wire.StreamSubjects(service)},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		Discard:     jetstream.DiscardOld,
		MaxAge:      streamMaxAge,
		MaxBytes:    -1,
		Duplicates:  streamDedup,
		Replicas:    e.replicas,
		Metadata: map[string]string{
			wire.MetaService: service, wire.MetaBy: wire.BySubscriber, wire.MetaKind: wire.KindEvents,
		},
	}
}

// ensureStream creates the event's stream cfg when it is missing and
// leaves an existing one alone: its emitter owns the configuration.
func ensureStream(ctx context.Context, jet jetstream.JetStream, cfg jetstream.StreamConfig) error {
	_, err := jet.Stream(ctx, cfg.Name)
	if err == nil {
		return nil
	}

	if !errors.Is(err, jetstream.ErrStreamNotFound) {
		return fmt.Errorf("stream %s: %w", cfg.Name, err)
	}

	if _, err := jet.CreateStream(ctx, cfg); err != nil && !errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
		return fmt.Errorf("create stream %s: %w", cfg.Name, err)
	}

	return nil
}

// consumerConfig of the rule's consumer. Explicit acks; deliveries are
// not bounded (a run that cannot start waits for Temporal, the engine
// naks with its own delay); a new consumer starts at new events.
func (e *Engine) consumerConfig(d desired) jetstream.ConsumerConfig {
	wait := ackWait
	if e.direct != nil && d.prog != nil && d.prog.Runtime() == consolev1.Runtime_RUNTIME_DIRECT {
		// A direct run completes within the delivery: the event comes back
		// only after the run's own timeout, with slack.
		wait = max(wait, e.direct.RunTimeout()+ackSlack)
	}

	return jetstream.ConsumerConfig{
		Durable:       d.durable,
		Description:   "backplane rule " + d.id.String() + " on " + d.event,
		FilterSubject: d.subject,
		DeliverPolicy: jetstream.DeliverNewPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       wait,
		MaxDeliver:    -1,
		Metadata: map[string]string{
			wire.MetaService: Subscriber, wire.MetaConsumer: ConsumerName(d.id), wire.MetaEvent: d.event,
			MetaRule: d.id.String(), MetaVersion: strconv.FormatInt(d.version, 10),
		},
	}
}

// ensureConsumer creates or updates the rule's consumer. The server does
// not let a consumer's start change, so an existing one keeps its own —
// and its position.
func (e *Engine) ensureConsumer(ctx context.Context, jet jetstream.JetStream, d desired) (jetstream.Consumer, error) {
	cfg := e.consumerConfig(d)

	have, err := jet.Consumer(ctx, d.stream, d.durable)

	switch {
	case err == nil:
		old := have.CachedInfo().Config
		cfg.DeliverPolicy, cfg.OptStartSeq, cfg.OptStartTime = old.DeliverPolicy, old.OptStartSeq, old.OptStartTime
	case !errors.Is(err, jetstream.ErrConsumerNotFound):
		return nil, fmt.Errorf("consumer %s: %w", d.durable, err)
	}

	cons, err := jet.CreateOrUpdateConsumer(ctx, d.stream, cfg)
	if err != nil {
		return nil, fmt.Errorf("consumer %s: %w", d.durable, err)
	}

	return cons, nil
}
