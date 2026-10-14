package rules

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/backplane-labs/backplane/internal/bindings"
)

// Outcome is outcome: what became of a delivery.
type Outcome = outcome

// Outcomes of a delivery.
const (
	OutcomeSkipped = outcomeSkipped
	OutcomeStarted = outcomeStarted
	OutcomeDedup   = outcomeDedup
	OutcomeFailed  = outcomeFailed
	OutcomeInvalid = outcomeInvalid
)

// Message is message: a delivery handle settles.
type Message = message

// Handle is handle of msg by rule id's program prog at version.
func (e *Engine) Handle(
	ctx context.Context, id uuid.UUID, prog *bindings.Program, version int64, msg Message,
) Outcome {
	return e.handle(ctx, id, &program{prog: prog, version: version}, msg)
}

// NakDelay is nakDelay.
func NakDelay(n uint64) time.Duration { return nakDelay(n) }

// Desired is desired: what one rule wants of its consumer.
type Desired = desired

// NewDesired is a rule's want with only the fields orphan reads.
func NewDesired(id uuid.UUID, version int64, stream string, deleted bool) Desired {
	return desired{id: id, version: version, stream: stream, deleted: deleted}
}

// Runnable is runnable.
func (d desired) Runnable() bool { return d.runnable() }

// Prog is the compiled program.
func (d desired) Prog() *bindings.Program { return d.prog }

// Spec is the serialized program.
func (d desired) Spec() []byte { return d.spec }

// Version is the rule's version.
func (d desired) Version() int64 { return d.version }

// Service is the event's service.
func (d desired) Service() string { return d.service }

// Stream is the event's stream.
func (d desired) Stream() string { return d.stream }

// Subject is the event's subject.
func (d desired) Subject() string { return d.subject }

// Durable is the consumer's durable name.
func (d desired) Durable() string { return d.durable }

// Deleted reports a deleted rule.
func (d desired) Deleted() bool { return d.deleted }

// Err is why the rule does not compile.
func (d desired) Err() error { return d.err }

// Plan is plan.
func Plan(entries []bindings.RuleEntry, cat bindings.Catalog) map[uuid.UUID]Desired {
	return plan(entries, cat)
}

// Running is running: a consumer being consumed.
type Running = running

// RunningOf is the consumer d runs once reconciled.
func RunningOf(d Desired) Running {
	return running{stream: d.stream, subject: d.subject, durable: d.durable, version: d.version, spec: d.spec}
}

// Action is action: one step of a reconciliation.
type Action = action

// ActionKind is actionKind.
type ActionKind = actionKind

// Kinds of actions.
const (
	ActStart = actStart
	ActDrop  = actDrop
)

// Kind is what the action does.
func (a action) Kind() ActionKind { return a.kind }

// ID is the rule acted on.
func (a action) ID() uuid.UUID { return a.id }

// Stream is the stream of the consumer acted on.
func (a action) Stream() string { return a.stream }

// Diff is diff.
func Diff(cur map[uuid.UUID]Running, want map[uuid.UUID]Desired) []Action { return diff(cur, want) }

// Orphan is orphan.
func Orphan(want map[uuid.UUID]Desired, id uuid.UUID, stream string, version int64) bool {
	return orphan(want, id, stream, version)
}

// ConsumerConfig is consumerConfig.
func ConsumerConfig(d Desired) jetstream.ConsumerConfig { return consumerConfig(d) }

// RuleOf is ruleOf.
func RuleOf(metadata map[string]string) (uuid.UUID, int64, bool) { return ruleOf(metadata) }
