package bindings

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// ItemError is a failed item of a for-each step: steps.<name>.errors.
type ItemError struct {
	Index   int
	Message string
}

var errNotItems = errors.New("not a list or a map")

// Items are the items a for-each step runs for on s: a list as is, a map
// as {key, value} by key.
func (p *Program) Items(step string, s Scope) ([]any, error) {
	i, ok := p.index[step]
	if !ok || p.steps[i].forEach == nil {
		return nil, fmt.Errorf("%w: %s is not a for-each step", errNoStep, step)
	}

	value, err := eval(p.steps[i].forEach, s.vars)
	if err != nil {
		return nil, &EvalError{Step: step, Place: "forEach", Err: err}
	}

	switch x := value.(type) {
	case []any:
		return x, nil
	case map[string]any:
		out := make([]any, 0, len(x))
		for _, key := range slices.Sorted(maps.Keys(x)) {
			out = append(out, map[string]any{"key": key, "value": x[key]})
		}

		return out, nil
	default:
		return nil, &EvalError{Step: step, Place: "forEach", Err: fmt.Errorf("%w: %T", errNotItems, value)}
	}
}

// ItemScope is s with a for-each step's item and its position bound: the
// scope its when, input, undo input and body see.
func (p *Program) ItemScope(step string, s Scope, index int, item any) (Scope, error) {
	i, ok := p.index[step]
	if !ok || p.spec.Steps[i].ForEach == nil {
		return Scope{}, fmt.Errorf("%w: %s is not a for-each step", errNoStep, step)
	}

	name := p.spec.Steps[i].ForEach.Item
	vars := maps.Clone(s.vars)
	vars[name] = item
	vars[name+"Index"] = int64(index)

	return Scope{vars: vars}, nil
}

// Body is the sub-flow of a for-each step whose body is steps.
func (p *Program) Body(step string) (*Program, bool) {
	i, ok := p.index[step]
	if !ok || p.steps[i].body == nil {
		return nil, false
	}

	return p.steps[i].body, true
}

// Begin is the scope a body starts an item in: the item's scope with the
// body's steps not run yet.
func (p *Program) Begin(item Scope) Scope {
	vars := maps.Clone(item.vars)
	steps, _ := vars[VarSteps].(map[string]any)
	steps = maps.Clone(steps)
	p.declare(vars, steps)
	vars[VarSteps] = steps

	return Scope{vars: vars}
}

// ItemOutput is an item's output (JSON) in the step's list: the activity's
// output, or the body's result on its final scope.
func (p *Program) ItemOutput(s Scope) ([]byte, error) { return p.Result(s) }

// BindItems is s with a for-each step done: its variable the list of the
// items' outputs (JSON; nil: null — skipped or failed), its state the
// failed items.
func (p *Program) BindItems(s Scope, step string, outputs [][]byte, failed []ItemError) (Scope, error) {
	i, ok := p.index[step]
	if !ok || p.spec.Steps[i].ForEach == nil {
		return Scope{}, fmt.Errorf("%w: %s is not a for-each step", errNoStep, step)
	}

	elem := p.steps[i].output
	if p.steps[i].body != nil {
		elem = dynShape
	}

	list := make([]any, 0, len(outputs))

	for index, out := range outputs {
		if out == nil {
			list = append(list, nil)

			continue
		}

		v, err := decodeJSON(out)
		if err != nil {
			return Scope{}, &EvalError{Step: step, Place: fmt.Sprintf("output[%d]", index), Err: err}
		}

		list = append(list, convert(elem, v))
	}

	errs := make([]any, 0, len(failed))
	for _, f := range failed {
		errs = append(errs, map[string]any{"index": int64(f.Index), "message": f.Message})
	}

	vars := maps.Clone(s.vars)
	vars[step] = list
	steps, _ := vars[VarSteps].(map[string]any)
	steps = maps.Clone(steps)
	steps[step] = map[string]any{fieldSkipped: false, fieldFailed: int64(len(failed)), fieldErrors: errs}
	vars[VarSteps] = steps

	return Scope{vars: vars}, nil
}

// CallLabel is a call label read back: the call is of Step, in the body
// of the for-each step Parent (its id: "<outer>/<inner>" in a nested body;
// "" at the top), for Item (the step's own, or its innermost enclosing
// for-each step's for a step of a body; -1: none).
type CallLabel struct {
	Step   string
	Parent string
	Item   int
}

// ParseCallLabel reads a call label — the envelope's step: segments
// "<step>" or "<step>[<item>]" joined by "." from the outermost for-each
// step in.
func ParseCallLabel(label string) CallLabel {
	segments := strings.Split(label, ".")
	out := CallLabel{Item: -1}
	parents := make([]string, 0, len(segments)-1)

	for i, segment := range segments {
		name, item := labelSegment(segment)
		if item >= 0 {
			out.Item = item
		}

		if i < len(segments)-1 {
			parents = append(parents, name)
		} else {
			out.Step = name
		}
	}

	out.Parent = strings.Join(parents, "/")

	return out
}

// labelSegment splits "<name>[<n>]" into the name and n; -1 without an
// index.
func labelSegment(segment string) (string, int) {
	open := strings.IndexByte(segment, '[')
	if open < 0 || !strings.HasSuffix(segment, "]") {
		return segment, -1
	}

	n, err := strconv.Atoi(segment[open+1 : len(segment)-1])
	if err != nil {
		return segment, -1
	}

	return segment[:open], n
}
