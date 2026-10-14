package bindings

import (
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
)

// frame is one scope of steps: the definition's, or the body of a
// for-each step, which also sees its enclosing frames and the item.
type frame struct {
	// path is the enclosing for-each step's path; "" at the top.
	path string
	// vars are the variables visible besides `steps`.
	vars map[string]*types.Type
	// states are the steps.<name> of every visible step.
	states map[string]*shape
	// names are the steps of this frame; outer those of enclosing frames.
	names map[string]bool
	outer map[string]bool
	env   *cel.Env
}

// States of steps: skipped by when; a for-each step also counts failed
// items and lists their errors.
//
//nolint:gochecknoglobals // immutable shapes
var (
	stateShape = &shape{
		kind: kindObject, name: "bp.step_state", fields: map[string]*shape{"skipped": {kind: kindBool}},
	}
	errorShape = &shape{kind: kindObject, name: "bp.item_error", fields: map[string]*shape{
		"index": {kind: kindInt}, "message": {kind: kindString},
	}}
	forEachStateShape = &shape{kind: kindObject, name: "bp.for_each_state", fields: map[string]*shape{
		"skipped": {kind: kindBool}, "failed": {kind: kindInt}, "errors": {kind: kindList, elem: errorShape},
	}}
)

// topFrame declares the source: `req`, or `event` and `meta`.
func (c *compiler) topFrame() *frame {
	for _, s := range []*shape{stateShape, errorShape, forEachStateShape} {
		c.shapes.objects[s.name] = s
	}

	f := &frame{
		vars: map[string]*types.Type{}, states: map[string]*shape{}, names: map[string]bool{}, outer: map[string]bool{},
	}

	if c.kind == KindBinding {
		f.vars[VarReq] = c.input.celType()
	} else {
		str := &shape{kind: kindString}
		meta := &shape{kind: kindObject, name: "bp.meta", fields: map[string]*shape{
			"id": str, "source": str, "subject": str, "type": str, "time": str,
		}}
		c.shapes.objects[meta.name] = meta
		f.vars[VarEvent] = c.input.celType()
		f.vars[VarMeta] = meta.celType()
	}

	return f
}

// declare adds the frame's steps: their outputs by the shapes of the
// schemas (a for-each step's is the list of its items' outputs) and their
// states.
func (c *compiler) declare(f *frame, drafts []*draft) {
	for _, d := range drafts {
		if !d.ok {
			continue
		}

		if f.vars[d.Name] != nil || f.states[d.Name] != nil {
			c.violate(d.path, CodeInvalidName, fmt.Sprintf("%q is taken by an enclosing step or item", d.Name))

			continue
		}

		d.outShape = c.shapes.of(d.out, "bp.step"+strings.ReplaceAll(d.path, "/", "."))
		f.names[d.Name] = true

		if d.ForEach == "" {
			f.vars[d.Name] = d.outShape.celType()
			f.states[d.Name] = stateShape

			continue
		}

		elem := d.outShape
		if len(d.Steps) > 0 {
			elem = dynShape // a body result's type is not declared by a schema
		}

		f.vars[d.Name] = (&shape{kind: kindList, elem: elem}).celType()
		f.states[d.Name] = forEachStateShape
	}
}

// frameEnv builds the frame's CEL environment.
func (c *compiler) frameEnv(f *frame) error {
	vars := maps.Clone(f.vars)
	stepsShape := &shape{
		kind: kindObject, name: "bp.steps" + strings.ReplaceAll(f.path, "/", "."), fields: maps.Clone(f.states),
	}
	c.shapes.objects[stepsShape.name] = stepsShape
	vars[VarSteps] = stepsShape.celType()

	env, err := typedEnv(vars, c.shapes.objects)
	if err != nil {
		return err
	}

	f.env = env

	return nil
}

// expressions compiles and type-checks the step's expressions in its frame
// and derives its dependencies; a for-each step's body compiles in its own
// frame.
func (c *compiler) expressions(d *draft, f *frame) error {
	c.env = f.env
	env := f.env

	if d.ForEach != "" {
		body, err := c.forEach(d, f)
		if err != nil {
			return err
		}

		env = body.env
	}

	c.env = env

	if d.When != "" {
		t, rs, ok := c.expr(d.path+"/when", d.When)
		if ok {
			c.boolean(d.path+"/when", t)
		}

		d.when = c.stepReads(d, rs, f)
	}

	c.inputs(d, f)
	c.env = f.env

	for i, a := range d.After {
		path := d.path + "/after/" + strconv.Itoa(i)

		switch {
		case a == d.Name:
			c.violate(path, CodeCycle, fmt.Sprintf("step %s runs after itself", a))
		case !f.names[a]:
			c.violate(path, CodeUnknownStep, "no step "+a)
		default:
			d.after = appendNew(d.after, a)
		}
	}

	for _, list := range [][]string{d.after, d.data, d.when} {
		for _, dep := range list {
			d.deps = appendNew(d.deps, dep)
		}
	}

	return nil
}

