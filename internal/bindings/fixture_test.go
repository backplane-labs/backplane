package bindings_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
)

// Payloads of the fixture services, as their authors declare them: the
// SDK reflects these types into the manifests' schemas.
type (
	email struct {
		To         string            `json:"to"`
		Template   string            `json:"template"`
		Data       map[string]string `json:"data,omitempty"`
		Priority   int64             `json:"priority,omitempty"`
		Recipients []recipient       `json:"recipients,omitempty"`
	}
	recipient struct {
		Email string `json:"email"`
		Name  string `json:"name,omitempty"`
	}
	sent struct {
		MessageID string `json:"message_id"`
	}
	registered struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	execIn struct {
		Name string            `json:"name"`
		Data map[string]string `json:"data,omitempty"`
	}
	execOut struct {
		Subject string `json:"subject"`
		Text    string `json:"text"`
	}
	sendIn struct {
		To       string   `json:"to"`
		Subject  string   `json:"subject"`
		Text     string   `json:"text"`
		Priority int64    `json:"priority,omitempty"`
		Weight   float64  `json:"weight,omitempty"`
		Tags     []string `json:"tags,omitempty"`
	}
	sendOut struct {
		ID   string `json:"id"`
		Size int64  `json:"size"`
	}
	recallIn struct {
		ID string `json:"id"`
	}
)

func schemaOf[T any](t testing.TB, service, name string) *sp.Schema {
	t.Helper()

	s, err := sp.ReflectType[T](sp.ID(sp.Namespace(service), sp.SchemaName(name), sp.Ver(1, 0, 0)))
	if err != nil {
		t.Fatalf("reflect %s/%s: %v", service, name, err)
	}

	return s
}

// manifests is an installation: iam calls SendEmail and publishes
// UserRegistered; template, smtp and billing implement activities
// (billing without schemas).
func manifests(t testing.TB) bindings.Manifests {
	t.Helper()

	return bindings.Manifests{
		{
			Service: "iam", Version: "1.0.0",
			Hooks: []*backplanev1.Hook{{
				Name: "SendEmail", Required: true,
				Input:  schemaOf[email](t, "iam", "SendEmail.input"),
				Output: schemaOf[sent](t, "iam", "SendEmail.output"),
			}, {Name: "Audit"}},
			Events: []*backplanev1.Event{{Name: "UserRegistered", Schema: schemaOf[registered](t, "iam", "UserRegistered")}},
		},
		{
			Service: "template", Version: "1.0.0",
			Activities: []*backplanev1.Activity{{
				Name: "Exec", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY,
				Input:        schemaOf[execIn](t, "template", "Exec.input"),
				Output:       schemaOf[execOut](t, "template", "Exec.output"),
				StartToClose: durationpb.New(5 * time.Second), Retry: &backplanev1.ActivityRetry{Attempts: 2},
			}},
		},
		{
			Service: "smtp", Version: "1.0.0",
			Activities: []*backplanev1.Activity{{
				Name: "Send", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY,
				Input:     schemaOf[sendIn](t, "smtp", "Send.input"),
				Output:    schemaOf[sendOut](t, "smtp", "Send.output"),
				Heartbeat: durationpb.New(3 * time.Second),
			}, {
				Name: "Recall", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_WORKFLOW,
				Input: schemaOf[recallIn](t, "smtp", "Recall.input"),
			}},
		},
		{
			Service: "billing", Version: "1.0.0",
			Activities: []*backplanev1.Activity{{Name: "Charge", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY}},
		},
	}
}

// sendEmail is §7.1's example.
const sendEmail = `
hook: iam.SendEmail
steps:
  render:
    activity: template.Exec
    input: {name: req.template, data: req.data}
  send:
    activity: smtp.Send
    input: {to: req.to, subject: render.subject, text: render.text}
result: {message_id: send.id}
`

func mustBinding(t testing.TB, text string) bindings.Binding {
	t.Helper()

	b, err := bindings.BindingYAML(text)
	if err != nil {
		t.Fatalf("binding: %v", err)
	}

	return b
}

func mustRule(t testing.TB, text string) bindings.Rule {
	t.Helper()

	r, err := bindings.RuleYAML(text)
	if err != nil {
		t.Fatalf("rule: %v", err)
	}

	return r
}

func mustCompile(t testing.TB, b bindings.Binding) *bindings.Program {
	t.Helper()

	p, err := bindings.CompileBinding(b, manifests(t))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	return p
}
