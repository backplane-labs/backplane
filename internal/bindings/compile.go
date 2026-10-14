package bindings

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"google.golang.org/protobuf/proto"

	sp "github.com/gopherex/schemapb/go/schemapb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// Platform defaults of a step, below the binding's own options and the
// activity's manifest defaults.
const (
	DefaultStartToClose    = 30 * time.Second
	DefaultAttempts        = 3
	DefaultInitialInterval = time.Second
	DefaultMaxInterval     = 30 * time.Second
	DefaultBackoff         = 2.0
)

// InvalidError is a definition that does not compile: the violations say
// why.
type InvalidError struct{ Violations []Violation }

func (e *InvalidError) Error() string {
	if len(e.Violations) == 0 {
		return "bindings: invalid definition"
	}

	msg := "bindings: invalid definition: " + e.Violations[0].String()
	if n := len(e.Violations) - 1; n > 0 {
		msg += fmt.Sprintf(" (and %d more)", n)
	}

	return msg
}

// Analysis is a definition as the compiler sees it: its violations, the
// order of its steps, the type of every value and what every expression
// reads. Parts that do not compile are left out.
type Analysis struct {
	Violations []Violation
	// Steps by name.
	Steps []StepAnalysis
	// Types by path.
	Types []ValueType
	// References by path, then position.
	References []Reference
	// Secrets are the paths of the value nodes that carry secrets: fields
	// marked secret by a schema, or computed from one. Empty until the
	// definition is valid.
	Secrets []string
	// Runtime the definition compiled for: its own, else the catalog's
	// default, else Temporal.
	Runtime consolev1.Runtime
}

// StepAnalysis is where a step stands in the order of execution.
type StepAnalysis struct {
	Name string
	// Level is 0 for a step that depends on nothing, else one above its
	// highest dependency; -1 in a cycle or with an invalid name.
	Level int
	// Data, After and When are the steps its input, `after` and `when`
	// make it depend on; Undo the steps its undo input reads.
	Data, After, When, Undo []string
	// Parent is the for-each step whose body the step is in; "" at the top.
	Parent string
	// ItemType is the CEL type of a for-each step's item; "" without.
	ItemType string
}

// ValueType is the CEL type of a value node.
type ValueType struct {
	Path string
	Type string
}

// Reference is one read of a variable by the expression at Path.
type Reference struct {
	Path     string
	Variable string
	Fields   []string
	Expr     Range
}

// ValidateBinding checks b against the catalog: every violation, empty
// when valid. The error is a failure to validate, not a violation.
func ValidateBinding(b Binding, cat Catalog) ([]Violation, error) {
	_, a, err := compileBinding(b, cat)

	return a.Violations, err
}

// ValidateRule checks r against the catalog, as ValidateBinding.
func ValidateRule(r Rule, cat Catalog) ([]Violation, error) {
	_, a, err := compileRule(r, cat)

	return a.Violations, err
}

// AnalyzeBinding checks b against the catalog and describes it.
func AnalyzeBinding(b Binding, cat Catalog) (Analysis, error) {
	_, a, err := compileBinding(b, cat)

	return a, err
}

// AnalyzeRule checks r against the catalog and describes it.
func AnalyzeRule(r Rule, cat Catalog) (Analysis, error) {
	_, a, err := compileRule(r, cat)

	return a, err
}

// CompileBinding checks b against the catalog and compiles it; an invalid
// definition is an *InvalidError.
func CompileBinding(b Binding, cat Catalog) (*Program, error) {
	p, a, err := compileBinding(b, cat)
	if err != nil {
		return nil, err
	}

	if len(a.Violations) > 0 {
		return nil, &InvalidError{Violations: a.Violations}
	}

	return p, nil
}

// CompileRule checks r against the catalog and compiles it; an invalid
// definition is an *InvalidError.
func CompileRule(r Rule, cat Catalog) (*Program, error) {
	p, a, err := compileRule(r, cat)
	if err != nil {
		return nil, err
	}

	if len(a.Violations) > 0 {
		return nil, &InvalidError{Violations: a.Violations}
	}

	return p, nil
}

// Paths of the definition's own places.
const (
	pathHook   = "/hook"
	pathEvent  = "/event"
	pathWhen   = "/when"
	pathResult = "/result"
	pathSteps  = "/steps"
)

