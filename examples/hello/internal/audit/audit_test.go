package audit_test

import (
	"errors"
	"testing"

	"github.com/backplane-labs/backplane/examples/hello/internal/audit"
	"github.com/backplane-labs/backplane/examples/hello/internal/greeter"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/event"
)

func TestAudit(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t, backplanetest.Name("hello"))
	a := audit.New(h.Root(), event.Declare[greeter.Greeted](h.Root(), "Greeted"))
	h.Start()

	subs := h.Manifest().GetSubscriptions()
	if len(subs) != 1 || subs[0].GetEvent() != "hello.Greeted" || subs[0].GetConsumer() != audit.Consumer {
		t.Fatalf("subscriptions: %v", subs)
	}

	// Pinned: found by its event from the component's path.
	ann := greeter.Greeted{Name: "ann", Count: 1}
	if err := backplanetest.React(t.Context(), h, "audit", "hello.Greeted", ann, backplanetest.WithID("e-1")); err != nil {
		t.Fatal(err)
	}

	// A redelivery of the same event is counted once.
	if err := backplanetest.ReactConsumer(t.Context(), h, audit.Consumer, ann,
		backplanetest.WithID("e-1"), backplanetest.WithAttempt(2)); err != nil {
		t.Fatal(err)
	}

	if err := backplanetest.ReactConsumer(t.Context(), h, audit.Consumer, greeter.Greeted{Name: "bob"},
		backplanetest.WithKey("bob"), backplanetest.WithHeader("excited", "true")); err != nil {
		t.Fatal(err)
	}

	if a.Count("ann") != 1 || a.Count("bob") != 1 || a.Total() != 2 {
		t.Fatalf("counts: ann %d bob %d total %d", a.Count("ann"), a.Count("bob"), a.Total())
	}

	// No retry fixes a nameless greeting: it is dead-lettered at once.
	if err := backplanetest.ReactConsumer(t.Context(), h, audit.Consumer, greeter.Greeted{}); !errors.Is(err, audit.ErrNoName) {
		t.Fatalf("nameless: %v", err)
	}
}
