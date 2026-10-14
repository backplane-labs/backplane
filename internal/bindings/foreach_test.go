package bindings_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/backplane-labs/backplane/internal/bindings"
)

const forEachBinding = `
hook: iam.SendEmail
steps:
  render: {activity: template.Exec, input: {name: req.template}}
  notify:
    forEach: req.recipients
    as: who
    when: who.email != ""
    concurrency: 2
    onError: continue
    activity: smtp.Send
    input: {to: who.email, subject: render.subject, text: "'#' + string(whoIndex)"}
  offboard:
    forEach: req.data
    steps:
      charge: {activity: billing.Charge, input: {key: item.key}}
      recall: {activity: smtp.Recall, input: {id: charge.id + item.value}}
    result: {key: item.key, id: recall.id}
result: {message_id: "string(notify.size()) + '/' + string(steps.notify.failed)"}
`

// A for-each step types its item by the list's schema, waits for what its
// list and body read, and its output is a list for the steps after it.
func TestCompileForEach(t *testing.T) {
	t.Parallel()

	b := mustBinding(t, forEachBinding)

	a, err := bindings.AnalyzeBinding(b, manifests(t))
	if err != nil || len(a.Violations) > 0 {
		t.Fatalf("analysis: %v %v", a.Violations, err)
	}

	steps := map[string]bindings.StepAnalysis{}
	for _, s := range a.Steps {
		steps[s.Parent+"/"+s.Name] = s
	}

	notify := steps["/notify"]
	if notify.ItemType != "object" || !slices.Equal(notify.Data, []string{"render"}) || notify.Level != 1 {
		t.Fatalf("notify %+v", notify)
	}

	if offboard := steps["/offboard"]; offboard.ItemType != "map(string, dyn)" || offboard.Level != 0 {
		t.Fatalf("offboard %+v", offboard)
	}

	if recall := steps["offboard/recall"]; !slices.Equal(recall.Data, []string{"charge"}) || recall.Level != 1 {
		t.Fatalf("recall %+v", recall)
	}

	p := mustCompile(t, b)

	plan, _ := p.Step("notify")
	if plan.ForEach == nil || plan.ForEach.Item != "who" || plan.ForEach.Concurrency != 2 || !plan.ForEach.Continue ||
		plan.ForEach.MaxItems != bindings.DefaultMaxItems || plan.ForEach.Steps {
		t.Fatalf("notify plan %+v %+v", plan, plan.ForEach)
	}

	if body, ok := p.Body("offboard"); !ok || len(body.Steps()) != 2 {
		t.Fatalf("offboard body %v", ok)
	}

	// The program round-trips with its bodies.
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var back bindings.Program
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}

	if _, ok := back.Body("offboard"); !ok {
		t.Fatal("body lost in JSON")
	}
}

func TestForEachViolations(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, steps, path, code string }{
		{"not a list", `
  x: {forEach: req.to, activity: billing.Charge}`, "/steps/x/forEach", bindings.CodeTypeMismatch},
		{"options without forEach", `
  x: {activity: billing.Charge, concurrency: 3}`, "/steps/x/concurrency", bindings.CodeInvalidOption},
		{"activity and steps", `
  x: {forEach: req.recipients, activity: billing.Charge, steps: {y: {activity: billing.Charge}}}`, "/steps/x/steps", bindings.CodeInvalidOption},
		{"unknown on error", `
  x: {forEach: req.recipients, activity: billing.Charge, onError: ignore}`, "/steps/x/onError", bindings.CodeInvalidOption},
		{"item takes a step's name", `
  x: {forEach: req.recipients, as: y, activity: billing.Charge}
  y: {activity: billing.Charge}`, "/steps/x/as", bindings.CodeInvalidName},
		{"body step takes an outer name", `
  y: {activity: billing.Charge}
  x: {forEach: req.recipients, steps: {y: {activity: billing.Charge}}}`, "/steps/x/steps/y", bindings.CodeInvalidName},
		{"body reads an unknown field of the item", `
  x: {forEach: req.recipients, activity: billing.Charge, input: {v: item.nope}}`, "/steps/x/input/v", bindings.CodeCEL},
		{"body step input", `
  x: {forEach: req.recipients, steps: {y: {activity: smtp.Send, input: {to: 1}}}}`, "/steps/x/steps/y/input/to", bindings.CodeTypeMismatch},
		{"cycle in a body", `
  x: {forEach: req.recipients, steps: {y: {activity: billing.Charge, input: {v: z}}, z: {activity: billing.Charge, input: {v: y}}}}`, "/steps/x/steps/y", bindings.CodeCycle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			vs, err := bindings.ValidateBinding(mustBinding(t, "hook: iam.SendEmail\nsteps:"+tc.steps), manifests(t))
			if err != nil {
				t.Fatal(err)
			}

			if !has(vs, tc.path, tc.code) {
				t.Fatalf("want %s %s, got %v", tc.path, tc.code, vs)
			}
		})
	}
}

