package bindings_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/internal/bindings"
)

// A run of §7.1's example step by step: inputs from req and earlier
// outputs, integers of the schemas as ints (protojson's quoted int64 too),
// the result.
func TestEvalSendEmail(t *testing.T) {
	t.Parallel()

	p := mustCompile(t, mustBinding(t, sendEmail))

	s, err := p.Start([]byte(`{"to":"a@b.c","template":"welcome","data":{"name":"Ann"}}`))
	if err != nil {
		t.Fatal(err)
	}

	if run, err := p.When("render", s); err != nil || !run {
		t.Fatalf("when: %v %v", run, err)
	}

	expect(t)(p.Input("render", s))(`{"data":{"name":"Ann"},"name":"welcome"}`)

	s, err = p.Bind(s, "render", []byte(`{"subject":"Hi","text":"Hello Ann"}`))
	if err != nil {
		t.Fatal(err)
	}

	expect(t)(p.Input("send", s))(`{"subject":"Hi","text":"Hello Ann","to":"a@b.c"}`)

	s, err = p.Bind(s, "send", []byte(`{"id":"m-1","size":"42"}`))
	if err != nil {
		t.Fatal(err)
	}

	expect(t)(p.Result(s))(`{"message_id":"m-1"}`)

	v, err := p.Eval("send.size + 1", s.Vars())
	if err != nil || v != int64(43) {
		t.Fatalf("eval: %#v %v", v, err)
	}
}

// The same program on the same values gives the same bytes: maps are
// iterated in key order, results have sorted keys.
func TestEvalDeterministic(t *testing.T) {
	t.Parallel()

	p := mustCompile(t, mustBinding(t, `
hook: iam.SendEmail
steps:
  send:
    activity: smtp.Send
    input:
      to: req.to
      subject: 'req.data.map(k, k + "=" + req.data[k]).join(",")'
      text: string(req.priority * 2)
result: {message_id: send.id}
`))

	data := map[string]string{}

	var want []string

	for c := 'a'; c <= 'z'; c++ {
		data[string(c)] = strconv.Itoa(int(c - 'a'))
		want = append(want, fmt.Sprintf("%c=%d", c, c-'a'))
	}

	in, err := json.Marshal(map[string]any{"to": "x", "template": "t", "data": data, "priority": 21})
	if err != nil {
		t.Fatal(err)
	}

	var first []byte

	for range 30 {
		s, err := p.Start(in)
		if err != nil {
			t.Fatal(err)
		}

		out, err := p.Input("send", s)
		if err != nil {
			t.Fatal(err)
		}

		if first == nil {
			first = out
		} else if !bytes.Equal(out, first) {
			t.Fatalf("run differs:\n%s\n%s", first, out)
		}
	}

	wantJSON := fmt.Sprintf(`{"subject":%q,"text":"42","to":"x"}`, strings.Join(want, ","))
	if string(first) != wantJSON {
		t.Fatalf("input %s, want %s", first, wantJSON)
	}
}

// A rule: the filter over event and meta, a skipped step seen by the next
// one as {} and steps.<name>.skipped.
func TestEvalRule(t *testing.T) {
	t.Parallel()

	r := mustRule(t, `
event: iam.UserRegistered
when: event.name != ""
steps:
  a: {activity: billing.Charge, input: {v: event.email}, when: 'event.email.endsWith("@x.io")'}
  b:
    activity: billing.Charge
    input: {skipped: steps.a.skipped, got: a, at: 'meta.id + "@" + meta.time'}
`)

	p, err := bindings.CompileRule(r, manifests(t))
	if err != nil {
		t.Fatal(err)
	}

	meta := bindings.Meta{ID: "1", Source: "iam", Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}

	s, err := p.StartEvent([]byte(`{"email":"u@y.io","name":"U"}`), meta)
	if err != nil {
		t.Fatal(err)
	}

	if ok, err := p.Match(s); err != nil || !ok {
		t.Fatalf("match: %v %v", ok, err)
	}

	if run, err := p.When("a", s); err != nil || run {
		t.Fatalf("when a: %v %v", run, err)
	}

	if s, err = p.Skip(s, "a"); err != nil {
		t.Fatal(err)
	}

	expect(t)(p.Input("b", s))(`{"at":"1@2026-01-02T03:04:05Z","got":{},"skipped":true}`)

	empty, err := p.StartEvent([]byte(`{"email":"u@y.io","name":""}`), meta)
	if err != nil {
		t.Fatal(err)
	}

	if ok, err := p.Match(empty); err != nil || ok {
		t.Fatalf("match of an empty name: %v %v", ok, err)
	}

	if _, err := p.Start(nil); err == nil {
		t.Fatal("Start of a rule program")
	}
}

