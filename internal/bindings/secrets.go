package bindings

import (
	"slices"
	"sort"

	"google.golang.org/protobuf/proto"

	sp "github.com/gopherex/schemapb/go/schemapb"
)

// taint is what of a value is secret: nothing (the zero taint), all of it,
// or the secret parts of a shape — the value is exactly a variable or a
// selection of one, so the shape says which of its fields are secret.
type taint struct {
	all bool
	s   *shape
}

func (t taint) none() bool { return !t.all && t.s == nil }

// secrets is the pass that derives, once a definition is valid, what of
// every step's input, undo input and the result carries secrets: a field
// is secret when its expression reads a field a schema marks secret, or
// anything computed from one. Steps are visited in execution order so a
// step's output taint is known to the steps that read it; a for-each body
// sees its item. It returns the result's effective schema and whether the
// whole result is secret.
func (c *compiler) secrets(
	drafts []*draft, groups [][]string, src source, resultReads []read, result *shape,
) ([]byte, bool) {
	vars := map[string]taint{}
	if src.kind == KindBinding {
		vars[VarReq] = taint{s: c.input}
	} else {
		vars[VarEvent] = taint{s: c.input}
	}

	c.stepSecrets(drafts, groups, vars)

	if src.kind != KindBinding {
		return nil, false
	}

	var declared *sp.Schema
	if result != nil && result.kind == kindObject {
		declared = c.declaredOf(result)
	}

	return c.effective(pathResult, src.result, declared, c.taintsOf(resultReads, vars))
}

// stepSecrets visits the steps of one frame in group order.
func (c *compiler) stepSecrets(drafts []*draft, groups [][]string, vars map[string]taint) {
	byName := map[string]*draft{}
	for _, d := range drafts {
		byName[d.Name] = d
	}

	for _, g := range groups {
		for _, name := range g {
			if d := byName[name]; d != nil {
				c.stepSecret(d, vars)
			}
		}
	}
}

func (c *compiler) stepSecret(d *draft, vars map[string]taint) {
	inner := vars

	if d.ForEach != "" {
		inner = make(map[string]taint, len(vars)+1)
		for k, v := range vars {
			inner[k] = v
		}

		inner[d.ItemVar()] = elemTaint(c.exprTaint(d.forEachReads, vars))
	}

	switch {
	case d.body != nil:
		c.stepSecrets(d.body, d.bodyGroups, inner)

		// Items are the body's results: dyn, so any secret in them taints
		// the step's output whole.
		if !c.exprTaint(d.resultReads, inner).none() {
			vars[d.Name] = taint{all: true}
		}
	default:
		d.inputSchema, d.inputSecret = c.effective(d.path+"/input", d.Input, d.act.GetInput(),
			c.taintsOf(d.inputReads, inner))

		out := taint{s: d.outShape}
		if d.ForEach != "" {
			out.s = &shape{kind: kindList, elem: d.outShape}
		}

		if c.tainted(out.s) {
			vars[d.Name] = out
		}
	}

	if d.Undo != "" && !d.UndoInput.IsZero() {
		d.undoSchema, d.undoSecret = c.effective(d.path+"/undoInput", d.UndoInput, d.undo.GetInput(),
			c.taintsOf(d.undoReads, inner))
	}
}

// elemTaint is the taint of an item of a list or map value.
func elemTaint(t taint) taint {
	switch {
	case t.all:
		return t
	case t.s == nil:
		return taint{}
	case t.s.secret:
		return taint{all: true}
	case t.s.kind == kindList || t.s.kind == kindMap:
		return taint{s: t.s.elem}
	default:
		return taint{}
	}
}

// exprTaint is the taint of one value's expressions together: for a
// for-each list or a body result, where only whether anything is secret
// matters.
func (c *compiler) exprTaint(rs []read, vars map[string]taint) taint {
	var out taint

	for _, t := range c.taintsOf(rs, vars) {
		out = join(out, t)
	}

	return out
}

// taintsOf is the taint of every expression among reads, by path: a bare
// read of a shape with secrets is that shape; any other read of one, or a
// read through a secret field, is the whole expression.
func (c *compiler) taintsOf(rs []read, vars map[string]taint) map[string]taint {
	out := map[string]taint{}

	for _, r := range rs {
		t := descend(vars[r.variable], r.fields)
		if !t.all && !c.tainted(t.s) {
			continue
		}

		if !r.bare {
			t = taint{all: true}
		}

		out[r.path] = join(out[r.path], t)
	}

	for path := range out {
		if !slices.Contains(c.analysis.Secrets, path) {
			c.analysis.Secrets = append(c.analysis.Secrets, path)
		}
	}

	sort.Strings(c.analysis.Secrets)

	return out
}

// join is the taint of a value with both taints: all wins; two partial
// taints of one expression are the whole of it.
func join(a, b taint) taint {
	switch {
	case a.all || b.all:
		return taint{all: true}
	case a.s == nil:
		return b
	case b.s == nil:
		return a
	default:
		return taint{all: true}
	}
}

// descend follows field selections through a taint: a secret field on the
// way makes the value secret whole.
func descend(t taint, fields []string) taint {
	if t.all || t.s == nil {
		return t
	}

	s := t.s

	for _, f := range fields {
		if s.secret {
			return taint{all: true}
		}

		switch s.kind {
		case kindObject:
			s = s.fields[f]
		case kindMap:
			s = s.elem
		default:
			s = nil
		}

		if s == nil {
			return taint{}
		}
	}

	if s.secret {
		return taint{all: true}
	}

	return taint{s: s}
}