// inputs compiles the step's input (unless its body is steps) and undo
// input against the schemas of their activities.
func (c *compiler) inputs(d *draft, f *frame) {
	suffix := strings.ReplaceAll(d.path, "/", ".")

	if len(d.Steps) == 0 {
		var in *shape
		if d.act != nil {
			in = c.shapes.of(d.act.GetInput(), "bp.in"+suffix)
		}

		d.inputReads = c.value(d.path+"/input", d.Input, in)
		d.data = append(d.data, c.stepReads(d, d.inputReads, f)...)
	}

	if d.Undo != "" && !d.UndoInput.IsZero() {
		var undoIn *shape
		if d.undo != nil {
			undoIn = c.shapes.of(d.undo.GetInput(), "bp.undo"+suffix)
		}

		d.undoReads = c.value(d.path+"/undoInput", d.UndoInput, undoIn)
		d.undoRefs = stepNames(d.undoReads, f.names)
	}
}

// itemType is the type of an item of what forEach gives: a list's
// element, a map's {key, value}; dyn when unknown.
func (c *compiler) itemType(d *draft, t *types.Type, ok bool) *types.Type {
	if !ok {
		return types.DynType
	}

	switch t.Kind() {
	case types.ListKind:
		return t.Parameters()[0]
	case types.MapKind:
		return types.NewMapType(types.StringType, types.DynType)
	case types.DynKind, types.AnyKind:
	default:
		c.violate(d.path+"/forEach", CodeTypeMismatch, "expected a list or a map, got "+typeName(t))
	}

	return types.DynType
}

// forEach compiles a for-each step's list and body: the item's frame (the
// body's steps in it when the body is steps). Reads of the frame's steps
// anywhere in the body are the step's data dependencies.
func (c *compiler) forEach(d *draft, f *frame) (*frame, error) {
	t, rs, ok := c.expr(d.path+"/forEach", d.ForEach)
	d.forEachReads = rs
	d.data = append(d.data, c.stepReads(d, rs, f)...)
	item := c.itemType(d, t, ok)
	d.itemType = typeName(item)
	name := d.ItemVar()
	body := &frame{
		path: d.path, vars: maps.Clone(f.vars), states: maps.Clone(f.states), names: map[string]bool{},
		outer: maps.Clone(f.outer),
	}
	maps.Copy(body.outer, f.names)

	for _, v := range []string{name, name + "Index"} {
		if f.vars[v] != nil || f.states[v] != nil {
			c.violate(d.path+"/as", CodeInvalidName, fmt.Sprintf("%q is taken by a step or an enclosing item", v))
		}
	}

	body.vars[name] = item
	body.vars[name+"Index"] = types.IntType

	if len(d.Steps) == 0 {
		return body, c.frameEnv(body)
	}

	d.body = c.steps(d.path, d.Steps)
	c.declare(body, d.body)

	if err := c.frameEnv(body); err != nil {
		return nil, err
	}

	for _, bd := range d.body {
		if err := c.expressions(bd, body); err != nil {
			return nil, err
		}
	}

	c.env = body.env
	d.resultReads = c.value(d.path+"/result", d.Result, nil)

	d.bodyGroups = c.order(d.body)
	c.undoRefs(d.body)

	// What the body reads of this frame's steps, the step waits for; what
	// it reads of enclosing frames, the enclosing step does.
	for _, bd := range d.body {
		for _, name := range bd.outer {
			if f.names[name] {
				d.data = appendNew(d.data, name)
			} else {
				d.outer = appendNew(d.outer, name)
			}
		}
	}

	return body, nil
}

// stepReads are the frame's steps among reads (a read of the step's own
// output reported); reads of enclosing frames' steps go to d.outer.
func (c *compiler) stepReads(d *draft, rs []read, f *frame) []string {
	var out []string

	for _, r := range rs {
		name, ok := stepOf(r, f.names)
		if !ok {
			if outer, isOuter := stepOf(r, f.outer); isOuter {
				d.outer = appendNew(d.outer, outer)
			}

			continue
		}

		if name == d.Name {
			c.violateAt(r.path, CodeCycle, fmt.Sprintf("step %s reads its own output", name), r.whole)

			continue
		}

		out = appendNew(out, name)
	}

	return out
}

// stepOf is the step a read is of: its variable, or the field selected on
// `steps`.
func stepOf(r read, names map[string]bool) (string, bool) {
	name := r.variable
	if name == VarSteps {
		if len(r.fields) == 0 {
			return "", false
		}

		name = r.fields[0]
	}

	return name, names[name]
}

// stepNames are the steps among reads.
func stepNames(rs []read, names map[string]bool) []string {
	var out []string

	for _, r := range rs {
		if name, ok := stepOf(r, names); ok {
			out = appendNew(out, name)
		}
	}

	return out
}
