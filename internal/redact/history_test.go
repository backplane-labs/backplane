package redact_test

import (
	"encoding/json"
	"testing"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/redact"
	"github.com/backplane-labs/backplane/internal/wire"
)

// catalog is the fixture installation as the console's redactor sees it,
// plus a declared workflow.
func catalog(t *testing.T) redact.Manifests {
	t.Helper()

	manifests := installation(t)
	manifests = append(manifests, &backplanev1.Manifest{
		Service: "flows", Version: "1.0.0",
		Workflows: []*backplanev1.Workflow{{Name: "Welcome", Input: schemaOf[user](t), Output: schemaOf[vaultOut](t)}},
	})

	return redact.Manifests(func(service string) *backplanev1.Manifest {
		for _, m := range manifests {
			if m.GetService() == service {
				return m
			}
		}

		return nil
	})
}

// A binding run: the start's payload is masked by the hook's input, each
// step's call by its effective schemas, a second call of a label as its
// undo, the result by what it reads.
func TestHistoryBindingRun(t *testing.T) {
	t.Parallel()

	b, err := bindings.BindingYAML(`
hook: iam.SendOTP
steps:
  read: {activity: vault.Read, undo: audit.Log}
  send: {activity: sms.Send, input: {to: req.to, text: req.code}}
result: {delivered: true, echo: req.code}
`)
	if err != nil {
		t.Fatal(err)
	}

	prog, err := bindings.CompileBinding(b, installation(t))
	if err != nil {
		t.Fatal(err)
	}

	program, err := json.Marshal(prog)
	if err != nil {
		t.Fatal(err)
	}

	input := `{"kind":1,"program":` + string(program) + `,"payload":{"to":"a","code":"1"},"identity":{"hook":"iam.SendOTP"}}`

	h := redact.NewHistory(catalog(t))

	var started map[string]json.RawMessage
	if err := json.Unmarshal([]byte(h.Started(wire.BindingWorkflow, "backplane", input)), &started); err != nil {
		t.Fatal(err)
	}

	if string(started["payload"]) != `{"code":"***","to":"a"}` || string(started["program"]) != string(program) {
		t.Errorf("started payload %s", started["payload"])
	}

	call := `{"activity":"sms.Send","payload":{"to":"a","text":"1"},"step":"send"}`
	if got := h.Scheduled(5, call); got != `{"activity":"sms.Send","payload":{"text":"***","to":"a"},"step":"send"}` {
		t.Errorf("scheduled: %s", got)
	}

	read := `{"activity":"vault.Read","payload":{},"step":"read"}`
	if got := h.Scheduled(7, read); got != read {
		t.Errorf("read: %s", got)
	}

	if got := h.Completed(7, `{"payload":{"secret":"s","label":"l"}}`); got != `{"payload":{"label":"l","secret":"***"}}` {
		t.Errorf("read completed: %s", got)
	}

	undo := `{"activity":"audit.Log","payload":{"secret":"s","label":"l"},"step":"read"}`
	if got := h.Scheduled(9, undo); got != `{"activity":"audit.Log","payload":{"label":"l","secret":"***"},"step":"read"}` {
		t.Errorf("undo: %s", got)
	}

	if got := h.Completed(99, `{"payload":{"x":1}}`); got != `{"payload":{"x":1}}` {
		t.Errorf("unknown call: %s", got)
	}

	if got := h.Result(`{"payload":{"delivered":true,"echo":"1"}}`); got != `{"payload":{"delivered":true,"echo":"***"}}` {
		t.Errorf("result: %s", got)
	}
}

// A hook call from the console or a service's workflow (Nexus) and an
// activity run are masked by the manifests; a declared workflow by its
// own schemas; anything else passes.
func TestHistoryOtherRuns(t *testing.T) {
	t.Parallel()

	h := redact.NewHistory(catalog(t))

	hook := `{"hook":"iam.SendOTP","instance":"console","payload":{"to":"a","code":"1"}}`
	if got := h.Started("backplane.console.CallHook.v1", "backplane", hook); got !=
		`{"hook":"iam.SendOTP","instance":"console","payload":{"code":"***","to":"a"}}` {
		t.Errorf("hook call: %s", got)
	}

	if got := h.Result(`{"payload":{"delivered":true,"echo":"x"}}`); got != `{"payload":{"delivered":true,"echo":"x"}}` {
		t.Errorf("hook result: %s", got)
	}

	if got := h.Nexus(3, hook); got != `{"hook":"iam.SendOTP","instance":"console","payload":{"code":"***","to":"a"}}` {
		t.Errorf("nexus: %s", got)
	}

	h = redact.NewHistory(catalog(t))

	act := `{"activity":"vault.Read","payload":{},"step":"console"}`
	if got := h.Started("backplane.console.RunActivity.v1", "backplane", act); got != act {
		t.Errorf("activity run: %s", got)
	}

	if got := h.Result(`{"payload":{"secret":"s","label":"l"}}`); got != `{"payload":{"label":"l","secret":"***"}}` {
		t.Errorf("activity result: %s", got)
	}

	h = redact.NewHistory(catalog(t))

	if got := h.Started("Welcome", "flows", `{"name":"n","token":"t"}`); got != `{"name":"n","token":"***"}` {
		t.Errorf("workflow input: %s", got)
	}

	if got := h.Result(`{"secret":"s","label":"l"}`); got != `{"label":"l","secret":"***"}` {
		t.Errorf("workflow result: %s", got)
	}

	if got := h.Scheduled(4, `{"activity":"vault.Read","payload":{}}`); got != `{"activity":"vault.Read","payload":{}}` {
		t.Errorf("activity scheduled: %s", got)
	}

	if got := h.Completed(4, `{"payload":{"secret":"s"}}`); got != `{"payload":{"secret":"***"}}` {
		t.Errorf("activity completed: %s", got)
	}

	h = redact.NewHistory(catalog(t))

	for _, text := range []string{"", "null", `"text"`, `[1]`, `{"other":{"token":"t"}}`} {
		if got := h.Started("Unknown", "nowhere", text); got != text {
			t.Errorf("%q: %s", text, got)
		}

		if got := h.Scheduled(1, text); got != text {
			t.Errorf("%q scheduled: %s", text, got)
		}
	}

	if got := redact.NewHistory(nil).Started("Welcome", "flows", `{"token":"t"}`); got != `{"token":"t"}` {
		t.Errorf("no catalog: %s", got)
	}
}

func TestEventSchema(t *testing.T) {
	t.Parallel()

	cat := catalog(t)

	if got := string(redact.Mask(redact.EventSchema(cat, "iam.LoggedIn"), []byte(`{"token":"t"}`))); got != `{"token":"***"}` {
		t.Errorf("event: %s", got)
	}

	if redact.EventSchema(cat, "iam.Nope") != nil || redact.EventSchema(nil, "iam.LoggedIn") != nil {
		t.Error("unknown events have no schema")
	}
}