func compileBinding(b Binding, cat Catalog) (*Program, Analysis, error) {
	c := newCompiler(KindBinding, cat)
	c.runtime(b.Runtime)

	var in, out *sp.Schema

	switch hook, ok := cat.Hook(b.Hook); {
	case !IsFullName(b.Hook):
		c.violate(pathHook, CodeInvalidName, fmt.Sprintf("%q is not a hook name <service>.<Hook>", b.Hook))
	case !ok:
		c.violate(pathHook, CodeUnknownHook, "no manifest declares hook "+b.Hook)
	default:
		in, out = hook.GetInput(), hook.GetOutput()
	}

	c.input = c.shapes.of(in, "bp.req")
	resultShape := c.shapes.of(out, "bp.result")
	c.resultShape, c.resultDeclared = resultShape, out

	return c.compile(source{kind: KindBinding, name: b.Hook, result: b.Result}, b.Steps, in, resultShape)
}

func compileRule(r Rule, cat Catalog) (*Program, Analysis, error) {
	c := newCompiler(KindRule, cat)
	c.runtime(r.Runtime)

	var in *sp.Schema

	switch event, ok := cat.Event(r.Event); {
	case !IsFullName(r.Event):
		c.violate(pathEvent, CodeInvalidName, fmt.Sprintf("%q is not an event name <service>.<Event>", r.Event))
	case !ok:
		c.violate(pathEvent, CodeUnknownEvent, "no manifest declares event "+r.Event)
	default:
		in = event.GetSchema()
	}

	c.input = c.shapes.of(in, "bp.event")

	// A binding without steps may still answer (its result); a rule without
	// steps does nothing.
	if len(r.Steps) == 0 {
		c.violate(pathSteps, CodeMissingField, "a rule needs at least one step")
	}

	return c.compile(source{kind: KindRule, name: r.Event, when: r.When}, r.Steps, in, nil)
}

// source is what a definition runs on: a hook with its result, or an
// event with its filter.
type source struct {
	kind   Kind
	name   string
	when   string
	result Value
}

type compiler struct {
	kind     Kind
	cat      Catalog
	shapes   *shapes
	input    *shape
	env      *cel.Env
	analysis Analysis
	// resultShape and resultDeclared are a binding's result shape and the
	// hook's output schema it was built from, for the secrets pass.
	resultShape    *shape
	resultDeclared *sp.Schema
	// taints memoizes tainted.
	taints map[*shape]bool
	// rt is the runtime the definition compiles for.
	rt consolev1.Runtime
}

// runtime resolves the definition's runtime: its own, the catalog's
// default, else Temporal.
func (c *compiler) runtime(own consolev1.Runtime) {
	c.rt = own
	if c.rt == consolev1.Runtime_RUNTIME_UNSPECIFIED {
		c.rt = DefaultsOf(c.cat).Runtime
	}

	if c.rt == consolev1.Runtime_RUNTIME_UNSPECIFIED {
		c.rt = consolev1.Runtime_RUNTIME_TEMPORAL
	}

	c.analysis.Runtime = c.rt
}

// runtimeChecks reports what the step uses that the runtime does not run:
// on direct, no undo, retry options, heartbeat or child workflow.
func (c *compiler) runtimeChecks(d *draft) {
	if c.rt != consolev1.Runtime_RUNTIME_DIRECT {
		return
	}

	const msg = "the direct runtime does not run "

	if d.Undo != "" {
		c.violate(d.path+"/undo", CodeRuntimeUnsupported, msg+"undo: choose the temporal runtime")
	}

	if !d.Retry.IsZero() {
		c.violate(d.path+"/retry", CodeRuntimeUnsupported,
			msg+"retries of a step: the run as a whole is retried by its caller or by event redelivery")
	}

	if d.Heartbeat != 0 {
		c.violate(d.path+"/heartbeat", CodeRuntimeUnsupported, msg+"heartbeats: start_to_close bounds the call")
	}

	if d.act != nil && d.act.GetKind() == backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW {
		c.violate(d.path+"/activity", CodeRuntimeUnsupported,
			msg+"workflows as steps: "+d.Activity+" is a workflow; choose the temporal runtime")
	}
}

func newCompiler(kind Kind, cat Catalog) *compiler {
	return &compiler{kind: kind, cat: cat, shapes: newShapes()}
}

