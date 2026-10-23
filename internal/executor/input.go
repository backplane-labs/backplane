package executor

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/backplane-labs/backplane/internal/bindings"
)

// Kind is what a run executes.
type Kind string

// Kinds of runs.
const (
	// KindBinding: a hook call; the input is `req`, the result the hook's
	// output.
	KindBinding Kind = "binding"
	// KindRule: an event; the input is `event` with `meta`, no result.
	KindRule Kind = "rule"
)

// Input is the input of the workflow wire.BindingWorkflow: everything a
// run needs, nothing it has to look up — the compiled program travels
// with it, so a run is not affected by saves or manifests that come after
// its start, and replays evaluate exactly what was compiled.
//
// The rules engine starts the same workflow with Kind KindRule, the
// compiled rule, Meta and Identity.Rule.
type Input struct {
	Kind Kind `json:"kind"`
	// Program is a marshaled *bindings.Program (CompileBinding or
	// CompileRule) of the same kind.
	Program json.RawMessage `json:"program"`
	// Identity names what runs: ActivityCall.binding, the memo, metrics.
	Identity Identity `json:"identity"`
	// Payload is the hook's input (`req`) or the event's payload
	// (`event`): JSON; empty is {}.
	Payload json.RawMessage `json:"payload,omitempty"`
	// Meta are the event's CloudEvents attributes (`meta`); rules only.
	Meta *EventMeta `json:"meta,omitempty"`
	// Trace is the caller's W3C trace context (HookCall.trace, the event's
	// headers): ActivityCall.trace of every step when the workflow has no
	// span of its own.
	Trace map[string]string `json:"trace,omitempty"`
	// Metadata of the hook call (HookCall.metadata): ActivityCall.metadata
	// of every step. Bindings only.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Identity is what a run executes.
type Identity struct {
	// Hook is the full hook name of a binding.
	Hook string `json:"hook,omitempty"`
	// Rule is the id of a rule.
	Rule string `json:"rule,omitempty"`
	// Version of the binding or rule; 0: an unsaved definition (a test).
	Version int64 `json:"version,omitempty"`
	// Test: a console test run.
	Test bool `json:"test,omitempty"`
}

// String is "<hook>@<version>" or "rule:<id>@<version>"; an unsaved
// definition is "@draft": ActivityCall.binding and the memo.
func (i Identity) String() string {
	name := i.Hook
	if i.Rule != "" {
		name = "rule:" + i.Rule
	}

	if i.Version == 0 {
		return name + "@draft"
	}

	return name + "@" + strconv.FormatInt(i.Version, 10)
}

// EventMeta are the CloudEvents attributes of a rule's event.
type EventMeta struct {
	ID      string    `json:"id,omitempty"`
	Source  string    `json:"source,omitempty"`
	Subject string    `json:"subject,omitempty"`
	Type    string    `json:"type,omitempty"`
	Time    time.Time `json:"time"`
}

// meta is m as the program sees it.
func (m *EventMeta) meta() bindings.Meta {
	if m == nil {
		return bindings.Meta{}
	}

	return bindings.Meta{ID: m.ID, Source: m.Source, Subject: m.Subject, Type: m.Type, Time: m.Time}
}

// BindingInput is the input of a binding's run: the program compiled from
// the binding, the hook's input and the caller's trace.
func BindingInput(p *bindings.Program, id Identity, payload []byte, trace map[string]string) (Input, error) {
	return Input{Kind: KindBinding, Program: p.Spec(), Identity: id, Payload: jsonOrEmpty(payload), Trace: trace}, nil
}

// RuleInput is the input of a rule's run on an event.
func RuleInput(
	p *bindings.Program, id Identity, payload []byte, meta EventMeta, trace map[string]string,
) (Input, error) {
	return Input{
		Kind: KindRule, Program: p.Spec(), Identity: id, Payload: jsonOrEmpty(payload), Meta: &meta, Trace: trace,
	}, nil
}

// jsonOrEmpty is payload when it is JSON; empty is {}. Anything else is
// kept as a JSON string, so the input still marshals and the run fails
// readably on it.
func jsonOrEmpty(payload []byte) json.RawMessage {
	switch {
	case len(payload) == 0:
		return json.RawMessage("{}")
	case json.Valid(payload):
		return json.RawMessage(payload)
	default:
		return jsonString(string(payload))
	}
}
