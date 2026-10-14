package bindings_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
)

// §7.1's example compiles into two levels with the options resolved from
// the binding, the manifests and the platform.
func TestCompileSendEmail(t *testing.T) {
	t.Parallel()

	p := mustCompile(t, mustBinding(t, sendEmail))

	if p.Kind() != bindings.KindBinding || p.Source() != "iam.SendEmail" {
		t.Fatalf("kind %v source %q", p.Kind(), p.Source())
	}

	if got := p.Groups(); !slices.EqualFunc(got, [][]string{{"render"}, {"send"}}, slices.Equal) {
		t.Fatalf("groups %v", got)
	}

	render, _ := p.Step("render")
	if render.Service != "template" || render.Kind != backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY ||
		render.StartToClose != 5*time.Second || render.Retry.Attempts != 2 || len(render.Deps) != 0 {
		t.Fatalf("render %+v", render)
	}

	send, _ := p.Step("send")
	want := bindings.Retry{
		Attempts: bindings.DefaultAttempts, InitialInterval: bindings.DefaultInitialInterval,
		MaxInterval: bindings.DefaultMaxInterval, Backoff: bindings.DefaultBackoff,
	}

	if send.Retry != want || send.StartToClose != bindings.DefaultStartToClose || send.Heartbeat != 3*time.Second ||
		!slices.Equal(send.Deps, []string{"render"}) || send.Group != 1 {
		t.Fatalf("send %+v", send)
	}
}

// Steps without dependencies share a level; after and references order
// them; the binding's options win over the manifest's. Step order in the
// definition does not matter.
func TestCompileGroups(t *testing.T) {
	t.Parallel()

	p := mustCompile(t, mustBinding(t, `
hook: iam.SendEmail
steps:
  e: {activity: billing.Charge, input: {x: steps.d.skipped}}
  d:
    activity: smtp.Send
    input: {to: req.to, subject: a.subject, text: b.text}
  c: {activity: billing.Charge, input: {amount: 1}, after: [a]}
  b:
    activity: template.Exec
    input: {name: "'b'"}
    retry: {attempts: 7, backoff: 1.5}
    startToClose: 2s
  a: {activity: template.Exec, input: {name: "'a'"}}
result: {message_id: d.id}
`))

	want := [][]string{{"a", "b"}, {"c", "d"}, {"e"}}
	if got := p.Groups(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("groups %v, want %v", got, want)
	}

	b, _ := p.Step("b")
	if b.Retry.Attempts != 7 || b.StartToClose != 2*time.Second || b.Retry.Backoff != 1.5 {
		t.Fatalf("b %+v", b)
	}

	if d, _ := p.Step("d"); !slices.Equal(d.Deps, []string{"a", "b"}) {
		t.Fatalf("d deps %v", d.Deps)
	}

	if e, _ := p.Step("e"); !slices.Equal(e.Deps, []string{"d"}) {
		t.Fatalf("e deps %v", e.Deps)
	}
}

const sendStep = `
  send:
    activity: smtp.Send
    input: {to: req.to, subject: "'s'", text: "'t'"}`

// binding is a SendEmail binding of the steps (YAML under steps:) and the
// result (YAML flow value; empty: none).
func binding(hook, steps, result string) string {
	out := "hook: " + hook + "\nsteps:" + steps + "\n"
	if result != "" {
		out += "result: " + result + "\n"
	}

	return out
}

