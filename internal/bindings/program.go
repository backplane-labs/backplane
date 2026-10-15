package bindings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"time"

	"github.com/google/cel-go/cel"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/redact"
)

// Kind is what a program implements.
type Kind int

// Kinds of programs.
const (
	// KindBinding implements a hook: input `req`, a result.
	KindBinding Kind = iota + 1
	// KindRule reacts to an event: input `event` and `meta`, a filter, no
	// result.
	KindRule
	// KindBody is a for-each step's sub-flow (Program.Body): it runs in the
	// scope of an item, its result is the item's output.
	KindBody
)

// StepPlan is a step as the executor runs it: the activity, where it
// runs, what it waits for and its resolved options.
type StepPlan struct {
	Name     string `json:"name"`
	Activity string `json:"activity"`
	// Service owns the activity: its task queue (§17).
	Service string `json:"service"`
	// Kind: a Temporal activity, or a workflow run as a child workflow.
	Kind backplanev1.ActivityKind `json:"kind"`
	// Deps are the steps this one waits for: its `after` and every step
	// its input and when read.
	Deps []string `json:"deps,omitempty"`
	// Group is the step's level in Program.Groups: every dependency is in
	// a lower group.
	Group int `json:"group"`
	// Conditional: the step has a when (Program.When decides).
	Conditional bool `json:"conditional,omitempty"`
	// Undo is the compensating activity, empty for none; UndoService and
	// UndoKind as Service and Kind.
	Undo        string                   `json:"undo,omitempty"`
	UndoService string                   `json:"undo_service,omitempty"`
	UndoKind    backplanev1.ActivityKind `json:"undo_kind,omitempty"`
	// Retry is resolved: every field set.
	Retry        Retry         `json:"retry"`
	StartToClose time.Duration `json:"start_to_close"`
	// Heartbeat 0: none.
	Heartbeat time.Duration `json:"heartbeat,omitempty"`
	// ForEach: the step runs once per item; nil: once.
	ForEach *ForEachPlan `json:"for_each,omitempty"`
}

// ForEachPlan is how a for-each step runs its items.
type ForEachPlan struct {
	// Item names the item variable; its position is Item+"Index".
	Item string `json:"item"`
	// Concurrency bounds the items in flight.
	Concurrency int `json:"concurrency"`
	// Continue: failed items are collected, the step succeeds; else the
	// first failure fails it.
	Continue bool `json:"continue,omitempty"`
	// MaxItems bounds the items: more fails the step.
	MaxItems int `json:"max_items"`
	// Steps: the body is a sub-flow (Program.Body), else the activity.
	Steps bool `json:"steps,omitempty"`
}

// spec is a Program's serialized form: everything evaluation needs,
// nothing it has to look up. Values are their JSON form.
type spec struct {
	Kind   Kind            `json:"kind"`
	Source string          `json:"source"`
	When   string          `json:"when,omitempty"`
	Steps  []specStep      `json:"steps"`
	Result json.RawMessage `json:"result,omitempty"`
	// Input is the schema of `req` or `event` (protobuf binary).
	Input []byte `json:"input,omitempty"`
	// ResultSchema marks what of the result carries secrets (protobuf
	// binary; nil: nothing); ResultSecret: the whole result does. The
	// console masks the hook's answer by them.
	ResultSchema []byte `json:"result_schema,omitempty"`
	ResultSecret bool   `json:"result_secret,omitempty"`
	// Runtime the program compiled for; unspecified in programs compiled
	// before runtimes existed: Temporal.
	Runtime consolev1.Runtime `json:"runtime,omitempty"`
}

