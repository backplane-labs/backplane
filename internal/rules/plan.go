package rules

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/google/uuid"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/wire"
)

// Subscriber is the service name rule consumers are made under: their
// durable is backplane__rule-<id> (wire.Durable).
const Subscriber = "backplane"

// Metadata of a rule's consumer besides wire.MetaService, MetaConsumer
// and MetaEvent: the rule and the version that last wrote the consumer.
const (
	MetaRule    = "bp.rule"
	MetaVersion = "bp.rule-version"
)

// ConsumerName is the consumer name of rule id: rule-<id>.
func ConsumerName(id uuid.UUID) string { return "rule-" + id.String() }

// DurableName is the NATS durable of rule id's consumer:
// backplane__rule-<id>.
func DurableName(id uuid.UUID) string { return wire.Durable(Subscriber, ConsumerName(id)) }

// desired is what one rule wants of its consumer.
type desired struct {
	id      uuid.UUID
	version int64
	event   string
	// service is the event's service: its stream is stream.
	service string
	stream  string
	subject string
	durable string
	deleted bool
	paused  bool
	// prog is the compiled program of an active rule; nil when paused,
	// deleted or invalid (err says why).
	prog *bindings.Program
	// spec is prog's serialized form: a changed manifest may change the
	// program without a new version.
	spec []byte
	err  error
}

// errNoTemporal: the rule runs on Temporal, which the installation lacks.
var errNoTemporal = errors.New("the rule runs on the temporal runtime, which this installation does not have: " +
	"set runtime: direct")

// runnable: the rule's events are consumed.
func (d desired) runnable() bool { return !d.deleted && !d.paused && d.prog != nil }

// plan is what every rule wants: its consumer's names and, for an active
// rule, its program compiled against cat. A deleted rule stays in the
// plan (deleted) so its consumer can be found and removed.
func plan(entries []bindings.RuleEntry, cat bindings.Catalog, temporal bool) map[uuid.UUID]desired {
	out := make(map[uuid.UUID]desired, len(entries))

	for i := range entries {
		e := &entries[i]
		d := desired{
			id: e.ID, version: e.Current.Version, event: e.Current.Rule.Event, deleted: e.Current.Deleted,
			paused: e.Paused, durable: DurableName(e.ID),
		}

		if !d.deleted {
			svc, name, err := wire.SplitEvent(d.event)
			if err != nil {
				d.err = err
			} else {
				d.service, d.stream, d.subject = svc, wire.StreamName(svc), wire.Subject(svc, name)
			}
		}

		if d.err == nil && !d.deleted && !d.paused {
			d.prog, d.err = bindings.CompileRule(e.Current.Rule, cat)
			if d.err == nil && !temporal && d.prog.Runtime() != consolev1.Runtime_RUNTIME_DIRECT {
				d.err = errNoTemporal
			}

			if d.err == nil {
				d.spec, d.err = json.Marshal(d.prog)
			}

			if d.err != nil {
				d.prog, d.spec = nil, nil
			}
		}

		out[e.ID] = d
	}

	return out
}

// actionKind is what reconciliation does to one rule's consumer.
type actionKind int

const (
	// actStart ensures the stream and the consumer and starts consuming.
	actStart actionKind = iota + 1
	// actUpdate rewrites the consumer (filter, metadata; the position is
	// kept) and swaps the program; consumption goes on.
	actUpdate
	// actStop stops consuming and keeps the consumer: a pause or a program
	// that no longer compiles — events wait at the consumer's position.
	actStop
	// actDrop stops consuming and deletes the consumer: the rule is
	// deleted, or its event moved to another service's stream.
	actDrop
)

func (k actionKind) String() string {
	switch k {
	case actStart:
		return "start"
	case actUpdate:
		return "update"
	case actStop:
		return "stop"
	case actDrop:
		return "drop"
	default:
		return strconv.Itoa(int(k))
	}
}

// action is one step of a reconciliation: stream and durable name the
// consumer acted on (the running one for stop and drop).
type action struct {
	kind    actionKind
	id      uuid.UUID
	stream  string
	durable string
	want    desired
}

func (a action) String() string { return fmt.Sprintf("%s %s", a.kind, a.id) }

// running is what reconciliation knows of a consumer being consumed.
type running struct {
	stream  string
	subject string
	durable string
	version int64
	spec    []byte
}

// diff is the actions that take the consumers being consumed (cur) to
// want, ordered by rule id: drops before starts of the same rule.
func diff(cur map[uuid.UUID]running, want map[uuid.UUID]desired) []action {
	var out []action

	for _, id := range ruleIDs(cur, want) {
		c, isRunning := cur[id]
		d, isWanted := want[id]

		switch {
		case !isRunning:
			if isWanted && d.runnable() {
				out = append(out, action{kind: actStart, id: id, stream: d.stream, durable: d.durable, want: d})
			}
		case !isWanted || d.deleted:
			out = append(out, action{kind: actDrop, id: id, stream: c.stream, durable: c.durable, want: d})
		case d.stream != c.stream:
			out = append(out, action{kind: actDrop, id: id, stream: c.stream, durable: c.durable, want: d})
			if d.runnable() {
				out = append(out, action{kind: actStart, id: id, stream: d.stream, durable: d.durable, want: d})
			}
		case !d.runnable():
			out = append(out, action{kind: actStop, id: id, stream: c.stream, durable: c.durable, want: d})
		case d.subject != c.subject || d.version != c.version || !bytes.Equal(d.spec, c.spec):
			out = append(out, action{kind: actUpdate, id: id, stream: d.stream, durable: d.durable, want: d})
		}
	}

	return out
}

// ruleIDs are the rules either running or wanted, ordered by id.
func ruleIDs(cur map[uuid.UUID]running, want map[uuid.UUID]desired) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(cur)+len(want))
	for id := range cur {
		ids = append(ids, id)
	}

	for id := range want {
		if _, ok := cur[id]; !ok {
			ids = append(ids, id)
		}
	}

	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })

	return ids
}

// orphan reports whether a rule consumer found on stream, written by
// version, is to be deleted: its rule is deleted or its event now lives
// on another stream. A rule this replica does not know, or a consumer
// written by a newer version than it has seen, is left alone: another
// replica may be ahead of it.
func orphan(want map[uuid.UUID]desired, id uuid.UUID, stream string, version int64) bool {
	d, ok := want[id]
	if !ok || version > d.version {
		return false
	}

	return d.deleted || d.stream != stream
}