func (c *compiler) violate(path, code, msg string) {
	c.violateAt(path, code, msg, Range{})
}

func (c *compiler) violateAt(path, code, msg string, at Range) {
	c.analysis.Violations = append(c.analysis.Violations, Violation{Path: path, Code: code, Message: msg, Expr: at})
}

// draft is a step while it compiles.
type draft struct {
	Step

	path     string
	ok       bool // name valid: a CEL variable
	act      *backplanev1.Activity
	undo     *backplanev1.Activity
	out      *sp.Schema
	outShape *shape
	data     []string
	after    []string
	when     []string
	undoRefs []string
	deps     []string
	level    int
	// outer are the steps of enclosing frames the step's expressions read:
	// dependencies of the for-each step whose body it is in.
	outer []string
	// itemType is the CEL type of a for-each step's item.
	itemType string
	// body and bodyGroups are a for-each step's sub-flow.
	body       []*draft
	bodyGroups [][]string
	// What the step's expressions read, for the secrets pass.
	inputReads, undoReads, forEachReads, resultReads []read
	// Effective schemas of the input and undo input: the activity's with
	// the fields that carry secrets marked (nil: nothing to hide); secret
	// when the whole value is.
	inputSchema, undoSchema []byte
	inputSecret, undoSecret bool
}

func (c *compiler) compile(src source, steps []Step, in *sp.Schema, result *shape) (*Program, Analysis, error) {
	drafts := c.steps("", steps)
	top := c.topFrame()
	c.declare(top, drafts)

	if err := c.frameEnv(top); err != nil {
		return nil, Analysis{}, err
	}

	for _, d := range drafts {
		if err := c.expressions(d, top); err != nil {
			return nil, Analysis{}, err
		}
	}

	c.env = top.env

	if src.kind == KindRule && src.when != "" {
		c.ruleWhen(src.when, top.names)
	}

	var resultReads []read
	if src.kind == KindBinding {
		resultReads = c.value(pathResult, src.result, result)
	}

	groups := c.order(drafts)
	c.undoRefs(drafts)
	c.describe(drafts, "")

	if len(c.analysis.Violations) > 0 {
		return nil, c.analysis, nil
	}

	s := spec{
		Kind: src.kind, Source: src.name, When: src.when, Result: src.result.JSON(), Input: schemaBytes(in),
		Runtime: c.rt,
	}
	s.ResultSchema, s.ResultSecret = c.secrets(drafts, groups, src, resultReads, result)
	s.Steps = c.specSteps(drafts, groups)

	p, err := newProgram(s)
	if err != nil {
		return nil, Analysis{}, err
	}

	return p, c.analysis, nil
}

// specSteps are the steps as a program runs them, a for-each body nested.
func (c *compiler) specSteps(drafts []*draft, groups [][]string) []specStep {
	out := make([]specStep, 0, len(drafts))

	for _, d := range drafts {
		st := specStep{
			StepPlan: c.plan(d, groups), When: d.When, ForEachExpr: d.ForEach, Input: d.Input.JSON(),
			UndoInput: d.UndoInput.JSON(), Output: schemaBytes(d.out),
			InputSchema: d.inputSchema, InputSecret: d.inputSecret, UndoSchema: d.undoSchema, UndoSecret: d.undoSecret,
		}
		if d.body != nil {
			st.Body = &specBody{Steps: c.specSteps(d.body, d.bodyGroups), Result: d.Result.JSON()}
		}

		out = append(out, st)
	}

	return out
}

// ruleWhen compiles a rule's filter: a bool over event and meta only.
func (c *compiler) ruleWhen(src string, names map[string]bool) {
	t, rs, ok := c.expr(pathWhen, src)
	if ok {
		c.boolean(pathWhen, t)
	}

	for _, r := range rs {
		if r.variable == VarSteps || names[r.variable] {
			c.violateAt(pathWhen, CodeUnknownStep, "a rule's when sees only event and meta, not steps", r.whole)

			return
		}
	}
}

