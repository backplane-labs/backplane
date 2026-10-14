package redact

import (
	"encoding/json"
	"strings"

	"google.golang.org/protobuf/proto"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// Program is what the console needs of a compiled binding or rule to mask
// its run: the schemas the compiler derived for the input, every step's
// calls and the result. It reads the program's serialized form (the
// workflow's input), so a run masks by the definition it ran, whatever
// the manifests say now.
type Program struct {
	input        *sp.Schema
	result       *sp.Schema
	resultSecret bool
	steps        []Step
	byPath       map[string]*Step
}

// Step is one step of a program with its masks.
type Step struct {
	Name     string
	Activity string
	Kind     backplanev1.ActivityKind
	// Parent is the path of the for-each steps the step is in, "a/b"; ""
	// at the top.
	Parent string

	input, output, undo     *sp.Schema
	inputSecret, undoSecret bool
	// undoInput: the step has an undo input of its own; without one the
	// undo runs on the step's output.
	undoInput bool
}

// Serialized forms of bindings.Program's spec: the fields masking reads.
type (
	specJSON struct {
		Steps        []stepJSON `json:"steps"`
		Input        []byte     `json:"input,omitempty"`
		ResultSchema []byte     `json:"result_schema,omitempty"`
		ResultSecret bool       `json:"result_secret,omitempty"`
	}
	stepJSON struct {
		Name        string                   `json:"name"`
		Activity    string                   `json:"activity"`
		Kind        backplanev1.ActivityKind `json:"kind"`
		Body        *bodyJSON                `json:"body,omitempty"`
		UndoInput   json.RawMessage          `json:"undo_input,omitempty"`
		Output      []byte                   `json:"output,omitempty"`
		InputSchema []byte                   `json:"input_schema,omitempty"`
		InputSecret bool                     `json:"input_secret,omitempty"`
		UndoSchema  []byte                   `json:"undo_schema,omitempty"`
		UndoSecret  bool                     `json:"undo_secret,omitempty"`
	}
	bodyJSON struct {
		Steps []stepJSON `json:"steps"`
	}
)

// ParseProgram reads a marshaled program; a program that does not read
// has no steps and masks nothing.
func ParseProgram(data []byte) *Program {
	var s specJSON

	_ = json.Unmarshal(data, &s)

	p := &Program{
		input: schemaOf(s.Input), result: schemaOf(s.ResultSchema), resultSecret: s.ResultSecret,
		byPath: map[string]*Step{},
	}
	p.steps = p.add("", s.Steps)

	return p
}

func (p *Program) add(parent string, steps []stepJSON) []Step {
	out := make([]Step, 0, len(steps))

	for i := range steps {
		st := &steps[i]
		out = append(out, Step{
			Name: st.Name, Activity: st.Activity, Kind: st.Kind, Parent: parent,
			input: schemaOf(st.InputSchema), inputSecret: st.InputSecret, output: schemaOf(st.Output),
			undo: schemaOf(st.UndoSchema), undoSecret: st.UndoSecret, undoInput: len(st.UndoInput) > 0,
		})
	}

	for i := range out {
		st := &out[i]
		path := st.Name

		if parent != "" {
			path = parent + "/" + st.Name
		}

		p.byPath[path] = st

		if body := steps[i].Body; body != nil {
			p.add(path, body.Steps)
		}
	}

	return out
}

// Steps are the program's top-level steps in order.
func (p *Program) Steps() []Step {
	if p == nil {
		return nil
	}

	return p.steps
}

// Step is the step a call label names: segments "<step>" or
// "<step>[<item>]" joined by "." from the outermost for-each step in.
func (p *Program) Step(label string) (*Step, bool) {
	if p == nil {
		return nil, false
	}

	segments := strings.Split(label, ".")
	for i, s := range segments {
		if at := strings.IndexByte(s, '['); at >= 0 {
			segments[i] = s[:at]
		}
	}

	st, ok := p.byPath[strings.Join(segments, "/")]

	return st, ok
}

// MaskInput is the hook's input or the event's payload masked.
func (p *Program) MaskInput(data []byte) []byte {
	if p == nil {
		return data
	}

	return Mask(p.input, data)
}

// MaskResult is the hook's answer masked.
func (p *Program) MaskResult(data []byte) []byte {
	if p == nil {
		return data
	}

	if p.resultSecret {
		return All(data)
	}

	return Mask(p.result, data)
}

// MaskInput is the step's activity input masked.
func (s *Step) MaskInput(data []byte) []byte {
	if s == nil {
		return data
	}

	if s.inputSecret {
		return All(data)
	}

	return Mask(s.input, data)
}

// MaskOutput is the step's activity output masked.
func (s *Step) MaskOutput(data []byte) []byte {
	if s == nil {
		return data
	}

	return Mask(s.output, data)
}

// MaskUndo is the step's undo input masked: by its own undo input's
// schema, else — the undo runs on the step's output — by the output's.
func (s *Step) MaskUndo(data []byte) []byte {
	switch {
	case s == nil:
		return data
	case s.undoSecret:
		return All(data)
	case s.undoInput:
		return Mask(s.undo, data)
	default:
		return Mask(s.output, data)
	}
}

func schemaOf(b []byte) *sp.Schema {
	if len(b) == 0 {
		return nil
	}

	var s sp.Schema
	if err := proto.Unmarshal(b, &s); err != nil {
		return nil
	}

	return &s
}
