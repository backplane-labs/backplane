package bindings

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"
	"unicode"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	"github.com/google/cel-go/common"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/ext"
	"github.com/google/cel-go/parser"
)

// Limits of an expression.
const (
	// maxExprBytes bounds the source of one expression.
	maxExprBytes = 16 << 10
	// costLimit bounds one evaluation (CEL's cost units): a runaway
	// comprehension fails instead of stalling a workflow task.
	costLimit = 1_000_000
)

// Arities of the comprehension macros.
const (
	// iterArgs: the iteration variable and the body.
	iterArgs = 2
	// transformArgs: the iteration variable, the filter and the transform.
	transformArgs = 3
)

// orderedFn wraps the range of every comprehension macro: a map is
// iterated by its keys in order, so all/exists/map/filter over a map give
// the same result on every run (Go's map order is random).
const orderedFn = "@ordered"

// library is the language of binding expressions: standard CEL with the
// strings, encoders, math and lists extensions and optional values;
// numbers compare across int, uint and double. Nothing in it reads the
// clock, randomness or the outside world.
func library() []cel.EnvOption {
	iterate := func(expand parser.MacroExpander) parser.MacroExpander {
		return func(eh parser.ExprHelper, target ast.Expr, args []ast.Expr) (ast.Expr, *common.Error) {
			return expand(eh, eh.NewCall(orderedFn, target), args)
		}
	}

	k, v := cel.TypeParamType("K"), cel.TypeParamType("V")

	return []cel.EnvOption{
		cel.ClearMacros(),
		cel.Macros(
			cel.HasMacro,
			parser.NewReceiverMacro("all", iterArgs, iterate(parser.MakeAll)),
			parser.NewReceiverMacro("exists", iterArgs, iterate(parser.MakeExists)),
			parser.NewReceiverMacro("exists_one", iterArgs, iterate(parser.MakeExistsOne)),
			parser.NewReceiverMacro("map", iterArgs, iterate(parser.MakeMap)),
			parser.NewReceiverMacro("map", transformArgs, iterate(parser.MakeMap)),
			parser.NewReceiverMacro("filter", iterArgs, iterate(parser.MakeFilter)),
		),
		ext.Strings(), ext.Encoders(), ext.Math(), ext.Lists(),
		cel.OptionalTypes(),
		cel.CrossTypeNumericComparisons(true),
		cel.ParserExpressionSizeLimit(maxExprBytes),
		cel.Function(orderedFn,
			cel.Overload("bp_ordered_list", []*cel.Type{cel.ListType(v)}, cel.ListType(v)),
			cel.Overload("bp_ordered_map", []*cel.Type{cel.MapType(k, v)}, cel.ListType(k)),
			cel.SingletonUnaryBinding(ordered)),
	}
}

// ordered is a list as is and a map as its keys in order.
func ordered(v ref.Val) ref.Val {
	switch x := v.(type) {
	case traits.Lister:
		return v
	case traits.Mapper:
		var keys []ref.Val

		for it := x.Iterator(); it.HasNext() == types.True; {
			keys = append(keys, it.Next())
		}

		slices.SortFunc(keys, compareKeys)

		return types.NewRefValList(types.DefaultTypeAdapter, keys)
	default:
		return types.NewErr("cannot iterate over %s", v.Type().TypeName())
	}
}

func compareKeys(a, b ref.Val) int {
	ta, tb := a.Type().TypeName(), b.Type().TypeName()
	if ta != tb {
		return cmp.Compare(ta, tb)
	}

	if c, ok := a.(traits.Comparer); ok {
		if n, isInt := c.Compare(b).(types.Int); isInt {
			return int(n)
		}
	}

	return 0
}

// baseEnv is the library without variables: every evaluation environment
// extends it.
//
//nolint:gochecknoglobals // one immutable environment per process
var baseEnv = sync.OnceValues(func() (*cel.Env, error) {
	env, err := cel.NewEnv(library()...)
	if err != nil {
		return nil, fmt.Errorf("bindings: cel environment: %w", err)
	}

	return env, nil
})

// runtimeEnv declares every variable dyn: evaluation checks nothing
// statically — the save did — and dispatches on the values.
func runtimeEnv(vars []string) (*cel.Env, error) {
	base, err := baseEnv()
	if err != nil {
		return nil, err
	}

	opts := make([]cel.EnvOption, 0, len(vars))
	for _, v := range vars {
		opts = append(opts, cel.Variable(v, cel.DynType))
	}

	env, err := base.Extend(opts...)
	if err != nil {
		return nil, fmt.Errorf("bindings: cel environment: %w", err)
	}

	return env, nil
}