type specStep struct {
	StepPlan

	When        string          `json:"when_expr,omitempty"`
	ForEachExpr string          `json:"for_each_expr,omitempty"`
	Body        *specBody       `json:"body,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	UndoInput   json.RawMessage `json:"undo_input,omitempty"`
	// Output is the schema of the activity's output (protobuf binary).
	Output []byte `json:"output,omitempty"`
	// InputSchema is the activity's input schema with the fields that
	// carry secrets marked (protobuf binary; nil: nothing to hide);
	// InputSecret: the whole input is secret. UndoSchema and UndoSecret
	// the same for the undo input. The console masks the calls by them.
	InputSchema []byte `json:"input_schema,omitempty"`
	InputSecret bool   `json:"input_secret,omitempty"`
	UndoSchema  []byte `json:"undo_schema,omitempty"`
	UndoSecret  bool   `json:"undo_secret,omitempty"`
}

// specBody is a for-each step's sub-flow.
type specBody struct {
	Steps  []specStep      `json:"steps"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Program is a compiled binding or rule: the steps ordered into groups,
// options resolved, expressions compiled. It is built whole — by Compile
// or by json.Unmarshal of a marshaled Program — and not modified after:
// share it freely.
//
// Evaluation is pure and deterministic: expressions read only the Scope,
// nothing reads a clock or randomness, maps are iterated in key order and
// results are JSON with sorted keys, so the same Program and Scope give
// the same bytes on every run — it is safe inside Temporal workflow code.
type Program struct {
	spec spec
	// raw is the spec marshaled, masks what the console hides of a run by
	// it: both derived once here, read on every run (Spec, Masks).
	raw    []byte
	masks  *redact.Program
	env    *cel.Env
	input  *shape
	groups [][]string
	index  map[string]int
	steps  []runStep
	when   cel.Program
	result valueProgram
}

type runStep struct {
	when      cel.Program
	forEach   cel.Program
	input     valueProgram
	undoInput valueProgram
	output    *shape
	body      *Program
}

// valueProgram is a compiled Value: a program, fields, items or a
// literal; unset is the default of its place.
type valueProgram struct {
	kind    ValueKind
	prg     cel.Program
	names   []string
	fields  []valueProgram
	items   []valueProgram
	literal any
}

// newProgram builds the evaluation side of a spec.
func newProgram(s spec) (*Program, error) {
	vars := []string{VarSteps}
	if s.Kind == KindRule {
		vars = append(vars, VarEvent, VarMeta)
	} else {
		vars = append(vars, VarReq)
	}

	// One runtime environment for every frame: names are unique across
	// frames (compile checks), and evaluation checks no types.
	env, err := runtimeEnv(append(vars, frameNames(s.Steps)...))
	if err != nil {
		return nil, err
	}

	b := newShapes()

	in, err := schemaOf(s.Input)
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("bindings: program: %w", err)
	}

	p := &Program{spec: s, raw: raw, masks: redact.ParseProgram(raw), env: env, input: b.of(in, "bp.input")}

	if s.When != "" {
		if p.when, err = program(env, s.When); err != nil {
			return nil, fmt.Errorf("bindings: when: %w", err)
		}
	}

	if err := p.build(b); err != nil {
		return nil, err
	}

	return p, nil
}

// frameNames are the variables steps declare: their names, and for a
// for-each step its item, the item's position and its body's names.
func frameNames(steps []specStep) []string {
	var out []string

	for i := range steps {
		st := &steps[i]
		out = append(out, st.Name)

		if st.ForEach != nil {
			out = append(out, st.ForEach.Item, st.ForEach.Item+"Index")
		}

		if st.Body != nil {
			out = append(out, frameNames(st.Body.Steps)...)
		}
	}

	return out
}

// build compiles the program's steps (and their bodies) and result.
func (p *Program) build(b *shapes) error {
	p.index = map[string]int{}

	for i := range p.spec.Steps {
		st := &p.spec.Steps[i]
		p.index[st.Name] = i

		for len(p.groups) <= st.Group {
			p.groups = append(p.groups, nil)
		}

		p.groups[st.Group] = append(p.groups[st.Group], st.Name)
	}

	var err error
	if p.result, err = p.value(p.spec.Result); err != nil {
		return fmt.Errorf("bindings: result: %w", err)
	}

	for i := range p.spec.Steps {
		run, err := p.step(b, &p.spec.Steps[i])
		if err != nil {
			return fmt.Errorf("bindings: step %s: %w", p.spec.Steps[i].Name, err)
		}

		p.steps = append(p.steps, run)
	}

	return nil
}

