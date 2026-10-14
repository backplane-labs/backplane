package rules_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/serviceerror"

	"github.com/backplane-labs/backplane/internal/rules"
)

func engineWith(s rules.Starter) *rules.Engine { return rules.Detached(nil, rules.WithStarter(s)) }

func deliver(
	ctx context.Context, t *testing.T, e *rules.Engine, data, id string, delivered uint64,
) (rules.Outcome, *fakeMsg) {
	t.Helper()

	msg := &fakeMsg{data: []byte(data), h: headers(id), delivered: delivered}

	return e.Handle(ctx, ruleUUID, compile(t, proRule("iam.UserRegistered")), 3, msg), msg
}

var ruleUUID = uuid.MustParse("0b6f3b53-6d7c-4e27-9a55-5c8b0c2f4a10")

// A matching event starts rule/<id>/<ce-id> with the event, its
// attributes, the version and the trace, and is acked after the start.
func TestHandleStartsAndAcks(t *testing.T) {
	t.Parallel()

	s := &fakeStarter{}
	out, msg := deliver(t.Context(), t, engineWith(s), `{"email":"a@b.c","plan":"pro"}`, "ev-1", 1)

	if out != rules.OutcomeStarted || msg.acks != 1 || msg.naks+msg.terms != 0 {
		t.Fatalf("outcome %s, settled %+v", out, msg)
	}

	if len(s.calls) != 1 {
		t.Fatalf("starts: %d", len(s.calls))
	}

	c := s.calls[0]
	if c.workflowID != "rule/"+ruleUUID.String()+"/ev-1" || c.id != ruleUUID.String() || c.version != 3 {
		t.Fatalf("start: %+v", c)
	}

	want := time.Date(2026, 9, 29, 10, 0, 0, 5e8, time.UTC)
	if c.meta.ID != "ev-1" || c.meta.Source != "iam" || c.meta.Type != "iam.UserRegistered" ||
		c.meta.Subject != "user-1" || !c.meta.Time.Equal(want) {
		t.Fatalf("meta: %+v", c.meta)
	}

	if c.trace["traceparent"] == "" || string(c.event) != `{"email":"a@b.c","plan":"pro"}` {
		t.Fatalf("trace %v, event %s", c.trace, c.event)
	}
}

// when false: acked, no run.
func TestHandleSkips(t *testing.T) {
	t.Parallel()

	s := &fakeStarter{}
	out, msg := deliver(t.Context(), t, engineWith(s), `{"plan":"free"}`, "ev-2", 1)

	if out != rules.OutcomeSkipped || msg.acks != 1 || len(s.calls) != 0 {
		t.Fatalf("outcome %s, settled %+v, starts %d", out, msg, len(s.calls))
	}
}

// A run that exists already (a redelivery, a republish) is a duplicate:
// acked — whether the starter says ErrAlreadyStarted or Temporal's error.
func TestHandleDedup(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		rules.ErrAlreadyStarted,
		&serviceerror.WorkflowExecutionAlreadyStarted{Message: "exists"},
	} {
		out, msg := deliver(t.Context(), t, engineWith(&fakeStarter{err: err}), `{"plan":"pro"}`, "ev-3", 2)

		if out != rules.OutcomeDedup || msg.acks != 1 || msg.naks+msg.terms != 0 {
			t.Fatalf("%v: outcome %s, settled %+v", err, out, msg)
		}
	}
}

// A start that fails is redelivered with a delay growing with the
// deliveries; not acked.
func TestHandleStartFailureNaks(t *testing.T) {
	t.Parallel()

	out, msg := deliver(t.Context(), t, engineWith(&fakeStarter{err: errors.New("temporal down")}),
		`{"plan":"pro"}`, "ev-4", 3)

	if out != rules.OutcomeFailed || msg.naks != 1 || msg.acks+msg.terms != 0 || msg.delay != 4*time.Second {
		t.Fatalf("outcome %s, settled %+v", out, msg)
	}
}

// A start the stop interrupted is returned at once (plain nak).
func TestHandleStopNaksPlain(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	out, msg := deliver(ctx, t, engineWith(&fakeStarter{err: context.Canceled}), `{"plan":"pro"}`, "ev-5", 1)

	if out != rules.OutcomeFailed || msg.naks != 1 || msg.delay != 0 {
		t.Fatalf("outcome %s, settled %+v", out, msg)
	}
}

// A bad event — not JSON, no ce-id — is terminated, never started.
func TestHandleInvalidTerms(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, data, id string }{
		{"not json", `{"plan":`, "ev-6"},
		{"no ce-id", `{"plan":"pro"}`, ""},
	} {
		s := &fakeStarter{}
		out, msg := deliver(t.Context(), t, engineWith(s), tc.data, tc.id, 1)

		if out != rules.OutcomeInvalid || msg.terms != 1 || msg.acks+msg.naks != 0 || msg.reason == "" || len(s.calls) != 0 {
			t.Fatalf("%s: outcome %s, settled %+v", tc.name, out, msg)
		}
	}
}

func TestNakDelay(t *testing.T) {
	t.Parallel()

	for n, want := range map[uint64]time.Duration{
		0: time.Second, 1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 7: time.Minute, 100: time.Minute,
	} {
		if got := rules.NakDelay(n); got != want {
			t.Errorf("nakDelay(%d) = %v, want %v", n, got, want)
		}
	}
}