// typedEnv declares the variables with the types of their shapes and
// knows the object types among them.
func typedEnv(vars map[string]*types.Type, objects map[string]*shape) (*cel.Env, error) {
	reg, err := types.NewRegistry()
	if err != nil {
		return nil, fmt.Errorf("bindings: cel types: %w", err)
	}

	opts := []cel.EnvOption{cel.CustomTypeProvider(&provider{Registry: reg, objects: objects})}
	opts = append(opts, library()...)

	for _, name := range sortedKeys(vars) {
		opts = append(opts, cel.Variable(name, vars[name]))
	}

	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("bindings: cel environment: %w", err)
	}

	return env, nil
}

// program compiles src into an evaluable program of env.
func program(env *cel.Env, src string) (cel.Program, error) {
	checked, iss := env.Compile(src)
	if iss.Err() != nil {
		return nil, fmt.Errorf("%w", iss.Err())
	}

	prg, err := env.Program(checked, cel.CostLimit(costLimit))
	if err != nil {
		return nil, fmt.Errorf("cel: %w", err)
	}

	return prg, nil
}

// eval runs prg over vars into a JSON-able value.
func eval(prg cel.Program, vars map[string]any) (any, error) {
	out, _, err := prg.Eval(vars)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}

	return native(out)
}

// errNotJSON: a value CEL produced has no JSON form.
var errNotJSON = errors.New("value has no JSON form")

// native is a CEL value as JSON would carry it: objects map[string]any
// (keys of other types in their text form), lists []any, int64, uint64,
// float64, string, bool, nil, bytes []byte (base64 in JSON), timestamps
// RFC 3339 strings, durations protojson strings ("1.5s"), an optional
// its value or null.
//
//nolint:cyclop // one case per CEL type
func native(v ref.Val) (any, error) {
	switch x := v.(type) {
	case types.Null:
		return nil, nil //nolint:nilnil // nil is JSON null, a valid value
	case types.Bool:
		return bool(x), nil
	case types.Int:
		return int64(x), nil
	case types.Uint:
		return uint64(x), nil
	case types.Double:
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%w: %v", errNotJSON, f)
		}

		return f, nil
	case types.String:
		return string(x), nil
	case types.Bytes:
		return []byte(x), nil
	case types.Timestamp:
		return x.UTC().Format(time.RFC3339Nano), nil
	case types.Duration:
		return strconv.FormatFloat(x.Seconds(), 'f', -1, 64) + "s", nil
	case *types.Optional:
		if !x.HasValue() {
			return nil, nil //nolint:nilnil // an empty optional is JSON null
		}

		return native(x.GetValue())
	case *types.Err:
		return nil, x
	case traits.Lister:
		return nativeList(x)
	case traits.Mapper:
		return nativeMap(x)
	default:
		if v.Type() == types.NullType {
			return nil, nil //nolint:nilnil // nil is JSON null, a valid value
		}

		out, err := v.ConvertToNative(reflect.TypeFor[any]())
		if err != nil {
			return nil, fmt.Errorf("%w: %s", errNotJSON, v.Type())
		}

		return out, nil
	}
}

func nativeList(l traits.Lister) ([]any, error) {
	n, _ := l.Size().(types.Int)
	out := make([]any, 0, int(n))

	for i := range int64(n) {
		item, err := native(l.Get(types.Int(i)))
		if err != nil {
			return nil, err
		}

		out = append(out, item)
	}

	return out, nil
}

func nativeMap(m traits.Mapper) (map[string]any, error) {
	out := map[string]any{}

	for it := m.Iterator(); it.HasNext() == types.True; {
		k := it.Next()

		key, err := native(k)
		if err != nil {
			return nil, err
		}

		value, err := native(m.Get(k))
		if err != nil {
			return nil, err
		}

		out[fmt.Sprint(key)] = value
	}

	return out, nil
}

// read is one read of a variable by an expression: the variable, the
// field selections that follow it and where they are.
type read struct {
	// path is the JSON Pointer of the expression (set by the compiler).
	path     string
	variable string
	// ident is where the variable's name is.
	ident  Range
	fields []string
	// fieldAt is where each field's name is (zero when not found).
	fieldAt []Range
	// whole is the variable with its selections.
	whole Range
	// bare: the read is the whole expression ("req.otp", not
	// "req.otp + 'x'"): its value is exactly what it reads.
	bare bool
}