func (p *Program) step(b *shapes, st *specStep) (runStep, error) {
	var (
		run runStep
		err error
	)

	if st.When != "" {
		if run.when, err = program(p.env, st.When); err != nil {
			return run, fmt.Errorf("when: %w", err)
		}
	}

	if st.ForEach != nil {
		if run.forEach, err = program(p.env, st.ForEachExpr); err != nil {
			return run, fmt.Errorf("forEach: %w", err)
		}
	}

	if st.Body != nil {
		body := &Program{
			spec: spec{Kind: KindBody, Source: p.spec.Source, Steps: st.Body.Steps, Result: st.Body.Result},
			env:  p.env, input: p.input,
		}
		if err := body.build(b); err != nil {
			return run, err
		}

		run.body = body
	}

	if run.input, err = p.value(st.Input); err != nil {
		return run, fmt.Errorf("input: %w", err)
	}

	if run.undoInput, err = p.value(st.UndoInput); err != nil {
		return run, fmt.Errorf("undo input: %w", err)
	}

	out, err := schemaOf(st.Output)
	if err != nil {
		return run, err
	}

	run.output = b.of(out, "bp.step."+st.Name)

	return run, nil
}

func (p *Program) value(data json.RawMessage) (valueProgram, error) {
	v, err := ParseValue(data)
	if err != nil {
		return valueProgram{}, err
	}

	return p.compileValue(v)
}

func (p *Program) compileValue(v Value) (valueProgram, error) {
	out := valueProgram{kind: v.Kind()}

	switch out.kind {
	case ValueUnset:
	case ValueExpr:
		prg, err := program(p.env, v.Expr())
		if err != nil {
			return out, err
		}

		out.prg = prg
	case ValueObject:
		for _, f := range v.Fields() {
			field, err := p.compileValue(f.Value)
			if err != nil {
				return out, fmt.Errorf("%s: %w", f.Name, err)
			}

			out.names = append(out.names, f.Name)
			out.fields = append(out.fields, field)
		}
	case ValueList:
		for i, item := range v.Items() {
			compiled, err := p.compileValue(item)
			if err != nil {
				return out, fmt.Errorf("[%d]: %w", i, err)
			}

			out.items = append(out.items, compiled)
		}
	case ValueLiteral:
		out.literal = v.Literal()
	}

	return out, nil
}

// MarshalJSON is the program's serialized form: a workflow takes it as
// input and evaluates exactly what was compiled.
func (p *Program) MarshalJSON() ([]byte, error) { return bytes.Clone(p.raw), nil }

// Spec is MarshalJSON's bytes without the copy: read, never written. A
// for-each body (KindBody) has none.
func (p *Program) Spec() []byte { return p.raw }

// Masks is what the console hides of a run of the program: the secrets
// of its input, calls and result. Shared: read only.
func (p *Program) Masks() *redact.Program { return p.masks }

// UnmarshalJSON rebuilds a marshaled program. Only for a new zero Program
// (json.Unmarshal into one): a Program is not modified once built.
func (p *Program) UnmarshalJSON(data []byte) error {
	var s spec
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("bindings: program: %w", err)
	}

	built, err := newProgram(s)
	if err != nil {
		return err
	}

	*p = *built

	return nil
}

// Kind is what the program implements.
func (p *Program) Kind() Kind { return p.spec.Kind }

// Source is the hook or event name.
func (p *Program) Source() string { return p.spec.Source }

// Runtime is what runs the program: Temporal or direct.
func (p *Program) Runtime() consolev1.Runtime {
	if p.spec.Runtime == consolev1.Runtime_RUNTIME_UNSPECIFIED {
		return consolev1.Runtime_RUNTIME_TEMPORAL
	}

	return p.spec.Runtime
}

// Steps are the steps by name.
func (p *Program) Steps() []StepPlan {
	out := make([]StepPlan, 0, len(p.spec.Steps))

	for i := range p.spec.Steps {
		plan := p.spec.Steps[i].StepPlan
		plan.Deps = append([]string(nil), plan.Deps...)
		out = append(out, plan)
	}

	return out
}

// Step is one step by name.
func (p *Program) Step(name string) (StepPlan, bool) {
	i, ok := p.index[name]
	if !ok {
		return StepPlan{}, false
	}

	plan := p.spec.Steps[i].StepPlan
	plan.Deps = append([]string(nil), plan.Deps...)

	return plan, true
}

// Groups are the steps by level: a group's steps depend only on steps of
// lower groups; within a group by name. The executor does not wait for a
// whole group: a step starts once its Deps are done.
func (p *Program) Groups() [][]string {
	out := make([][]string, 0, len(p.groups))
	for _, g := range p.groups {
		out = append(out, append([]string(nil), g...))
	}

	return out
}