// steps checks what does not need CEL: names, activities, options. prefix
// is the path of the for-each step whose body they are, "" at the top.
func (c *compiler) steps(prefix string, steps []Step) []*draft {
	sorted := sortSteps(slices.Clone(steps))
	out := make([]*draft, 0, len(sorted))

	for i := range sorted {
		s := &sorted[i]
		d := &draft{Step: *s, path: prefix + Pointer("steps", s.Name), level: -1}
		out = append(out, d)

		switch {
		case !IsIdent(s.Name):
			c.violate(d.path, CodeInvalidName, fmt.Sprintf("%q is not an identifier [A-Za-z_][A-Za-z0-9_]*", s.Name))
		case reserved[s.Name]:
			c.violate(d.path, CodeReservedName, fmt.Sprintf("%q is reserved", s.Name))
		default:
			d.ok = true
		}

		// A for-each step whose body is steps calls no activity itself.
		if s.ForEach == "" || len(s.Steps) == 0 || s.Activity != "" {
			d.act = c.activity(d.path+"/activity", s.Activity)
		}

		if d.act != nil {
			d.out = d.act.GetOutput()
		}

		if s.Undo != "" {
			d.undo = c.activity(d.path+"/undo", s.Undo)
		} else if !s.UndoInput.IsZero() {
			c.violate(d.path+"/undoInput", CodeInvalidOption, "undo input without an undo activity")
		}

		c.options(d)
		c.forEachOptions(d)
		c.runtimeChecks(d)
	}

	return out
}

// forEachOptions checks the options of a for-each step, and that a step
// without forEach sets none of them.
//
//nolint:cyclop // one check per option
func (c *compiler) forEachOptions(d *draft) {
	if d.ForEach == "" {
		for _, o := range []struct {
			set  bool
			path string
		}{
			{d.As != "", "/as"},
			{d.Concurrency != 0, "/concurrency"},
			{d.OnError != "", "/onError"},
			{d.MaxItems != 0, "/maxItems"},
			{len(d.Steps) > 0, "/steps"},
			{!d.Result.IsZero(), "/result"},
		} {
			if o.set {
				c.violate(d.path+o.path, CodeInvalidOption, "only a step with forEach takes this option")
			}
		}

		return
	}

	if len(d.Steps) > 0 && d.Activity != "" {
		c.violate(d.path+"/steps", CodeInvalidOption, "a for-each body is an activity or steps, not both")
	}

	if len(d.Steps) > 0 && (d.Undo != "" || !d.Input.IsZero()) {
		c.violate(d.path+"/steps", CodeInvalidOption, "a body of steps takes no input or undo: its steps have their own")
	}

	if len(d.Steps) == 0 && !d.Result.IsZero() {
		c.violate(d.path+"/result", CodeInvalidOption, "a result needs a body of steps")
	}

	if d.Concurrency < 0 || d.Concurrency > MaxConcurrency {
		c.violate(d.path+"/concurrency", CodeInvalidOption, fmt.Sprintf("must be between 1 and %d", MaxConcurrency))
	}

	if d.MaxItems < 0 || d.MaxItems > MaxItemsLimit {
		c.violate(d.path+"/maxItems", CodeInvalidOption, fmt.Sprintf("must be between 1 and %d", MaxItemsLimit))
	}

	if d.OnError != "" && d.OnError != OnErrorFail && d.OnError != OnErrorContinue {
		c.violate(d.path+"/onError", CodeInvalidOption,
			fmt.Sprintf("%q is not %q or %q", d.OnError, OnErrorFail, OnErrorContinue))
	}

	if item := d.ItemVar(); !IsIdent(item) || reserved[item] {
		c.violate(d.path+"/as", CodeInvalidName, fmt.Sprintf("%q is not a free identifier", item))
	}
}

func (c *compiler) activity(path, name string) *backplanev1.Activity {
	if !IsFullName(name) {
		c.violate(path, CodeInvalidName, fmt.Sprintf("%q is not an activity name <service>.<Activity>", name))

		return nil
	}

	a, ok := c.cat.Activity(name)
	if !ok {
		c.violate(path, CodeUnknownActivity, "no manifest declares activity "+name)

		return nil
	}

	return a
}