// reader collects the reads of one parsed expression.
type reader struct {
	src  []rune
	info *ast.SourceInfo
	out  []read
}

// readsOf are the variables expr reads (identifiers not bound by a
// comprehension), in source order.
func readsOf(src string, parsed *ast.AST) []read {
	r := &reader{src: []rune(src), info: parsed.SourceInfo()}
	r.walk(parsed.Expr(), map[string]int{})

	slices.SortStableFunc(r.out, func(a, b read) int { return cmp.Compare(a.whole.Start, b.whole.Start) })

	return r.out
}

func (r *reader) at(id int64) (Range, bool) {
	o, ok := r.info.GetOffsetRange(id)
	if !ok {
		return Range{}, false
	}

	return Range{Start: int(o.Start), End: int(o.Stop)}, true
}

func (r *reader) walk(e ast.Expr, bound map[string]int) {
	switch e.Kind() {
	case ast.IdentKind:
		if bound[e.AsIdent()] == 0 {
			at, _ := r.at(e.ID())
			r.out = append(r.out, read{variable: e.AsIdent(), ident: at, whole: at})
		}
	case ast.SelectKind:
		r.selection(e, bound)
	case ast.CallKind:
		call := e.AsCall()
		if call.IsMemberFunction() {
			r.walk(call.Target(), bound)
		}

		for _, a := range call.Args() {
			r.walk(a, bound)
		}
	case ast.ListKind:
		for _, item := range e.AsList().Elements() {
			r.walk(item, bound)
		}
	case ast.MapKind:
		for _, entry := range e.AsMap().Entries() {
			r.walk(entry.AsMapEntry().Key(), bound)
			r.walk(entry.AsMapEntry().Value(), bound)
		}
	case ast.StructKind:
		for _, f := range e.AsStruct().Fields() {
			r.walk(f.AsStructField().Value(), bound)
		}
	case ast.ComprehensionKind:
		r.comprehension(e.AsComprehension(), bound)
	default:
	}
}

// selection is a chain of selects: a read of its variable when the chain
// starts at an unbound identifier, else a walk of what it starts at.
func (r *reader) selection(e ast.Expr, bound map[string]int) {
	var fields []string

	root := e
	for root.Kind() == ast.SelectKind {
		fields = append(fields, root.AsSelect().FieldName())
		root = root.AsSelect().Operand()
	}

	if root.Kind() != ast.IdentKind || bound[root.AsIdent()] > 0 {
		r.walk(root, bound)

		return
	}

	slices.Reverse(fields)

	ident, _ := r.at(root.ID())
	reading := read{variable: root.AsIdent(), ident: ident, fields: fields, whole: ident}

	pos := ident.End
	for _, f := range fields {
		name, ok := r.field(pos, f)
		if !ok || ident.IsZero() {
			break
		}

		reading.fieldAt = append(reading.fieldAt, name)
		reading.whole.End = name.End
		pos = name.End
	}

	for len(reading.fieldAt) < len(fields) {
		reading.fieldAt = append(reading.fieldAt, Range{})
	}

	r.out = append(r.out, reading)
}

// field finds ".<name>" (spaces allowed around the dot, the name possibly
// in backquotes) at pos: where the name is.
func (r *reader) field(pos int, name string) (Range, bool) {
	pos = r.skipSpace(pos)
	if pos >= len(r.src) || r.src[pos] != '.' {
		return Range{}, false
	}

	pos = r.skipSpace(pos + 1)
	want := []rune(name)

	if pos < len(r.src) && r.src[pos] == '`' {
		end := pos + 1 + len(want)
		if end < len(r.src) && string(r.src[pos+1:end]) == name && r.src[end] == '`' {
			return Range{Start: pos, End: end + 1}, true
		}

		return Range{}, false
	}

	end := pos + len(want)
	if end > len(r.src) || string(r.src[pos:end]) != name {
		return Range{}, false
	}

	return Range{Start: pos, End: end}, true
}

func (r *reader) skipSpace(pos int) int {
	for pos < len(r.src) && unicode.IsSpace(r.src[pos]) {
		pos++
	}

	return pos
}

