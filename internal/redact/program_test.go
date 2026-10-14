package redact_test

import (
	"encoding/json"
	"testing"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/redact"
)

// Activities of the fixture installation.
type (
	smsIn struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	smsOut struct {
		ID string `json:"id"`
	}
	renderIn struct {
		Name string            `json:"name"`
		Data map[string]string `json:"data,omitempty"`
	}
	renderOut struct {
		Body string `json:"body"`
	}
	vaultOut struct {
		Secret string `json:"secret" schemapb:"secret=true"`
		Label  string `json:"label"`
	}
	sentOut struct {
		Delivered bool   `json:"delivered"`
		Echo      string `json:"echo"`
	}
)

// installation: iam asks SendOTP with a secret code and publishes
// LoggedIn with a secret token; sms, templates, vault and audit implement
// activities (audit without schemas).
func installation(t *testing.T) bindings.Manifests {
	t.Helper()

	act := func(name string) *backplanev1.Activity {
		return &backplanev1.Activity{Name: name, Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY}
	}

	return bindings.Manifests{
		{
			Service: "iam", Version: "1.0.0",
			Hooks: []*backplanev1.Hook{{
				Name: "SendOTP", Input: schemaOf[otp](t), Output: schemaOf[sentOut](t),
			}},
			Events: []*backplanev1.Event{{Name: "LoggedIn", Schema: schemaOf[user](t)}},
		},
		{Service: "sms", Version: "1.0.0", Activities: []*backplanev1.Activity{{
			Name: "Send", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY,
			Input: schemaOf[smsIn](t), Output: schemaOf[smsOut](t),
		}}},
		{Service: "templates", Version: "1.0.0", Activities: []*backplanev1.Activity{{
			Name: "Render", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY,
			Input: schemaOf[renderIn](t), Output: schemaOf[renderOut](t),
		}}},
		{Service: "vault", Version: "1.0.0", Activities: []*backplanev1.Activity{{
			Name: "Read", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY, Output: schemaOf[vaultOut](t),
		}}},
		{Service: "audit", Version: "1.0.0", Activities: []*backplanev1.Activity{act("Log")}},
	}
}

// compiled is the marshaled program of a binding (YAML) and its
// analysis.
func compiled(t *testing.T, text string) (*redact.Program, bindings.Analysis) {
	t.Helper()

	b, err := bindings.BindingYAML(text)
	if err != nil {
		t.Fatalf("binding: %v", err)
	}

	p, err := bindings.CompileBinding(b, installation(t))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	a, err := bindings.AnalyzeBinding(b, installation(t))
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return redact.ParseProgram(data), a
}

func step(t *testing.T, p *redact.Program, label string) *redact.Step {
	t.Helper()

	st, ok := p.Step(label)
	if !ok {
		t.Fatalf("no step %s", label)
	}

	return st
}

// A field whose expression reads a secret is secret in the step's input,
// a bare read or computed from one; a field reading the rest is not. The
// analysis names the secret nodes.
func TestProgramStepInput(t *testing.T) {
	t.Parallel()

	p, a := compiled(t, `
hook: iam.SendOTP
steps:
  bare: {activity: sms.Send, input: {to: req.to, text: req.code}}
  computed: {activity: sms.Send, input: {to: req.user.name, text: "'code: ' + req.code"}}
  nested: {activity: sms.Send, input: {to: req.to, text: req.user.token}}
  clean: {activity: sms.Send, input: {to: req.to, text: req.user.name}}
result: {delivered: true, echo: req.to}
`)

	in := []byte(`{"to":"+1","text":"1234"}`)

	for _, name := range []string{"bare", "computed", "nested"} {
		if got := string(step(t, p, name).MaskInput(in)); got != `{"text":"***","to":"+1"}` {
			t.Errorf("%s: %s", name, got)
		}
	}

	if got := string(step(t, p, "clean").MaskInput(in)); got != string(in) {
		t.Errorf("clean: %s", got)
	}

	want := []string{"/steps/bare/input/text", "/steps/computed/input/text", "/steps/nested/input/text"}
	if got := a.Secrets; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("secrets %v", got)
	}
}

// An input that is the request whole hides the request's secret fields
// even though the activity declares none of them; a secret value used
// whole hides the input whole; a list with a secret item hides the field.
func TestProgramWholeValues(t *testing.T) {
	t.Parallel()

	p, _ := compiled(t, `
hook: iam.SendOTP
steps:
  whole: {activity: audit.Log, input: req}
  all: {activity: audit.Log, input: req.code}
  items: {activity: audit.Log, input: {entries: [req.to, req.code]}}
  object: {activity: audit.Log, input: {who: {name: req.user.name, token: req.user.token}}}
result: {delivered: true, echo: req.to}
`)

	in := `{"to":"a","code":"1","user":{"name":"n","token":"t"},"other":1}`
	if got := string(step(t, p, "whole").MaskInput([]byte(in))); got !=
		`{"code":"***","other":1,"to":"a","user":{"name":"n","token":"***"}}` {
		t.Errorf("whole: %s", got)
	}

	if got := string(step(t, p, "all").MaskInput([]byte(`"1234"`))); got != `"***"` {
		t.Errorf("all: %s", got)
	}

	if got := string(step(t, p, "items").MaskInput([]byte(`{"entries":["a","1"]}`))); got != `{"entries":"***"}` {
		t.Errorf("items: %s", got)
	}

	if got := string(step(t, p, "object").MaskInput([]byte(`{"who":{"name":"n","token":"t"}}`))); got !=
		`{"who":{"name":"n","token":"***"}}` {
		t.Errorf("object: %s", got)
	}
}