// Items of a list or a map, each item's scope, a body's scope and result,
// and the step's list and state for what follows.
func TestEvalForEach(t *testing.T) {
	t.Parallel()

	p := mustCompile(t, mustBinding(t, forEachBinding))

	s, err := p.Start([]byte(`{"to":"a","template":"t","data":{"b":"2","a":"1"},"recipients":[{"email":"x@y"},{"email":""}]}`))
	if err != nil {
		t.Fatal(err)
	}

	if s, err = p.Bind(s, "render", []byte(`{"subject":"Hi","text":"t"}`)); err != nil {
		t.Fatal(err)
	}

	items, err := p.Items("notify", s)
	if err != nil || len(items) != 2 {
		t.Fatalf("items %v %v", items, err)
	}

	first, err := p.ItemScope("notify", s, 0, items[0])
	if err != nil {
		t.Fatal(err)
	}

	expect(t)(p.Input("notify", first))(`{"subject":"Hi","text":"#0","to":"x@y"}`)

	second, _ := p.ItemScope("notify", s, 1, items[1])
	if run, err := p.When("notify", second); err != nil || run {
		t.Fatalf("when of an empty email: %v %v", run, err)
	}

	s, err = p.BindItems(s, "notify", [][]byte{[]byte(`{"id":"m-1","size":1}`), nil},
		[]bindings.ItemError{{Index: 1, Message: "refused"}})
	if err != nil {
		t.Fatal(err)
	}

	byKey, err := p.Items("offboard", s)
	if err != nil || len(byKey) != 2 || byKey[0].(map[string]any)["key"] != "a" {
		t.Fatalf("map items %v %v", byKey, err)
	}

	body, _ := p.Body("offboard")
	item, _ := p.ItemScope("offboard", s, 0, byKey[0])
	bodyScope := body.Begin(item)

	if bodyScope, err = body.Bind(bodyScope, "charge", []byte(`{"id":"c"}`)); err != nil {
		t.Fatal(err)
	}

	expect(t)(body.Input("recall", bodyScope))(`{"id":"c1"}`)

	if bodyScope, err = body.Bind(bodyScope, "recall", []byte(`{"id":"r"}`)); err != nil {
		t.Fatal(err)
	}

	expect(t)(body.ItemOutput(bodyScope))(`{"id":"r","key":"a"}`)

	if s, err = p.BindItems(s, "offboard", [][]byte{[]byte(`{"id":"r","key":"a"}`), []byte(`{}`)}, nil); err != nil {
		t.Fatal(err)
	}

	expect(t)(p.Result(s))(`{"message_id":"2/1"}`)
}

func TestParseCallLabel(t *testing.T) {
	t.Parallel()

	for label, want := range map[string]struct {
		step, parent string
		item         int
	}{
		"send":                {"send", "", -1},
		"notify[3]":           {"notify", "", 3},
		"offboard[2].recall":  {"recall", "offboard", 2},
		"outer[1].inner[4]":   {"inner", "outer", 4},
		"outer[1].inner[4].x": {"x", "outer/inner", 4},
		"odd[":                {"odd[", "", -1},
	} {
		got := bindings.ParseCallLabel(label)
		if got.Step != want.step || got.Parent != want.parent || got.Item != want.item {
			t.Errorf("%s: %+v, want %+v", label, got, want)
		}
	}
}