// comprehension walks a comprehension with its variables bound in the
// loop and the result.
func (r *reader) comprehension(c ast.ComprehensionExpr, bound map[string]int) {
	r.walk(c.IterRange(), bound)
	r.walk(c.AccuInit(), bound)

	vars := []string{c.IterVar(), c.AccuVar()}
	if c.HasIterVar2() {
		vars = append(vars, c.IterVar2())
	}

	for _, v := range vars {
		bound[v]++
	}

	r.walk(c.LoopCondition(), bound)
	r.walk(c.LoopStep(), bound)
	r.walk(c.Result(), bound)

	for _, v := range vars {
		bound[v]--
	}
}

// issueRange is where in src a CEL issue is: the span of the expression it
// is about, else its location; zero when unknown.
func issueRange(src string, parsed *ast.AST, issue *cel.Error) Range {
	runes := []rune(src)

	if parsed != nil && issue.ExprID != 0 {
		r := &reader{src: runes, info: parsed.SourceInfo()}
		if node, found := findExpr(parsed.Expr(), issue.ExprID); found {
			if span, known := r.span(node); known {
				return span
			}
		}
	}

	line, col := issue.Location.Line(), issue.Location.Column()
	if line < 1 || col < 0 {
		return Range{}
	}

	offset := 0
	for l := 1; l < line && offset < len(runes); offset++ {
		if runes[offset] == '\n' {
			l++
		}
	}

	start := min(offset+col, len(runes))
	if start == len(runes) && start > 0 {
		return Range{Start: start - 1, End: start}
	}

	return Range{Start: start, End: min(start+1, len(runes))}
}

// span is the text an expression covers: its own position joined with
// its operands', a select through its field's name.
func (r *reader) span(e ast.Expr) (Range, bool) {
	out, ok := r.at(e.ID())

	join := func(child ast.Expr) {
		if s, has := r.span(child); has {
			if !ok {
				out, ok = s, true
			}

			out.Start, out.End = min(out.Start, s.Start), max(out.End, s.End)
		}
	}

	switch e.Kind() {
	case ast.SelectKind:
		sel := e.AsSelect()
		if s, has := r.span(sel.Operand()); has {
			if at, found := r.field(s.End, sel.FieldName()); found {
				return Range{Start: s.Start, End: at.End}, true
			}
		}

		join(sel.Operand())
	case ast.CallKind:
		call := e.AsCall()
		if call.IsMemberFunction() {
			join(call.Target())
		}

		for _, a := range call.Args() {
			join(a)
		}
	case ast.ListKind:
		for _, item := range e.AsList().Elements() {
			join(item)
		}
	case ast.MapKind:
		for _, entry := range e.AsMap().Entries() {
			join(entry.AsMapEntry().Key())
			join(entry.AsMapEntry().Value())
		}
	default:
	}

	return out, ok && out.End > out.Start
}

// findExpr is the node of id in e.
//
//nolint:ireturn // CEL's nodes are ast.Expr
func findExpr(e ast.Expr, id int64) (ast.Expr, bool) {
	if e.ID() == id {
		return e, true
	}

	var children []ast.Expr

	switch e.Kind() {
	case ast.SelectKind:
		children = []ast.Expr{e.AsSelect().Operand()}
	case ast.CallKind:
		if e.AsCall().IsMemberFunction() {
			children = append(children, e.AsCall().Target())
		}

		children = append(children, e.AsCall().Args()...)
	case ast.ListKind:
		children = e.AsList().Elements()
	case ast.MapKind:
		for _, entry := range e.AsMap().Entries() {
			children = append(children, entry.AsMapEntry().Key(), entry.AsMapEntry().Value())
		}
	case ast.StructKind:
		for _, f := range e.AsStruct().Fields() {
			children = append(children, f.AsStructField().Value())
		}
	case ast.ComprehensionKind:
		c := e.AsComprehension()
		children = []ast.Expr{c.IterRange(), c.AccuInit(), c.LoopCondition(), c.LoopStep(), c.Result()}
	default:
	}

	for _, child := range children {
		if found, ok := findExpr(child, id); ok {
			return found, true
		}
	}

	return nil, false
}

// maxCollection is the size a cost estimate assumes for a list, map or
// string whose size is not known before the run.
const maxCollection = 1000

// sizeEstimator gives CEL's cost estimate the sizes of unknown
// collections: at most maxCollection.
type sizeEstimator struct{}

func (sizeEstimator) EstimateSize(checker.AstNode) *checker.SizeEstimate {
	return &checker.SizeEstimate{Min: 0, Max: maxCollection}
}

//nolint:gocritic // checker.CostEstimator's signature
func (sizeEstimator) EstimateCallCost(string, string, *checker.AstNode, []checker.AstNode) *checker.CallEstimate {
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}