func (c *compiler) options(d *draft) {
	r := d.Retry

	if r.Attempts < 0 {
		c.violate(d.path+"/retry/attempts", CodeInvalidOption, "attempts must not be negative")
	}

	if r.InitialInterval < 0 {
		c.violate(d.path+"/retry/initialInterval", CodeInvalidOption, "must not be negative")
	}

	if r.MaxInterval < 0 {
		c.violate(d.path+"/retry/maxInterval", CodeInvalidOption, "must not be negative")
	}

	if r.MaxInterval > 0 && r.InitialInterval > r.MaxInterval {
		c.violate(d.path+"/retry/maxInterval", CodeInvalidOption, "must not be below the initial interval")
	}

	if r.Backoff != 0 && r.Backoff < 1 {
		c.violate(d.path+"/retry/backoff", CodeInvalidOption, "must be at least 1")
	}

	if d.StartToClose < 0 {
		c.violate(d.path+"/startToClose", CodeInvalidOption, "must not be negative")
	}

	if d.Heartbeat < 0 {
		c.violate(d.path+"/heartbeat", CodeInvalidOption, "must not be negative")
	}
}

func appendNew(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}

	return append(list, s)
}

// value compiles a Value against the shape it must have (nil: unknown, no
// check) and returns what its expressions read. Unset is {} of want.
func (c *compiler) value(path string, v Value, want *shape) []read {
	switch v.Kind() {
	case ValueUnset:
		c.required(path, want, nil)

		return nil
	case ValueExpr:
		t, reads, ok := c.expr(path, v.Expr())
		if ok && want != nil {
			if msg := c.assignable(want, t); msg != "" {
				c.violate(path, CodeTypeMismatch, msg)
			}
		}

		return reads
	case ValueObject:
		return c.object(path, v, want)
	case ValueList:
		return c.list(path, v, want)
	default:
		c.literal(path, v.Literal(), want)

		return nil
	}
}

func (c *compiler) object(path string, v Value, want *shape) []read {
	c.typed(path, "object")

	fields := v.Fields()
	reads := make([]read, 0, len(fields))
	set := map[string]bool{}

	for _, f := range fields {
		set[f.Name] = true
	}

	for _, f := range fields {
		fpath := path + "/" + escapePointer(f.Name)

		var fieldWant *shape

		switch {
		case want == nil:
		case want.kind == kindObject:
			if fieldWant = want.fields[f.Name]; fieldWant == nil {
				c.violate(fpath, CodeUnknownField, "the schema declares no field "+f.Name)
			}
		case want.kind == kindMap:
			fieldWant = want.elem
		default:
		}

		reads = append(reads, c.value(fpath, f.Value, fieldWant)...)
	}

	if want != nil && want.kind != kindObject && want.kind != kindMap && want.kind != kindDyn {
		c.violate(path, CodeTypeMismatch, fmt.Sprintf("expected %s, got an object", want))
	}

	c.required(path, want, set)

	return reads
}

// required reports the required fields of an object shape not in set.
func (c *compiler) required(path string, want *shape, set map[string]bool) {
	if want == nil || want.kind != kindObject {
		return
	}

	for _, r := range want.required {
		if !set[r] {
			c.violate(path, CodeMissingField, fmt.Sprintf("required field %s is not set", r))
		}
	}
}

func (c *compiler) list(path string, v Value, want *shape) []read {
	c.typed(path, "list")

	var elem *shape

	switch {
	case want == nil, want.kind == kindDyn:
	case want.kind == kindList:
		elem = want.elem
	default:
		c.violate(path, CodeTypeMismatch, fmt.Sprintf("expected %s, got a list", want))
	}

	items := v.Items()
	reads := make([]read, 0, len(items))

	for i, item := range items {
		reads = append(reads, c.value(path+"/"+strconv.Itoa(i), item, elem)...)
	}

	return reads
}

// Literal kinds as the analysis names them.
const (
	typeInt    = "int"
	typeDouble = "double"
	typeBool   = "bool"
	typeNull   = "null"
)

// literal checks a number, bool or null against want. A string is an
// expression, never a literal: a literal string is written as CEL
// ("'text'").
func (c *compiler) literal(path string, v any, want *shape) {
	got := literalType(v)
	c.typed(path, got)

	if got == typeNull || want == nil {
		return
	}

	if want.kind == kindString {
		c.violate(path, CodeTypeMismatch, fmt.Sprintf("expected string, got %s (a string is an expression: "+
			"write a literal as '%v' in quotes)", got, v))

		return
	}

	if !literalFits(want.kind, got, v) {
		c.violate(path, CodeTypeMismatch, fmt.Sprintf("expected %s, got %s", want, got))
	}
}