// tainted reports whether anything in the shape is secret.
func (c *compiler) tainted(s *shape) bool {
	if c.taints == nil {
		c.taints = map[*shape]bool{}
	}

	return c.taintedIn(s, map[*shape]bool{})
}

func (c *compiler) taintedIn(s *shape, visiting map[*shape]bool) bool {
	if s == nil || visiting[s] {
		return false
	}

	if known, ok := c.taints[s]; ok {
		return known
	}

	visiting[s] = true
	defer delete(visiting, s)

	out := s.secret || c.taintedIn(s.elem, visiting)

	for _, f := range s.fields {
		out = out || c.taintedIn(f, visiting)
	}

	c.taints[s] = out

	return out
}

// effective is the schema the console masks a value by: declared (the
// activity's or hook's; nil when it has none) with the fields whose
// expressions carry secrets marked secret, fields the schema does not
// declare added for it; nil when nothing is secret. true: the value is
// secret whole (its expression is, or it is a list with a secret in it).
func (c *compiler) effective(path string, v Value, declared *sp.Schema, taints map[string]taint) ([]byte, bool) {
	if len(taints) == 0 {
		return nil, false
	}

	root, _ := proto.Clone(declared).(*sp.Schema)
	if root == nil {
		root = &sp.Schema{}
	}

	if c.apply(v, path, root, taints) {
		return nil, true
	}

	return schemaBytes(root), false
}

// apply marks the secrets of v in s, an object schema; true when v is
// secret whole.
func (c *compiler) apply(v Value, path string, s *sp.Schema, taints map[string]taint) bool {
	switch v.Kind() {
	case ValueExpr:
		t := taints[path]

		switch {
		case t.none():
			return false
		case t.all, t.s.kind != kindObject:
			return true
		default:
			c.mark(s, t.s)

			return false
		}
	case ValueObject:
		for _, f := range v.Fields() {
			c.applyField(f, path+"/"+escapePointer(f.Name), s, taints)
		}

		return false
	case ValueList:
		return anyTaint(v, path, taints)
	default:
		return false
	}
}

// applyField marks one field of an object value: into the declared
// field's object schema when both are objects, else the field whole.
func (c *compiler) applyField(f Field, path string, s *sp.Schema, taints map[string]taint) {
	declared := fieldOf(s, f.Name)

	switch {
	case f.Value.Kind() == ValueObject && declared != nil && declared.GetObject().GetSchema() != nil:
		if c.apply(f.Value, path, declared.GetObject().GetSchema(), taints) {
			declared.Secret = true
		}
	case f.Value.Kind() == ValueObject && declared == nil:
		sub := &sp.Schema{}
		if c.apply(f.Value, path, sub, taints) {
			s.Fields = append(s.Fields, &sp.Schema_Field{Name: f.Name, Secret: true})
		} else if len(sub.GetFields()) > 0 {
			s.Fields = append(s.Fields, &sp.Schema_Field{
				Name: f.Name, Kind: &sp.Schema_Field_Object_{Object: &sp.Schema_Field_Object{Schema: sub}},
			})
		}
	case anyTaint(f.Value, path, taints):
		if declared == nil {
			s.Fields = append(s.Fields, &sp.Schema_Field{Name: f.Name, Secret: true})
		} else {
			declared.Secret = true
		}
	}
}

// anyTaint reports whether any expression of v carries a secret.
func anyTaint(v Value, path string, taints map[string]taint) bool {
	found := false

	v.walkExprs(path, func(p, _ string) {
		if !taints[p].none() {
			found = true
		}
	})

	return found
}

// mark marks in s, an object schema, the fields sh says are secret: a
// secret field, or one whose shape is not an object, whole; an object
// field by its own fields. Fields the schema does not declare are added,
// with their secret fields when they are objects. A shape met again on
// the way down (a recursive def) is hidden whole.
func (c *compiler) mark(s *sp.Schema, sh *shape) {
	c.markIn(s, sh, map[*shape]bool{sh: true})
}

func (c *compiler) markIn(s *sp.Schema, sh *shape, visiting map[*shape]bool) {
	for _, name := range sortedKeys(sh.fields) {
		sub := sh.fields[name]
		if !c.tainted(sub) {
			continue
		}

		f := fieldOf(s, name)
		whole := sub.secret || sub.kind != kindObject || visiting[sub]

		switch {
		case whole:
			if f == nil {
				s.Fields = append(s.Fields, &sp.Schema_Field{Name: name, Secret: true})
			} else {
				f.Secret = true
			}
		case f == nil:
			child := &sp.Schema{}
			visiting[sub] = true
			c.markIn(child, sub, visiting)
			delete(visiting, sub)

			s.Fields = append(s.Fields, &sp.Schema_Field{
				Name: name, Kind: &sp.Schema_Field_Object_{Object: &sp.Schema_Field_Object{Schema: child}},
			})
		case f.GetObject().GetSchema() == nil:
			f.Secret = true
		default:
			visiting[sub] = true
			c.markIn(f.GetObject().GetSchema(), sub, visiting)
			delete(visiting, sub)
		}
	}
}

func fieldOf(s *sp.Schema, name string) *sp.Schema_Field {
	for _, f := range s.GetFields() {
		if f.GetName() == name {
			return f
		}
	}

	return nil
}

// declaredOf is the hook's output schema the result shape was built from.
func (c *compiler) declaredOf(result *shape) *sp.Schema {
	if result == nil || result == c.resultShape {
		return c.resultDeclared
	}

	return nil
}