// Meta are the CloudEvents attributes of the event a rule runs on, the CEL
// variable `meta` (time as RFC 3339).
type Meta struct {
	ID      string
	Source  string
	Subject string
	Type    string
	Time    time.Time
}

// Scope is what expressions see at one point of a run: the input, the
// outputs of the steps done so far and which steps were skipped. It is a
// value: Bind and Skip return a new Scope and leave the old one as it
// was.
type Scope struct {
	vars map[string]any
}

// Vars is a copy of the scope's variables (values as CEL sees them).
func (s Scope) Vars() map[string]any { return maps.Clone(s.vars) }

// Start is the scope of a binding's run on the hook's input (JSON).
func (p *Program) Start(input []byte) (Scope, error) {
	if p.spec.Kind != KindBinding {
		return Scope{}, errors.New("bindings: Start of a rule program: use StartEvent") //nolint:err113 // misuse
	}

	req, err := decodeJSON(input)
	if err != nil {
		return Scope{}, &EvalError{Place: "input", Err: err}
	}

	return Scope{vars: p.initial(VarReq, convert(p.input, req))}, nil
}

// StartEvent is the scope of a rule's run on an event: its payload (JSON)
// and attributes.
func (p *Program) StartEvent(payload []byte, meta Meta) (Scope, error) {
	if p.spec.Kind != KindRule {
		return Scope{}, errors.New("bindings: StartEvent of a binding program: use Start") //nolint:err113 // misuse
	}

	ev, err := decodeJSON(payload)
	if err != nil {
		return Scope{}, &EvalError{Place: "event", Err: err}
	}

	vars := p.initial(VarEvent, convert(p.input, ev))

	var eventTime string
	if !meta.Time.IsZero() {
		eventTime = meta.Time.UTC().Format(time.RFC3339Nano)
	}

	vars[VarMeta] = map[string]any{
		"id": meta.ID, "source": meta.Source, "subject": meta.Subject, "type": meta.Type, "time": eventTime,
	}

	return Scope{vars: vars}, nil
}

func (p *Program) initial(name string, value any) map[string]any {
	steps := make(map[string]any, len(p.spec.Steps))
	vars := map[string]any{name: value, VarSteps: steps}

	p.declare(vars, steps)

	return vars
}

// declare sets every step of the program to not run yet: its variable {}
// (a for-each step's []), its state not skipped.
func (p *Program) declare(vars, steps map[string]any) {
	for i := range p.spec.Steps {
		st := &p.spec.Steps[i]
		if st.ForEach != nil {
			steps[st.Name] = map[string]any{"skipped": false, "failed": int64(0), "errors": []any{}}
			vars[st.Name] = []any{}

			continue
		}

		steps[st.Name] = map[string]any{"skipped": false}
		vars[st.Name] = map[string]any{}
	}
}

// Bind is s with the output (JSON) of a step that ran.
func (p *Program) Bind(s Scope, step string, output []byte) (Scope, error) {
	i, ok := p.index[step]
	if !ok {
		return Scope{}, fmt.Errorf("%w: %s", errNoStep, step)
	}

	v, err := decodeJSON(output)
	if err != nil {
		return Scope{}, &EvalError{Step: step, Place: "output", Err: err}
	}

	return s.with(step, convert(p.steps[i].output, v), false), nil
}

// Skip is s with a step skipped by its when: its variable is {} and
// steps.<name>.skipped is true. Steps depending on it still run.
func (p *Program) Skip(s Scope, step string) (Scope, error) {
	if _, ok := p.index[step]; !ok {
		return Scope{}, fmt.Errorf("%w: %s", errNoStep, step)
	}

	return s.with(step, map[string]any{}, true), nil
}

func (s Scope) with(step string, value any, skipped bool) Scope {
	vars := maps.Clone(s.vars)
	vars[step] = value

	steps, _ := vars[VarSteps].(map[string]any)
	steps = maps.Clone(steps)
	steps[step] = map[string]any{"skipped": skipped}
	vars[VarSteps] = steps

	return Scope{vars: vars}
}

