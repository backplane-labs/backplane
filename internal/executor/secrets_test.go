package executor_test

import (
	"encoding/json"
	"testing"

	historypb "go.temporal.io/api/history/v1"

	sp "github.com/gopherex/schemapb/go/schemapb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/executor"
)

type (
	otpIn struct {
		To   string `json:"to"`
		Code string `json:"code" schemapb:"secret=true"`
	}
	smsIn struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	vaultOut struct {
		Secret string `json:"secret" schemapb:"secret=true"`
	}
)

func schemaOf[T any](t *testing.T) *sp.Schema {
	t.Helper()

	s, err := sp.ReflectType[T](sp.ID(sp.Namespace("test"), sp.SchemaName("payload"), sp.Ver(1, 0, 0)))
	if err != nil {
		t.Fatal(err)
	}

	return s
}

// The timeline hides what the program's schemas mark secret: a step's
// input computed from a secret, an output the activity declares secret,
// and an undo that runs on that output; the rest shows.
func TestTimelineSecrets(t *testing.T) {
	t.Parallel()

	manifests := bindings.Manifests{
		{Service: "iam", Version: "1.0.0", Hooks: []*backplanev1.Hook{{Name: "SendOTP", Input: schemaOf[otpIn](t)}}},
		{Service: "sms", Version: "1.0.0", Activities: []*backplanev1.Activity{{
			Name: "Send", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY, Input: schemaOf[smsIn](t),
		}}},
		{Service: "vault", Version: "1.0.0", Activities: []*backplanev1.Activity{
			{Name: "Read", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY, Output: schemaOf[vaultOut](t)},
			{Name: "Forget", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY},
		}},
	}

	b, err := bindings.BindingYAML(`
hook: iam.SendOTP
steps:
  read: {activity: vault.Read, undo: vault.Forget}
  send: {activity: sms.Send, input: {to: req.to, text: req.code}}
  note: {activity: sms.Send, input: {to: req.to, text: req.to}}
`)
	if err != nil {
		t.Fatal(err)
	}

	p, err := bindings.CompileBinding(b, manifests)
	if err != nil {
		t.Fatal(err)
	}

	prog, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	events := []*historypb.HistoryEvent{
		scheduled(t, 5, "5", "read", "vault.Read", `{}`),
		completedEvent(t, 6, 5, `{"secret":"s"}`),
		scheduled(t, 7, "7", "send", "sms.Send", `{"to":"a","text":"1234"}`),
		completedEvent(t, 8, 7, `{"id":"m1"}`),
		scheduled(t, 9, "9", "note", "sms.Send", `{"to":"a","text":"a"}`),
		scheduled(t, 10, "10", "read", "vault.Forget", `{"secret":"s"}`),
	}

	got := executor.TimelineOf(prog, events, nil)
	byRow := map[string]*consolev1.StepRun{}

	for _, run := range got {
		key := run.GetStep()
		if run.GetUndo() {
			key += "/undo"
		}

		byRow[key] = run
	}

	for key, want := range map[string][2]string{
		"read":      {`{}`, `{"secret":"***"}`},
		"send":      {`{"text":"***","to":"a"}`, `{"id":"m1"}`},
		"note":      {`{"to":"a","text":"a"}`, ``},
		"read/undo": {`{"secret":"***"}`, ``},
	} {
		run := byRow[key]
		if run == nil {
			t.Fatalf("%s: no row in %v", key, got)
		}

		if run.GetInput() != want[0] || run.GetOutput() != want[1] {
			t.Errorf("%s: input %s output %s", key, run.GetInput(), run.GetOutput())
		}
	}
}
