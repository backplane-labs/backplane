package redact

import (
	"encoding/json"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/wire"
)

// Catalog resolves full names ("<service>.<Name>") to what the manifests
// declare: the schemas payloads are masked by.
type Catalog interface {
	Hook(full string) (*backplanev1.Hook, bool)
	Activity(full string) (*backplanev1.Activity, bool)
	Event(full string) (*backplanev1.Event, bool)
	Workflow(full string) (*backplanev1.Workflow, bool)
}

// Manifests is a Catalog over the latest manifest of each service (nil:
// none).
type Manifests func(service string) *backplanev1.Manifest

func (m Manifests) manifest(full string) (*backplanev1.Manifest, string) {
	for i := range len(full) {
		if full[i] == '.' {
			return m(full[:i]), full[i+1:]
		}
	}

	return nil, full
}

// Hook implements Catalog.
func (m Manifests) Hook(full string) (*backplanev1.Hook, bool) {
	man, name := m.manifest(full)
	for _, h := range man.GetHooks() {
		if h.GetName() == name {
			return h, true
		}
	}

	return nil, false
}

// Activity implements Catalog.
func (m Manifests) Activity(full string) (*backplanev1.Activity, bool) {
	man, name := m.manifest(full)
	for _, a := range man.GetActivities() {
		if a.GetName() == name {
			return a, true
		}
	}

	return nil, false
}

// Event implements Catalog.
func (m Manifests) Event(full string) (*backplanev1.Event, bool) {
	man, name := m.manifest(full)
	for _, e := range man.GetEvents() {
		if e.GetName() == name {
			return e, true
		}
	}

	return nil, false
}

// Workflow implements Catalog.
func (m Manifests) Workflow(full string) (*backplanev1.Workflow, bool) {
	man, name := m.manifest(full)
	for _, w := range man.GetWorkflows() {
		if w.GetName() == name {
			return w, true
		}
	}

	return nil, false
}

// EventSchema is the schema of an event, nil when the catalog has none.
func EventSchema(cat Catalog, full string) *sp.Schema {
	if cat == nil {
		return nil
	}

	e, _ := cat.Event(full)

	return e.GetSchema()
}

// History masks the payloads of one run's history as it is read, in
// order: the start tells what the run is — a binding or rule run with its
// program, a hook call, an activity run, a declared workflow — and each
// scheduled call what its result is masked by. Payloads come and go as
// the console's JSON text: envelopes (HookCall, ActivityCall and their
// results) with their payload unwrapped to the JSON it carries.
type History struct {
	cat Catalog
	// prog is the binding or rule run's program.
	prog *Program
	// result masks the run's result.
	result mask
	// calls are the masks of the results of calls in flight, by the event
	// id that scheduled or initiated them.
	calls map[int64]mask
	// seen counts the calls of each step label: the second is its undo.
	seen map[string]bool
}

// mask masks one payload.
type mask func(data []byte) []byte

// payloadKey is the envelopes' payload field.
const payloadKey = "payload"

func keep(data []byte) []byte { return data }

// NewHistory is the masker of one run; cat may be nil when only binding
// runs are read.
func NewHistory(cat Catalog) *History {
	return &History{cat: cat, result: keep, calls: map[int64]mask{}, seen: map[string]bool{}}
}

// Started takes the run's input — the workflow type, the service of its
// task queue and the input's JSON text — and is the text masked.
func (h *History) Started(workflowType, service, input string) string {
	fields, ok := object(input)
	if !ok {
		return input
	}

	switch {
	case workflowType == wire.BindingWorkflow:
		h.prog = ParseProgram(fields["program"])
		h.result = h.prog.MaskResult

		return rewrite(input, fields, h.prog.MaskInput)
	case fields["hook"] != nil:
		var name string

		_ = json.Unmarshal(fields["hook"], &name)
		in, out := h.hook(name)
		h.result = out

		return rewrite(input, fields, in)
	case fields["activity"] != nil:
		var name string

		_ = json.Unmarshal(fields["activity"], &name)
		in, out := h.activity(name)
		h.result = out

		return rewrite(input, fields, in)
	case h.cat != nil:
		if w, found := h.cat.Workflow(service + "." + workflowType); found {
			h.result = schemaMask(w.GetOutput())

			return string(Mask(w.GetInput(), []byte(input)))
		}
	}

	return input
}

// Result is the run's result masked: a HookResult or ActivityResult
// envelope's payload, or a declared workflow's own JSON.
func (h *History) Result(result string) string {
	fields, ok := object(result)
	if !ok {
		return result
	}

	if fields[payloadKey] != nil {
		return rewrite(result, fields, h.result)
	}

	return string(h.result([]byte(result)))
}

// Scheduled takes the input of the call event id schedules — an activity
// task or a child workflow — and is it masked. In a binding run the
// program's step masks it (a label seen before is the step's undo); else
// the activity the envelope names.
func (h *History) Scheduled(id int64, input string) string {
	fields, ok := object(input)
	if !ok || fields["activity"] == nil {
		return input
	}

	var name, label string

	_ = json.Unmarshal(fields["activity"], &name)
	_ = json.Unmarshal(fields["step"], &label)

	in, out := h.activity(name)

	if st, found := h.prog.Step(label); found {
		in, out = st.MaskInput, st.MaskOutput
		if h.seen[label] {
			in = st.MaskUndo
		}

		h.seen[label] = true
	}

	h.calls[id] = out

	return rewrite(input, fields, in)
}

// Completed takes the result of the call scheduled by id and is it
// masked.
func (h *History) Completed(id int64, result string) string {
	out := h.calls[id]
	if out == nil {
		return result
	}

	fields, ok := object(result)
	if !ok {
		return result
	}

	return rewrite(result, fields, out)
}

// Nexus takes the input of a Nexus operation event id schedules — a hook
// call from a service's workflow — and is it masked.
func (h *History) Nexus(id int64, input string) string {
	fields, ok := object(input)
	if !ok || fields["hook"] == nil {
		return input
	}

	var name string

	_ = json.Unmarshal(fields["hook"], &name)
	in, out := h.hook(name)
	h.calls[id] = out

	return rewrite(input, fields, in)
}

func (h *History) hook(full string) (mask, mask) {
	if h.cat == nil {
		return keep, keep
	}

	hk, _ := h.cat.Hook(full)

	return schemaMask(hk.GetInput()), schemaMask(hk.GetOutput())
}

func (h *History) activity(full string) (mask, mask) {
	if h.cat == nil {
		return keep, keep
	}

	if a, ok := h.cat.Activity(full); ok {
		return schemaMask(a.GetInput()), schemaMask(a.GetOutput())
	}

	w, _ := h.cat.Workflow(full)

	return schemaMask(w.GetInput()), schemaMask(w.GetOutput())
}

func schemaMask(s *sp.Schema) mask {
	if s == nil {
		return keep
	}

	return func(data []byte) []byte { return Mask(s, data) }
}

// object is text as a JSON object's fields.
func object(text string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &fields); err != nil || fields == nil {
		return nil, false
	}

	return fields, true
}

// rewrite is text with the envelope's payload masked; text as is when
// there is none or masking changes nothing.
func rewrite(text string, fields map[string]json.RawMessage, m mask) string {
	raw, ok := fields[payloadKey]
	if !ok {
		return text
	}

	masked := m(raw)
	if string(masked) == string(raw) {
		return text
	}

	fields[payloadKey] = masked

	out, err := json.Marshal(fields)
	if err != nil {
		return text
	}

	return string(out)
}