// Every violation names its place (a JSON Pointer) and its code.
func TestViolations(t *testing.T) {
	t.Parallel()

	const ok = "{message_id: send.id}"

	charge := func(name, rest string) string {
		return "\n  " + name + ": {activity: billing.Charge" + rest + "}"
	}

	cases := []struct {
		name, text, path, code string
	}{
		{"unknown hook", binding("iam.Nope", sendStep, ok), "/hook", bindings.CodeUnknownHook},
		{"bad hook name", binding("IAM.send", sendStep, ok), "/hook", bindings.CodeInvalidName},
		{
			"unknown activity", binding("iam.SendEmail", sendStep+"\n  x: {activity: smtp.Nope}", ok),
			"/steps/x/activity", bindings.CodeUnknownActivity,
		},
		{"reserved name", binding("iam.SendEmail", sendStep+charge("req", ""), ok), "/steps/req", bindings.CodeReservedName},
		{"invalid name", binding("iam.SendEmail", sendStep+charge("1x", ""), ok), "/steps/1x", bindings.CodeInvalidName},
		{
			"unknown after", binding("iam.SendEmail", sendStep+charge("x", ", after: [nope]"), ok),
			"/steps/x/after/0", bindings.CodeUnknownStep,
		},
		{
			"after itself", binding("iam.SendEmail", sendStep+charge("x", ", after: [x]"), ok),
			"/steps/x/after/0", bindings.CodeCycle,
		},
		{
			"reads itself", binding("iam.SendEmail", sendStep+charge("x", ", input: {v: x}"), ok),
			"/steps/x/input/v", bindings.CodeCycle,
		},
		{
			"cycle", binding("iam.SendEmail", sendStep+charge("x", ", input: {v: y}")+charge("y", ", input: {v: x}"), ok),
			"/steps/x", bindings.CodeCycle,
		},
		{
			"unknown variable", binding("iam.SendEmail", sendStep+charge("x", ", input: {v: nope}"), ok),
			"/steps/x/input/v", bindings.CodeCEL,
		},
		{
			"unknown field of req", binding("iam.SendEmail", sendStep+charge("x", ", input: {v: req.nope}"), ok),
			"/steps/x/input/v", bindings.CodeCEL,
		},
		{
			"unknown field of a step", binding("iam.SendEmail", sendStep, "{message_id: send.nope}"),
			"/result/message_id", bindings.CodeCEL,
		},
		{
			"literal of another type", binding("iam.SendEmail", `
  send: {activity: smtp.Send, input: {to: 1, subject: "'s'", text: "'t'"}}`, ok),
			"/steps/send/input/to", bindings.CodeTypeMismatch,
		},
		{
			"expression of another type", binding("iam.SendEmail", `
  send: {activity: smtp.Send, input: {to: 1 + 1, subject: "'s'", text: "'t'"}}`, ok),
			"/steps/send/input/to", bindings.CodeTypeMismatch,
		},
		{
			"double into int", binding("iam.SendEmail", `
  send: {activity: smtp.Send, input: {to: "'a'", subject: "'s'", text: "'t'", priority: 1.5}}`, ok),
			"/steps/send/input/priority", bindings.CodeTypeMismatch,
		},
		{
			"list item", binding("iam.SendEmail", `
  send: {activity: smtp.Send, input: {to: "'a'", subject: "'s'", text: "'t'", tags: [req.to, 1]}}`, ok),
			"/steps/send/input/tags/1", bindings.CodeTypeMismatch,
		},
		{
			"list into a string", binding("iam.SendEmail", `
  send: {activity: smtp.Send, input: {to: [req.to], subject: "'s'", text: "'t'"}}`, ok),
			"/steps/send/input/to", bindings.CodeTypeMismatch,
		},
		{
			"nested map value", binding("iam.SendEmail", sendStep+`
  render: {activity: template.Exec, input: {name: "'n'", data: {a: req.to, b: true}}}`, ok),
			"/steps/render/input/data/b", bindings.CodeTypeMismatch,
		},
		{
			"unknown input field", binding("iam.SendEmail", `
  send: {activity: smtp.Send, input: {to: "'a'", subject: "'s'", text: "'t'", cc: "'b'"}}`, ok),
			"/steps/send/input/cc", bindings.CodeUnknownField,
		},
		{
			"missing input field", binding("iam.SendEmail", `
  send: {activity: smtp.Send, input: {to: "'a'", subject: "'s'"}}`, ok),
			"/steps/send/input", bindings.CodeMissingField,
		},
		{"missing result field", binding("iam.SendEmail", sendStep, ""), "/result", bindings.CodeMissingField},
		{
			"result mismatch", binding("iam.SendEmail", sendStep, "{message_id: send.size > 1}"),
			"/result/message_id", bindings.CodeTypeMismatch,
		},
		{
			"when not bool", binding("iam.SendEmail", sendStep+charge("x", ", when: req.to"), ok),
			"/steps/x/when", bindings.CodeTypeMismatch,
		},
		{
			"undo input reads a later step", binding("iam.SendEmail", sendStep+`
  y:
    activity: smtp.Send
    input: {to: "'a'", subject: "'s'", text: send.id}
    undo: smtp.Recall
    undoInput: {id: z.id}
  z: {activity: smtp.Send, input: {to: "'a'", subject: "'s'", text: "'t'"}}`, ok),
			"/steps/y/undoInput", bindings.CodeUndoReference,
		},
		{
			"undo input without undo", binding("iam.SendEmail", sendStep+charge("w", ", undoInput: w"), ok),
			"/steps/w/undoInput", bindings.CodeInvalidOption,
		},
		{
			"unknown undo", binding("iam.SendEmail", sendStep+charge("x", ", undo: billing.Nope"), ok),
			"/steps/x/undo", bindings.CodeUnknownActivity,
		},
		{
			"bad backoff", binding("iam.SendEmail", sendStep+charge("x", ", retry: {backoff: 0.5}"), ok),
			"/steps/x/retry/backoff", bindings.CodeInvalidOption,
		},
		{
			"intervals", binding("iam.SendEmail", sendStep+charge("x", ", retry: {initialInterval: 60s, maxInterval: 1s}"), ok),
			"/steps/x/retry/maxInterval", bindings.CodeInvalidOption,
		},
		{
			"cost", binding("iam.SendEmail", sendStep+
				charge("x", `, input: {v: "req.data.map(a, req.data.map(b, req.data.map(c, a + b + c)))"}`), ok),
			"/steps/x/input/v", bindings.CodeCost,
		},
		{
			"syntax", binding("iam.SendEmail", sendStep+charge("x", `, input: {v: "req."}`), ok),
			"/steps/x/input/v", bindings.CodeCEL,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			vs, err := bindings.ValidateBinding(mustBinding(t, tc.text), manifests(t))
			if err != nil {
				t.Fatal(err)
			}

			if !has(vs, tc.path, tc.code) {
				t.Fatalf("want %s %s, got %v", tc.path, tc.code, vs)
			}
		})
	}
}

