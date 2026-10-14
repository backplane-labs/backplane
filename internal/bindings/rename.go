package bindings

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// RenameBinding is b with step from renamed to: its key, every `after`
// naming it, its editor position and every expression reading it (by the
// expressions' syntax trees: a string or a field of the same name stays).
// from is the step's id: its name, or "<for-each step>/<name>" for a step
// of a body (bodies nest); to is its new name. Refused: the violations say
// why.
func RenameBinding(b Binding, from, to string) (Binding, []Violation) {
	r := newRenamer(from, to)
	if vs := r.check(b.Steps); len(vs) > 0 {
		return Binding{}, vs
	}

	out := b
	out.Steps = r.steps("", nil, b.Steps)
	out.Result = r.value(nil, pathResult, b.Result)
	out.Editor = r.editor(b.Editor)

	if len(r.vs) > 0 {
		return Binding{}, r.vs
	}

	return out, nil
}

// RenameRule is RenameBinding for a rule.
func RenameRule(rule Rule, from, to string) (Rule, []Violation) {
	r := newRenamer(from, to)
	if vs := r.check(rule.Steps); len(vs) > 0 {
		return Rule{}, vs
	}

	out := rule
	out.Steps = r.steps("", nil, rule.Steps)
	out.When = r.expr(nil, pathWhen, rule.When)
	out.Editor = r.editor(rule.Editor)

	if len(r.vs) > 0 {
		return Rule{}, r.vs
	}

	return out, nil
}

// renamer renames the step from in the body of the for-each steps scope
// (outermost first; none: at the top). Expressions of that body and of
// the bodies nested in it see the step; others do not.
type renamer struct {
	id, from, to string
	scope        []string
	vs           []Violation
}

func newRenamer(id, to string) *renamer {
	names := strings.Split(id, "/")

	return &renamer{id: id, from: names[len(names)-1], to: to, scope: names[:len(names)-1]}
}

func (r *renamer) violate(path, code, msg string) {
	r.vs = append(r.vs, Violation{Path: path, Code: code, Message: msg})
}

// sees reports whether expressions in the body chain see the step.
func (r *renamer) sees(chain []string) bool {
	return len(chain) >= len(r.scope) && slices.Equal(chain[:len(r.scope)], r.scope)
}

// check refuses a rename of a step that does not exist or to a name that
// is taken where the step is seen: by a step or an item of an enclosing
// body, or of this body and the bodies in it.
func (r *renamer) check(steps []Step) []Violation {
	path := ""
	taken := map[string]bool{}

	for _, name := range r.scope {
		path += Pointer("steps", name)
		i := slices.IndexFunc(steps, func(s Step) bool { return s.Name == name && s.ForEach != "" })

		if i < 0 {
			r.violate(path, CodeUnknownStep, "no for-each step "+name)

			return r.vs
		}

		for i := range steps {
			taken[steps[i].Name] = true
		}

		taken[steps[i].ItemVar()] = true
		taken[steps[i].ItemVar()+"Index"] = true
		steps = steps[i].Steps
	}

	path += Pointer("steps", r.from)
	exists := slices.ContainsFunc(steps, func(s Step) bool { return s.Name == r.from })

	names(steps, taken)
	delete(taken, r.from)

	switch {
	case !exists:
		r.violate(path, CodeUnknownStep, "no step "+r.from)
	case !IsIdent(r.to):
		r.violate(path, CodeInvalidName, fmt.Sprintf("%q is not an identifier [A-Za-z_][A-Za-z0-9_]*", r.to))
	case reserved[r.to]:
		r.violate(path, CodeReservedName, fmt.Sprintf("%q is reserved", r.to))
	case r.to != r.from && taken[r.to]:
		r.violate(path, CodeInvalidName, r.to+" is taken by another step or an item")
	}

	return r.vs
}

// names adds the steps and items of steps and of the bodies in them.
func names(steps []Step, taken map[string]bool) {
	for i := range steps {
		s := &steps[i]
		taken[s.Name] = true

		if s.ForEach != "" {
			taken[s.ItemVar()] = true
			taken[s.ItemVar()+"Index"] = true
			names(s.Steps, taken)
		}
	}
}

// steps renames in the steps of the body chain (path: its pointer).
func (r *renamer) steps(prefix string, chain []string, steps []Step) []Step {
	out := make([]Step, 0, len(steps))
	here := slices.Equal(chain, r.scope)

	for i := range steps {
		s := steps[i]
		path := prefix + Pointer("steps", s.Name)

		s.When = r.expr(chain, path+"/when", s.When)
		s.ForEach = r.expr(chain, path+"/forEach", s.ForEach)
		s.Input = r.value(chain, path+"/input", s.Input)
		s.UndoInput = r.value(chain, path+"/undoInput", s.UndoInput)

		if len(s.Steps) > 0 {
			body := append(slices.Clone(chain), s.Name)
			s.Steps = r.steps(path, body, s.Steps)
			s.Result = r.value(body, path+"/result", s.Result)
		}

		if here {
			s.After = slices.Clone(s.After)

			for i, a := range s.After {
				if a == r.from {
					s.After[i] = r.to
				}
			}

			if s.Name == r.from {
				s.Name = r.to
			}
		}

		out = append(out, s)
	}

	return sortSteps(out)
}

func (r *renamer) value(chain []string, path string, v Value) Value {
	return v.mapExprs(path, func(path, src string) string { return r.expr(chain, path, src) })
}

// expr is src with every read of the step renamed, when the body chain
// sees it.
func (r *renamer) expr(chain []string, path, src string) string {
	if src == "" || !r.sees(chain) {
		return src
	}

	env, err := baseEnv()
	if err != nil {
		r.violate(path, CodeCEL, err.Error())

		return src
	}

	parsed, iss := env.Parse(src)
	if iss.Err() != nil {
		r.violate(path, CodeCEL, "the expression does not parse; fix it before renaming")

		return src
	}

	var spans []Range

	for _, rd := range readsOf(src, parsed.NativeRep()) {
		switch {
		case rd.variable == r.from:
			spans = append(spans, rd.ident)
		case rd.variable == VarSteps && len(rd.fields) > 0 && rd.fields[0] == r.from:
			spans = append(spans, rd.fieldAt[0])
		default:
			continue
		}

		if spans[len(spans)-1].IsZero() {
			r.violate(path, CodeCEL, "cannot locate "+r.from+" in the expression")

			return src
		}
	}

	runes := []rune(src)

	for i := len(spans) - 1; i >= 0; i-- {
		name := []rune(r.to)
		if string(runes[spans[i].Start:spans[i].End]) == "`"+r.from+"`" {
			name = []rune("`" + r.to + "`")
		}

		runes = append(runes[:spans[i].Start], append(name, runes[spans[i].End:]...)...)
	}

	return string(runes)
}

func (r *renamer) editor(e *consolev1.EditorLayout) *consolev1.EditorLayout {
	if e == nil {
		return nil
	}

	out, _ := proto.Clone(e).(*consolev1.EditorLayout)

	// The step's position and those of the steps of its body move with it.
	renamed := strings.Join(append(slices.Clone(r.scope), r.to), "/")
	nodes := out.GetNodes()

	for id, position := range maps.Clone(nodes) {
		rest, nested := strings.CutPrefix(id, r.id+"/")
		switch {
		case id == r.id:
			delete(nodes, id)
			nodes[renamed] = position
		case nested:
			delete(nodes, id)
			nodes[renamed+"/"+rest] = position
		}
	}

	return out
}