// Secrets flow through steps: a step reading a secret output of another
// hides it; an activity's output is secret only where its author says so,
// so a template rendered from a code is not. The hook's answer is masked
// by what the result reads.
func TestProgramFlow(t *testing.T) {
	t.Parallel()

	p, _ := compiled(t, `
hook: iam.SendOTP
steps:
  read: {activity: vault.Read}
  render: {activity: templates.Render, input: {name: "'otp'", data: {code: req.code}}}
  send: {activity: sms.Send, input: {to: req.to, text: read.secret}}
  body: {activity: sms.Send, input: {to: req.to, text: render.body}}
  label: {activity: sms.Send, input: {to: req.to, text: read.label}}
result: {delivered: true, echo: req.code}
`)

	if got := string(step(t, p, "render").MaskInput([]byte(`{"name":"otp","data":{"code":"1"}}`))); got !=
		`{"data":"***","name":"otp"}` {
		t.Errorf("render: %s", got)
	}

	if got := string(step(t, p, "read").MaskOutput([]byte(`{"secret":"s","label":"l"}`))); got !=
		`{"label":"l","secret":"***"}` {
		t.Errorf("read output: %s", got)
	}

	in := []byte(`{"to":"a","text":"x"}`)
	if got := string(step(t, p, "send").MaskInput(in)); got != `{"text":"***","to":"a"}` {
		t.Errorf("send: %s", got)
	}

	for _, name := range []string{"body", "label"} {
		if got := string(step(t, p, name).MaskInput(in)); got != string(in) {
			t.Errorf("%s: %s", name, got)
		}
	}

	res := []byte(`{"delivered":true,"echo":"1"}`)
	if got := string(p.MaskResult(res)); got != `{"delivered":true,"echo":"***"}` {
		t.Errorf("result: %s", got)
	}

	if got := string(p.MaskInput([]byte(`{"to":"a","code":"1"}`))); got != `{"code":"***","to":"a"}` {
		t.Errorf("input: %s", got)
	}
}

// A for-each over a secret list makes its item secret; a body's steps see
// it; an undo input reading a secret is masked, an undo without one by
// the step's output.
func TestProgramForEachAndUndo(t *testing.T) {
	t.Parallel()

	p, _ := compiled(t, `
hook: iam.SendOTP
steps:
  each: {activity: sms.Send, forEach: req.codes, input: {to: req.to, text: item}}
  users:
    forEach: req.links
    as: l
    steps:
      send: {activity: sms.Send, input: {to: l.url, text: l.nonce}}
      note: {activity: sms.Send, input: {to: l.url, text: l.url}}
  guarded: {activity: vault.Read, undo: audit.Log, undoInput: {token: req.user.token}}
  plain: {activity: vault.Read, undo: audit.Log}
result: {delivered: true, echo: req.to}
`)

	in := []byte(`{"to":"a","text":"x"}`)
	if got := string(step(t, p, "each[0]").MaskInput(in)); got != `{"text":"***","to":"a"}` {
		t.Errorf("each: %s", got)
	}

	if got := string(step(t, p, "users[1].send").MaskInput(in)); got != `{"text":"***","to":"a"}` {
		t.Errorf("users/send: %s", got)
	}

	if got := string(step(t, p, "users[1].note").MaskInput(in)); got != string(in) {
		t.Errorf("users/note: %s", got)
	}

	if got := string(step(t, p, "guarded").MaskUndo([]byte(`{"token":"t"}`))); got != `{"token":"***"}` {
		t.Errorf("guarded undo: %s", got)
	}

	if got := string(step(t, p, "plain").MaskUndo([]byte(`{"secret":"s","label":"l"}`))); got !=
		`{"label":"l","secret":"***"}` {
		t.Errorf("plain undo: %s", got)
	}

	if st, ok := p.Step("users"); !ok || st.Parent != "" || st.Name != "users" {
		t.Errorf("users step %+v %v", st, ok)
	}

	if st := step(t, p, "users[1].send"); st.Parent != "users" {
		t.Errorf("parent %q", st.Parent)
	}
}

// A rule's event is masked by its schema; a step reading its secret
// field too.
func TestProgramRule(t *testing.T) {
	t.Parallel()

	r, err := bindings.RuleYAML(`
event: iam.LoggedIn
steps:
  send: {activity: sms.Send, input: {to: event.name, text: event.token}}
`)
	if err != nil {
		t.Fatalf("rule: %v", err)
	}

	prog, err := bindings.CompileRule(r, installation(t))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	data, err := json.Marshal(prog)
	if err != nil {
		t.Fatal(err)
	}

	p := redact.ParseProgram(data)

	if got := string(p.MaskInput([]byte(`{"name":"n","token":"t"}`))); got != `{"name":"n","token":"***"}` {
		t.Errorf("event: %s", got)
	}

	if got := string(step(t, p, "send").MaskInput([]byte(`{"to":"n","text":"t"}`))); got != `{"text":"***","to":"n"}` {
		t.Errorf("send: %s", got)
	}
}

// A program that does not parse masks nothing and has no steps.
func TestProgramEmpty(t *testing.T) {
	t.Parallel()

	var p *redact.Program

	if got := p.MaskInput([]byte(`{"a":1}`)); string(got) != `{"a":1}` || len(p.Steps()) != 0 {
		t.Fatalf("nil: %s", got)
	}

	p = redact.ParseProgram([]byte("nope"))
	if _, ok := p.Step("x"); ok || len(p.Steps()) != 0 || string(p.MaskResult([]byte(`1`))) != "1" {
		t.Fatalf("unparsed: %+v", p)
	}
}