// A CEL problem points into its expression: code points, end exclusive.
func TestViolationRanges(t *testing.T) {
	t.Parallel()

	cases := []struct {
		expr       string
		start, end int
	}{
		{"'é' + nope", 6, 10},
		{"req.to + req.nope", 9, 17},
		{"size(req.to) + ", 14, 15},
	}

	for _, tc := range cases {
		b := mustBinding(t, binding("iam.SendEmail", sendStep+`
  x: {activity: billing.Charge, input: {v: "`+strings.ReplaceAll(tc.expr, `"`, `\"`)+`"}}`, "{message_id: send.id}"))

		vs, err := bindings.ValidateBinding(b, manifests(t))
		if err != nil {
			t.Fatal(err)
		}

		i := slices.IndexFunc(vs, func(v bindings.Violation) bool { return v.Path == "/steps/x/input/v" })
		if i < 0 || vs[i].Expr != (bindings.Range{Start: tc.start, End: tc.end}) {
			t.Errorf("%q: want [%d,%d), got %+v", tc.expr, tc.start, tc.end, vs)
		}
	}
}

// A definition built directly is checked for what YAML cannot say.
func TestViolationsOfModel(t *testing.T) {
	t.Parallel()

	b := bindings.Binding{
		Hook: "iam.Audit",
		Steps: []bindings.Step{
			{Name: "w", Activity: "billing.Charge", Retry: bindings.Retry{Attempts: -1}, StartToClose: -time.Second},
		},
	}

	vs, err := bindings.ValidateBinding(b, manifests(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range [][2]string{
		{"/steps/w/retry/attempts", bindings.CodeInvalidOption},
		{"/steps/w/startToClose", bindings.CodeInvalidOption},
	} {
		if !has(vs, want[0], want[1]) {
			t.Errorf("want %s %s, got %v", want[0], want[1], vs)
		}
	}

	_, err = bindings.CompileBinding(b, manifests(t))

	var invalid *bindings.InvalidError
	if !errors.As(err, &invalid) || len(invalid.Violations) != len(vs) {
		t.Fatalf("compile: %v", err)
	}
}

// A rule's when sees event and meta; its steps see event.
func TestRuleViolations(t *testing.T) {
	t.Parallel()

	cat := manifests(t)

	r := mustRule(t, `
event: iam.UserRegistered
when: event.email != "" && meta.source != ""
steps:
  send: {activity: smtp.Send, input: {to: event.email, subject: "'welcome'", text: event.name}}
`)

	if vs, err := bindings.ValidateRule(r, cat); err != nil || len(vs) > 0 {
		t.Fatalf("valid rule: %v %v", vs, err)
	}

	for _, tc := range []struct{ text, path, code string }{
		{"event: iam.Nope\nsteps: {x: {activity: billing.Charge}}", "/event", bindings.CodeUnknownEvent},
		{"event: iam.UserRegistered\nwhen: event.nope\nsteps: {x: {activity: billing.Charge}}", "/when", bindings.CodeCEL},
		{"event: iam.UserRegistered\nwhen: x.a == 1\nsteps: {x: {activity: billing.Charge}}", "/when", bindings.CodeUnknownStep},
		{"event: iam.UserRegistered\nsteps: {x: {activity: billing.Charge, input: {v: req.a}}}", "/steps/x/input/v", bindings.CodeCEL},
		{"event: iam.UserRegistered\nwhen: event.name != ''", "/steps", bindings.CodeMissingField},
	} {
		vs, err := bindings.ValidateRule(mustRule(t, tc.text), cat)
		if err != nil {
			t.Fatal(err)
		}

		if !has(vs, tc.path, tc.code) {
			t.Errorf("%q: want %s %s, got %v", tc.text, tc.path, tc.code, vs)
		}
	}
}

// The analysis gives the editor levels, dependencies by kind, types and
// every read with its place — also for a definition that does not
// compile.
func TestAnalyze(t *testing.T) {
	t.Parallel()

	a, err := bindings.AnalyzeBinding(mustBinding(t, `
hook: iam.SendEmail
steps:
  render: {activity: template.Exec, input: {name: req.template}}
  send:
    activity: smtp.Send
    input: {to: req.to, subject: render.subject, text: "render.text + nope", tags: [req.to]}
    when: "!steps.render.skipped"
  audit: {activity: billing.Charge, after: [send]}
result: {message_id: send.id}
`), manifests(t))
	if err != nil {
		t.Fatal(err)
	}

	if !has(a.Violations, "/steps/send/input/text", bindings.CodeCEL) || len(a.Violations) != 1 {
		t.Fatalf("violations %v", a.Violations)
	}

	byName := map[string]bindings.StepAnalysis{}
	for _, s := range a.Steps {
		byName[s.Name] = s
	}

	send := byName["send"]
	if byName["render"].Level != 0 || send.Level != 1 || byName["audit"].Level != 2 ||
		!slices.Equal(send.Data, []string{"render"}) || !slices.Equal(send.When, []string{"render"}) ||
		!slices.Equal(byName["audit"].After, []string{"send"}) {
		t.Fatalf("steps %+v", a.Steps)
	}

	types := map[string]string{}
	for _, v := range a.Types {
		types[v.Path] = v.Type
	}

	for path, want := range map[string]string{
		"/steps/send/input": "object", "/steps/send/input/subject": "string", "/steps/send/input/tags": "list",
		"/steps/send/when": "bool", "/result/message_id": "string",
	} {
		if types[path] != want {
			t.Errorf("type of %s: %q, want %q", path, types[path], want)
		}
	}

	if _, typed := types["/steps/send/input/text"]; typed {
		t.Error("an expression that does not compile has no type")
	}

	var reads []string

	for _, r := range a.References {
		if r.Path == "/steps/send/input/text" || r.Path == "/steps/send/when" {
			reads = append(reads, r.Path+" "+r.Variable+"."+strings.Join(r.Fields, ".")+" "+
				strings.Repeat("_", r.Expr.Start)+strings.Repeat("^", r.Expr.End-r.Expr.Start))
		}
	}

	want := []string{
		"/steps/send/input/text render.text ^^^^^^^^^^^",
		"/steps/send/input/text nope. ______________^^^^",
		"/steps/send/when steps.render.skipped _^^^^^^^^^^^^^^^^^^^^",
	}
	if !slices.Equal(reads, want) {
		t.Fatalf("reads\n%s\nwant\n%s", strings.Join(reads, "\n"), strings.Join(want, "\n"))
	}
}

func has(vs []bindings.Violation, path, code string) bool {
	return slices.ContainsFunc(vs, func(v bindings.Violation) bool {
		return v.Path == path && v.Code == code && strings.TrimSpace(v.Message) != ""
	})
}