// Undo gets the step's output by default, its own input otherwise.
func TestUndoInput(t *testing.T) {
	t.Parallel()

	p := mustCompile(t, mustBinding(t, `
hook: iam.SendEmail
steps:
  send: {activity: smtp.Send, input: {to: req.to, subject: "'s'", text: "'t'"}, undo: smtp.Recall}
  again:
    activity: smtp.Send
    input: {to: req.to, subject: "'s'", text: send.id}
    undo: smtp.Recall
    undoInput: {id: 'again.id + "/" + send.id'}
result: {message_id: send.id}
`))

	s, err := p.Start([]byte(`{"to":"a","template":"t"}`))
	if err != nil {
		t.Fatal(err)
	}

	if s, err = p.Bind(s, "send", []byte(`{"id":"m-1","size":7}`)); err != nil {
		t.Fatal(err)
	}

	if s, err = p.Bind(s, "again", []byte(`{"id":"m-2","size":1}`)); err != nil {
		t.Fatal(err)
	}

	expect(t)(p.UndoInput("send", s))(`{"id":"m-1","size":7}`)
	expect(t)(p.UndoInput("again", s))(`{"id":"m-2/m-1"}`)

	if plan, _ := p.Step("send"); plan.Undo != "smtp.Recall" || plan.UndoService != "smtp" || plan.UndoKind == 0 {
		t.Fatalf("plan %+v", plan)
	}
}

// A failure on the values of a run names the step and the field.
func TestEvalError(t *testing.T) {
	t.Parallel()

	p := mustCompile(t, mustBinding(t, `
hook: iam.SendEmail
steps:
  x: {activity: billing.Charge, input: {v: {w: ['req.data["missing"]']}}}
result: {message_id: "'m'"}
`))

	s, err := p.Start([]byte(`{"to":"a","template":"t","data":{}}`))
	if err != nil {
		t.Fatal(err)
	}

	_, err = p.Input("x", s)

	var ee *bindings.EvalError
	if !errors.As(err, &ee) || ee.Step != "x" || ee.Place != "input.v.w[0]" ||
		!strings.HasPrefix(err.Error(), "transform failed at step x (input.v.w[0]): ") {
		t.Fatalf("error %v", err)
	}

	if _, err := p.Bind(s, "x", []byte(`{`)); err == nil {
		t.Fatal("bind of a broken output")
	}

	if _, err := p.Input("nope", s); err == nil {
		t.Fatal("input of an unknown step")
	}
}

// Values nest: objects and lists are built node by node, numbers,
// booleans and null are literals, a string is always an expression.
func TestEvalNested(t *testing.T) {
	t.Parallel()

	p := mustCompile(t, mustBinding(t, `
hook: iam.SendEmail
steps:
  x:
    activity: billing.Charge
    input:
      to: {address: req.to, copies: [req.to, "'b@c.d'"]}
      amount: 12
      share: 0.5
      urgent: true
      note: null
      words: "size(req.template)"
result: {message_id: "'m'"}
`))

	s, err := p.Start([]byte(`{"to":"a@b.c","template":"four"}`))
	if err != nil {
		t.Fatal(err)
	}

	expect(t)(p.Input("x", s))(`{"amount":12,"note":null,"share":0.5,` +
		`"to":{"address":"a@b.c","copies":["a@b.c","b@c.d"]},"urgent":true,"words":4}`)
}

// A marshaled program is the same program: a workflow evaluates exactly
// what was compiled.
func TestProgramJSON(t *testing.T) {
	t.Parallel()

	p := mustCompile(t, mustBinding(t, sendEmail))

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var back bindings.Program
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(p.Steps(), back.Steps()) || !reflect.DeepEqual(p.Groups(), back.Groups()) ||
		back.Source() != p.Source() || back.Kind() != p.Kind() {
		t.Fatalf("round trip:\n%+v\n%+v", p.Steps(), back.Steps())
	}

	again, err := json.Marshal(&back)
	if err != nil || !bytes.Equal(again, data) {
		t.Fatalf("marshal again differs: %v\n%s\n%s", err, data, again)
	}

	s, err := back.Start([]byte(`{"to":"a","template":"w","data":{"k":"v"}}`))
	if err != nil {
		t.Fatal(err)
	}

	expect(t)(back.Input("render", s))(`{"data":{"k":"v"},"name":"w"}`)
}

// expect(t)(p.Input(...))(want) checks a JSON result.
func expect(t *testing.T) func(got []byte, err error) func(want string) {
	t.Helper()

	return func(got []byte, err error) func(want string) {
		return func(want string) {
			t.Helper()

			if err != nil {
				t.Fatal(err)
			}

			if string(got) != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		}
	}
}