func literalType(v any) string {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return typeInt
		}

		return typeDouble
	case bool:
		return typeBool
	default:
		return typeNull
	}
}

func literalFits(want kind, got string, v any) bool {
	switch want {
	case kindDyn:
		return true
	case kindDouble:
		return got != typeBool
	case kindInt:
		return got == typeInt
	case kindUint:
		n, isNum := v.(float64)

		return got == typeInt && isNum && n >= 0
	case kindBool:
		return got == typeBool
	default:
		return false
	}
}

func (c *compiler) typed(path, t string) {
	c.analysis.Types = append(c.analysis.Types, ValueType{Path: path, Type: t})
}

// expr compiles src in the typed environment: its output type and what it
// reads (also when it does not type-check); a failure is a violation.
func (c *compiler) expr(path, src string) (*types.Type, []read, bool) {
	if strings.TrimSpace(src) == "" {
		c.violate(path, CodeCEL, "empty expression")

		return nil, nil, false
	}

	parsed, iss := c.env.Parse(src)
	if iss.Err() != nil {
		for _, e := range iss.Errors() {
			c.violateAt(path, CodeCEL, e.Message, issueRange(src, nil, e))
		}

		return nil, nil, false
	}

	native := parsed.NativeRep()
	reads := readsOf(src, native)

	for i := range reads {
		reads[i].path = path
	}

	if len(reads) == 1 {
		reads[0].bare = isWhole(src, reads[0].whole)
	}

	for _, r := range reads {
		c.analysis.References = append(c.analysis.References, Reference{
			Path: path, Variable: r.variable, Fields: slices.Clone(r.fields), Expr: r.whole,
		})
	}

	checked, iss := c.env.Check(parsed)
	if iss.Err() != nil {
		for _, e := range iss.Errors() {
			c.violateAt(path, CodeCEL, e.Message, issueRange(src, native, e))
		}

		return nil, reads, false
	}

	if est, err := c.env.EstimateCost(checked, sizeEstimator{}); err == nil && est.Max > costLimit {
		c.violate(path, CodeCost, fmt.Sprintf("may cost up to %s evaluation units, over the limit of %d "+
			"(assuming collections of up to %d items)", costText(est.Max), costLimit, maxCollection))
	}

	c.typed(path, typeName(checked.OutputType()))

	return checked.OutputType(), reads, true
}

// isWhole reports whether r spans all of src but surrounding space.
func isWhole(src string, r Range) bool {
	runes := []rune(src)
	if r.Start < 0 || r.End > len(runes) || r.Start >= r.End {
		return false
	}

	return strings.TrimSpace(src) == string(runes[r.Start:r.End])
}

func costText(n uint64) string {
	if n == math.MaxUint64 {
		return "unbounded"
	}

	return strconv.FormatUint(n, 10)
}

// typeName is a CEL type as the editor shows it: objects of the schemas
// are "object".
func typeName(t *types.Type) string {
	switch t.Kind() {
	case types.StructKind:
		return "object"
	case types.ListKind:
		return "list(" + typeName(t.Parameters()[0]) + ")"
	case types.MapKind:
		return "map(" + typeName(t.Parameters()[0]) + ", " + typeName(t.Parameters()[1]) + ")"
	case types.NullTypeKind:
		return typeNull
	case types.OpaqueKind:
		if t.TypeName() == "optional_type" && len(t.Parameters()) == 1 {
			return "optional(" + typeName(t.Parameters()[0]) + ")"
		}
	default:
	}

	return t.String()
}

func (c *compiler) boolean(path string, t *types.Type) {
	switch t.Kind() {
	case types.BoolKind, types.DynKind, types.AnyKind, types.TypeParamKind:
	default:
		c.violate(path, CodeTypeMismatch, "expected bool, got "+typeName(t))
	}
}

