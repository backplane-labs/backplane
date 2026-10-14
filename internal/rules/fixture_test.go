package rules_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	sp "github.com/gopherex/schemapb/go/schemapb"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/wire"
)

type registered struct {
	Email string `json:"email"`
	Plan  string `json:"plan"`
}

func schemaOf[T any](t *testing.T, service, name string) *sp.Schema {
	t.Helper()

	s, err := sp.ReflectType[T](sp.ID(sp.Namespace(service), sp.SchemaName(name), sp.Ver(1, 0, 0)))
	if err != nil {
		t.Fatalf("reflect %s/%s: %v", service, name, err)
	}

	return s
}

// catalog: iam and crm publish UserRegistered; billing implements Charge.
func catalog(t *testing.T) bindings.Manifests {
	t.Helper()

	return bindings.Manifests{
		{
			Service: "iam", Version: "1.0.0",
			Events: []*backplanev1.Event{{Name: "UserRegistered", Schema: schemaOf[registered](t, "iam", "UserRegistered")}},
		},
		{
			Service: "crm", Version: "1.0.0",
			Events: []*backplanev1.Event{{Name: "UserRegistered", Schema: schemaOf[registered](t, "crm", "UserRegistered")}},
		},
		{
			Service: "billing", Version: "1.0.0",
			Activities: []*backplanev1.Activity{{Name: "Charge", Kind: backplanev1.ActivityKind_ACTIVITY_KIND_ACTIVITY}},
		},
	}
}

// proRule charges users registering on the pro plan.
func proRule(event string) bindings.Rule {
	return bindings.Rule{
		Event: event, When: `event.plan == "pro"`,
		Steps: []bindings.Step{{Name: "charge", Activity: "billing.Charge"}},
	}
}

func compile(t *testing.T, r bindings.Rule) *bindings.Program {
	t.Helper()

	p, err := bindings.CompileRule(r, catalog(t))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	return p
}

func entry(id uuid.UUID, version int64, r bindings.Rule) bindings.RuleEntry {
	return bindings.RuleEntry{ID: id, Current: bindings.RuleVersion{RuleID: id, Version: version, Name: "r", Rule: r}}
}

// headers of an event as the SDK publishes it.
func headers(id string) nats.Header {
	h := nats.Header{}
	h.Set(wire.HeaderSpecVersion, wire.SpecVersion)
	h.Set(wire.HeaderSource, "iam")
	h.Set(wire.HeaderType, "iam.UserRegistered")
	h.Set(wire.HeaderTime, "2026-09-29T10:00:00.5Z")
	h.Set(wire.HeaderSubject, "user-1")
	h.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")

	if id != "" {
		h.Set(wire.HeaderID, id)
	}

	return h
}

// fakeMsg is a delivery that records how it was settled.
type fakeMsg struct {
	data      []byte
	h         nats.Header
	delivered uint64

	acks, naks, terms int
	delay             time.Duration
	reason            string
}

func (m *fakeMsg) Data() []byte         { return m.data }
func (m *fakeMsg) Headers() nats.Header { return m.h }
func (m *fakeMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{NumDelivered: m.delivered}, nil
}
func (m *fakeMsg) Ack() error { m.acks++; return nil }
func (m *fakeMsg) Nak() error { m.naks++; return nil }
func (m *fakeMsg) NakWithDelay(d time.Duration) error {
	m.naks++
	m.delay = d

	return nil
}

func (m *fakeMsg) TermWithReason(r string) error {
	m.terms++
	m.reason = r

	return nil
}

// fakeStarter records starts and fails with err.
type fakeStarter struct {
	err   error
	calls []startCall
}

type startCall struct {
	id, workflowID string
	version        int64
	event          []byte
	meta           bindings.Meta
	trace          map[string]string
}

func (s *fakeStarter) StartRule(
	_ context.Context, id string, version int64, _ *bindings.Program, event []byte,
	meta bindings.Meta, trace map[string]string, workflowID string,
) error {
	s.calls = append(s.calls, startCall{id: id, workflowID: workflowID, version: version, event: event, meta: meta, trace: trace})

	return s.err
}
