package bindings_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/backplane-labs/backplane/internal/bindings"
)

// A rename changes reads of the step and nothing else: not a string, not
// a field of the same name, not a comprehension variable shadowing it.
func TestRenameBinding(t *testing.T) {
	t.Parallel()

	b := mustBinding(t, `
hook: iam.SendEmail
steps:
  render: {activity: template.Exec, input: {name: req.template}}
  send:
    activity: smtp.Send
    input:
      to: req.to
      subject: "render.subject + ' render ' + req.render"
      text: "[1].map(render, render + 1).size() > 0 ? render.text : ''"
    when: "!steps.render.skipped && !steps.`+"`render`"+`.skipped"
    after: [render]
result: {message_id: "send.id + render . subject"}
editor: {nodes: {render: {x: 1, y: 2}}}
`)

	got, vs := bindings.RenameBinding(b, "render", "tmpl")
	if len(vs) > 0 {
		t.Fatal(vs)
	}

	want := mustBinding(t, `
hook: iam.SendEmail
steps:
  tmpl: {activity: template.Exec, input: {name: req.template}}
  send:
    activity: smtp.Send
    input:
      to: req.to
      subject: "tmpl.subject + ' render ' + req.render"
      text: "[1].map(render, render + 1).size() > 0 ? tmpl.text : ''"
    when: "!steps.tmpl.skipped && !steps.`+"`tmpl`"+`.skipped"
    after: [tmpl]
result: {message_id: "send.id + tmpl . subject"}
editor: {nodes: {tmpl: {x: 1, y: 2}}}
`)

	if !proto.Equal(got.PB(), want.PB()) {
		t.Fatalf("got\n%v\nwant\n%v", got.PB(), want.PB())
	}

	if _, onto := bindings.RenameBinding(b, "render", "send"); !has(onto, "/steps/render", bindings.CodeInvalidName) {
		t.Fatalf("rename onto another step: %v", onto)
	}

	if _, unknown := bindings.RenameBinding(b, "nope", "x"); !has(unknown, "/steps/nope", bindings.CodeUnknownStep) {
		t.Fatalf("rename of an unknown step: %v", unknown)
	}

	broken := mustBinding(t, "hook: iam.SendEmail\nsteps: {a: {activity: billing.Charge}}\nresult: \"a.\"")
	if _, unparsed := bindings.RenameBinding(broken, "a", "b"); !has(unparsed, "/result", bindings.CodeCEL) {
		t.Fatalf("rename through an expression that does not parse: %v", unparsed)
	}
}

// A step of a body is renamed in its body only; a top step also in the
// bodies that read it, its body's positions moving with it.
func TestRenameInBody(t *testing.T) {
	t.Parallel()

	b := mustBinding(t, `
hook: iam.SendEmail
steps:
  charge: {activity: billing.Charge, input: {key: req.to}}
  each:
    forEach: req.recipients
    steps:
      charge: {activity: billing.Charge, input: {key: item.email}}
      recall: {activity: smtp.Recall, input: {id: charge.id}, after: [charge]}
    result: charge.id
result: {message_id: charge.id}
editor: {nodes: {each: {x: 1, y: 1}, each/charge: {x: 2, y: 2}}}
`)

	got, vs := bindings.RenameBinding(b, "each/charge", "pay")
	if len(vs) > 0 {
		t.Fatal(vs)
	}

	want := mustBinding(t, `
hook: iam.SendEmail
steps:
  charge: {activity: billing.Charge, input: {key: req.to}}
  each:
    forEach: req.recipients
    steps:
      pay: {activity: billing.Charge, input: {key: item.email}}
      recall: {activity: smtp.Recall, input: {id: pay.id}, after: [pay]}
    result: pay.id
result: {message_id: charge.id}
editor: {nodes: {each: {x: 1, y: 1}, each/pay: {x: 2, y: 2}}}
`)

	if !proto.Equal(got.PB(), want.PB()) {
		t.Fatalf("got\n%v\nwant\n%v", got.PB(), want.PB())
	}

	moved, vs := bindings.RenameBinding(want, "each", "perRecipient")
	if len(vs) > 0 {
		t.Fatal(vs)
	}

	if _, ok := moved.Editor.GetNodes()["perRecipient/pay"]; !ok {
		t.Fatalf("body positions did not move: %v", moved.Editor.GetNodes())
	}

	if _, taken := bindings.RenameBinding(want, "charge", "item"); !has(taken, "/steps/charge", bindings.CodeInvalidName) {
		t.Fatalf("rename onto an item: %v", taken)
	}

	if _, taken := bindings.RenameBinding(want, "each/pay", "charge"); !has(taken, "/steps/each/steps/pay", bindings.CodeInvalidName) {
		t.Fatalf("rename onto an enclosing step: %v", taken)
	}
}