// assignable is "" when a value of type t fits the shape as JSON: numbers
// widen (an int fits a double field, not the reverse), a map fits an
// object, timestamps and durations fit strings (their JSON form); dyn fits
// anything. Otherwise the message says why.
//
//nolint:cyclop // one case per kind
func (c *compiler) assignable(want *shape, t *types.Type) string {
	got := t.Kind()

	switch got {
	case types.DynKind, types.AnyKind, types.TypeParamKind, types.ErrorKind, types.NullTypeKind:
		return ""
	case types.OpaqueKind:
		if t.TypeName() == "optional_type" && len(t.Parameters()) == 1 {
			return c.assignable(want, t.Parameters()[0])
		}
	default:
	}

	mismatch := fmt.Sprintf("expected %s, got %s", want, typeName(t))

	switch want.kind {
	case kindDyn:
		return ""
	case kindInt, kindUint:
		if got == types.IntKind || got == types.UintKind {
			return ""
		}
	case kindDouble:
		if got == types.IntKind || got == types.UintKind || got == types.DoubleKind {
			return ""
		}
	case kindBool:
		if got == types.BoolKind {
			return ""
		}
	case kindString:
		if got == types.StringKind || got == types.TimestampKind || got == types.DurationKind {
			return ""
		}
	case kindBytes:
		if got == types.BytesKind {
			return ""
		}
	case kindList:
		if got == types.ListKind {
			return c.assignable(want.elem, t.Parameters()[0])
		}
	case kindMap:
		if got == types.MapKind {
			return c.assignable(want.elem, t.Parameters()[1])
		}

		if got == types.StructKind {
			return ""
		}
	case kindObject:
		return c.objectType(want, t, mismatch)
	}

	return mismatch
}

// objectType checks a value against an object shape field by field when
// both are known objects.
func (c *compiler) objectType(want *shape, t *types.Type, mismatch string) string {
	switch t.Kind() {
	case types.MapKind:
		return ""
	case types.StructKind:
		have := c.shapes.objects[t.TypeName()]
		if have == nil || have == want {
			return ""
		}

		for _, name := range sortedKeys(have.fields) {
			if f := want.fields[name]; f != nil {
				if msg := c.assignable(f, have.fields[name].celType()); msg != "" {
					return name + ": " + msg
				}
			}
		}

		return ""
	default:
		return mismatch
	}
}

// order sets the steps' levels from their dependencies (Kahn's algorithm,
// by name within a level) and reports a cycle; the groups are the steps
// by level.
func (c *compiler) order(drafts []*draft) [][]string {
	pending := 0

	for _, d := range drafts {
		if d.ok {
			pending++
		}
	}

	level := map[string]int{}

	var groups [][]string

	for len(level) < pending {
		var ready []*draft

		for _, d := range drafts {
			if _, done := level[d.Name]; done || !d.ok {
				continue
			}

			if allIn(d.deps, level) {
				ready = append(ready, d)
			}
		}

		if len(ready) == 0 {
			c.cycle(drafts, level)

			return groups
		}

		var names []string

		for _, d := range ready {
			level[d.Name] = len(groups)
			d.level = len(groups)
			names = append(names, d.Name)
		}

		groups = append(groups, names)
	}

	return groups
}

func allIn(deps []string, level map[string]int) bool {
	for _, dep := range deps {
		if _, ok := level[dep]; !ok {
			return false
		}
	}

	return true
}

// cycle reports one cycle among the steps not ordered.
func (c *compiler) cycle(drafts []*draft, level map[string]int) {
	byName := map[string]*draft{}

	var start *draft

	for _, d := range drafts {
		if _, done := level[d.Name]; !done && d.ok {
			byName[d.Name] = d
			if start == nil {
				start = d
			}
		}
	}

	var path []string

	seen := map[string]int{}

	// Every step left has a dependency left: walking them must return.
	for cur := start; cur != nil; {
		if at, ok := seen[cur.Name]; ok {
			loop := append(path[at:], cur.Name) //nolint:gocritic // a new slice for the message
			c.violate(byName[loop[0]].path, CodeCycle, "cycle: "+strings.Join(loop, " -> "))

			return
		}

		seen[cur.Name] = len(path)
		path = append(path, cur.Name)

		var next *draft

		for _, dep := range cur.deps {
			if byName[dep] != nil {
				next = byName[dep]

				break
			}
		}

		cur = next
	}
}

// undoRefs checks that an undo input reads only the step itself and what
// ran before it: nothing else is certain to have run when it compensates.
func (c *compiler) undoRefs(drafts []*draft) {
	byName := map[string]*draft{}

	for _, d := range drafts {
		if d.ok {
			byName[d.Name] = d
		}
	}

	for _, d := range drafts {
		if len(d.undoRefs) == 0 {
			continue
		}

		before := ancestors(d, byName)

		for _, r := range d.undoRefs {
			if r != d.Name && !before[r] {
				c.violate(d.path+"/undoInput", CodeUndoReference,
					fmt.Sprintf("step %s may not have run when %s is undone: add it to after", r, d.Name))
			}
		}
	}
}