// Match is a rule's when on the scope of StartEvent: false — ack the event
// without a run. A binding, or a rule without when, matches.
func (p *Program) Match(s Scope) (bool, error) {
	if p.when == nil {
		return true, nil
	}

	return evalBool(p.when, s.vars, "", "when")
}

// When is the step's when on s; a step without one runs.
func (p *Program) When(step string, s Scope) (bool, error) {
	i, ok := p.index[step]
	if !ok {
		return false, fmt.Errorf("%w: %s", errNoStep, step)
	}

	if p.steps[i].when == nil {
		return true, nil
	}

	return evalBool(p.steps[i].when, s.vars, step, "when")
}

// Input is the step's activity input (JSON) on s.
func (p *Program) Input(step string, s Scope) ([]byte, error) {
	i, ok := p.index[step]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNoStep, step)
	}

	return evalValue(p.steps[i].input, s.vars, step, "input", map[string]any{})
}

// UndoInput is the input (JSON) of the step's undo activity on s: its
// undo input, else the step's own output.
func (p *Program) UndoInput(step string, s Scope) ([]byte, error) {
	i, ok := p.index[step]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNoStep, step)
	}

	return evalValue(p.steps[i].undoInput, s.vars, step, "undo input", s.vars[step])
}

// Result is a binding's output (JSON) on s: the hook's answer.
func (p *Program) Result(s Scope) ([]byte, error) {
	return evalValue(p.result, s.vars, "", "result", map[string]any{})
}

// Eval evaluates src in the program's environment over vars (a Scope's
// Vars, possibly changed): the console's playground, tests. src is
// compiled on every call.
func (p *Program) Eval(src string, vars map[string]any) (any, error) {
	prg, err := program(p.env, src)
	if err != nil {
		return nil, err
	}

	return eval(prg, vars)
}

// EvalError is an expression that failed on the values of a run:
// "transform failed at step render (input.to): no such key: x".
type EvalError struct {
	// Step is empty for the result and a rule's when.
	Step string
	// Place: when, input, input.<field>, undo input, output, result, event.
	Place string
	Err   error
}

func (e *EvalError) Error() string {
	if e.Step == "" {
		return fmt.Sprintf("transform failed at %s: %v", e.Place, e.Err)
	}

	return fmt.Sprintf("transform failed at step %s (%s): %v", e.Step, e.Place, e.Err)
}

func (e *EvalError) Unwrap() error { return e.Err }

func evalBool(prg cel.Program, vars map[string]any, step, place string) (bool, error) {
	out, err := eval(prg, vars)
	if err != nil {
		return false, &EvalError{Step: step, Place: place, Err: err}
	}

	b, ok := out.(bool)
	if !ok {
		return false, &EvalError{Step: step, Place: place, Err: fmt.Errorf("%w: got %T", errNotBool, out)}
	}

	return b, nil
}

var errNotBool = errors.New("not a bool")

func evalValue(v valueProgram, vars map[string]any, step, place string, zero any) ([]byte, error) {
	out := zero

	if v.kind != ValueUnset {
		value, err := v.eval(vars, step, place)
		if err != nil {
			return nil, err
		}

		out = value
	}

	data, err := marshal(out)
	if err != nil {
		return nil, &EvalError{Step: step, Place: place, Err: err}
	}

	return data, nil
}

// eval is the value on vars; a failing expression is an *EvalError at its
// place ("input.to", "input.items[2]").
func (v valueProgram) eval(vars map[string]any, step, place string) (any, error) {
	switch v.kind {
	case ValueExpr:
		value, err := eval(v.prg, vars)
		if err != nil {
			return nil, &EvalError{Step: step, Place: place, Err: err}
		}

		return value, nil
	case ValueObject:
		obj := make(map[string]any, len(v.fields))

		for i, f := range v.fields {
			value, err := f.eval(vars, step, place+"."+v.names[i])
			if err != nil {
				return nil, err
			}

			obj[v.names[i]] = value
		}

		return obj, nil
	case ValueList:
		list := make([]any, 0, len(v.items))

		for i, item := range v.items {
			value, err := item.eval(vars, step, place+"["+strconv.Itoa(i)+"]")
			if err != nil {
				return nil, err
			}

			list = append(list, value)
		}

		return list, nil
	default:
		return v.literal, nil
	}
}

// marshal is deterministic JSON: sorted keys, no HTML escaping.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