func ancestors(d *draft, byName map[string]*draft) map[string]bool {
	out := map[string]bool{}

	stack := append([]string(nil), d.deps...)

	for len(stack) > 0 {
		name := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if out[name] || byName[name] == nil {
			continue
		}

		out[name] = true
		stack = append(stack, byName[name].deps...)
	}

	return out
}

// describe fills the analysis' steps (a for-each body's after its step)
// and orders its lists.
func (c *compiler) describe(drafts []*draft, parent string) {
	for _, d := range drafts {
		c.analysis.Steps = append(c.analysis.Steps, StepAnalysis{
			Name: d.Name, Level: d.level, Data: d.data, After: d.after, When: d.when, Undo: d.undoRefs,
			Parent: parent, ItemType: d.itemType,
		})
		if d.body != nil {
			id := d.Name
			if parent != "" {
				id = parent + "/" + d.Name
			}

			c.describe(d.body, id)
		}
	}

	if parent != "" {
		return
	}

	slices.SortStableFunc(c.analysis.Types, func(a, b ValueType) int { return strings.Compare(a.Path, b.Path) })
	slices.SortStableFunc(c.analysis.References, func(a, b Reference) int {
		if n := strings.Compare(a.Path, b.Path); n != 0 {
			return n
		}

		return a.Expr.Start - b.Expr.Start
	})
}

// plan resolves the step's options: the binding's, else the activity's
// manifest defaults, else the platform's.
func (c *compiler) plan(d *draft, groups [][]string) StepPlan {
	p := StepPlan{
		Name: d.Name, Activity: d.Activity, Kind: d.act.GetKind(), Deps: slices.Clone(d.deps),
		Conditional: d.When != "", Undo: d.Undo,
		Retry: d.Retry, StartToClose: d.StartToClose, Heartbeat: d.Heartbeat,
	}
	if d.ForEach != "" {
		p.ForEach = &ForEachPlan{
			Item: d.ItemVar(), Concurrency: cmp.Or(d.Concurrency, DefaultConcurrency),
			Continue: d.OnError == OnErrorContinue, MaxItems: cmp.Or(d.MaxItems, DefaultMaxItems), Steps: d.body != nil,
		}
	}

	p.Service, _ = SplitName(d.Activity)

	for i, g := range groups {
		if slices.Contains(g, d.Name) {
			p.Group = i
		}
	}

	if d.undo != nil {
		p.UndoService, _ = SplitName(d.Undo)
		p.UndoKind = d.undo.GetKind()
	}

	if p.StartToClose == 0 {
		p.StartToClose = durationOf(d.act.GetStartToClose())
	}

	if p.StartToClose == 0 {
		p.StartToClose = DefaultStartToClose
	}

	if p.Heartbeat == 0 {
		p.Heartbeat = durationOf(d.act.GetHeartbeat())
	}

	if p.Retry.Attempts == 0 {
		p.Retry.Attempts = int(d.act.GetRetry().GetAttempts())
	}

	if p.Retry.Attempts == 0 {
		p.Retry.Attempts = DefaultAttempts
	}

	if p.Retry.InitialInterval == 0 {
		p.Retry.InitialInterval = DefaultInitialInterval
	}

	if p.Retry.MaxInterval == 0 {
		p.Retry.MaxInterval = max(DefaultMaxInterval, p.Retry.InitialInterval)
	}

	if p.Retry.Backoff == 0 {
		p.Retry.Backoff = DefaultBackoff
	}

	return p
}

func schemaBytes(s *sp.Schema) []byte {
	if s == nil {
		return nil
	}

	out, err := proto.MarshalOptions{Deterministic: true}.Marshal(s)
	if err != nil {
		return nil
	}

	return out
}

func schemaOf(b []byte) (*sp.Schema, error) {
	if len(b) == 0 {
		return nil, nil //nolint:nilnil // no schema
	}

	var s sp.Schema
	if err := proto.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("bindings: schema: %w", err)
	}

	return &s, nil
}

// errNoStep: a step name the program does not have.
var errNoStep = errors.New("bindings: no such step")
