package activity_test

import (
	"context"
	"errors"
	"fmt"
	"time"
	"strings"
	"testing"

	sp "github.com/gopherex/schemapb/go/schemapb"

	"github.com/backplane-labs/backplane/pkg/backplane/activity"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

type (
	Order struct {
		Items int `json:"items"`
	}
	Receipt struct {
		Total int `json:"total"`
	}
)

var errEmpty = errors.New("empty order")

func charge(_ context.Context, in Order) (Receipt, error) {
	if in.Items == 0 {
		return Receipt{}, errEmpty
	}

	return Receipt{Total: in.Items * 10}, nil
}

func TestHandleRecordsAndInvokes(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	activity.Handle(deps.NewComponent(h.Root(), "billing"), "Charge", charge)

	acts := h.Manifest().GetActivities()
	if len(acts) != 1 || acts[0].GetName() != "Charge" || acts[0].GetInput() == nil || acts[0].GetOutput() == nil {
		t.Fatalf("activities: %v", acts)
	}

	h.Start()

	out, err := backplanetest.Activity[Order, Receipt](t.Context(), h, "Charge", Order{Items: 3})
	if err != nil || out.Total != 30 {
		t.Fatalf("invoke: %v %v", out, err)
	}

	if _, err := backplanetest.Activity[Order, Receipt](t.Context(), h, "Charge", Order{}); !errors.Is(err, errEmpty) {
		t.Fatalf("handler error: %v", err)
	}

	if _, err := backplanetest.Activity[Order, Receipt](t.Context(), h, "Refund", Order{}); err == nil {
		t.Fatal("invoked an undeclared activity")
	}
}

// A payload the handler cannot decode is an error, not a zero request.
func TestHandleRejectsBadPayload(t *testing.T) {
	t.Parallel()

	h := backplanetest.New(t)
	activity.Handle(h.Root(), "Charge", charge)
	h.Start()

	_, err := backplanetest.Activity[string, Receipt](t.Context(), h, "Charge", "not an order")
	if err == nil || !strings.Contains(err.Error(), "activity Charge: decode") {
		t.Fatalf("bad payload: %v", err)
	}
}

func TestDeclareOnZeroScopePanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r, _ := recover().(string); !strings.Contains(r, "activity Charge declared on a zero scope") {
			t.Fatalf("panic %q", r)
		}
	}()

	activity.Handle(deps.Component{}, "Charge", charge)
}

// A schema known at run time is recorded under the service's name for it;
// the handler gets and answers JSON.
func TestHandleSchema(t *testing.T) {
	t.Parallel()

	in, err := sp.ReflectType[Order](sp.ID(sp.Namespace("plugin"), sp.SchemaName("order"), sp.Ver(1, 0, 0)))
	if err != nil {
		t.Fatal(err)
	}

	h := backplanetest.New(t)
	activity.HandleSchema(h.Root(), "Relay", in, nil, func(_ context.Context, raw []byte) ([]byte, error) {
		return []byte(`{"total":` + strings.TrimSuffix(strings.TrimPrefix(string(raw), `{"items":`), "}") + `}`), nil
	})

	acts := h.Manifest().GetActivities()
	if len(acts) != 1 || acts[0].GetInput() == nil || acts[0].GetOutput() != nil ||
		len(acts[0].GetInput().GetFields()) != 1 || !strings.Contains(acts[0].GetInput().GetId().GetName(), "Relay") {
		t.Fatalf("activities: %v", acts)
	}

	h.Start()

	out, err := backplanetest.Activity[Order, Receipt](t.Context(), h, "Relay", Order{Items: 4})
	if err != nil || out.Total != 4 {
		t.Fatalf("invoke: %v %v", out, err)
	}
}

func TestErrorMarks(t *testing.T) {
	t.Parallel()

	base := errors.New("x")

	if !activity.IsNonRetryable(fmt.Errorf("wrapped: %w", activity.NonRetryable(base))) || activity.IsNonRetryable(base) {
		t.Fatal("non-retryable")
	}

	if d, ok := activity.RetryAfterOf(fmt.Errorf("w: %w", activity.RetryAfter(base, time.Second))); !ok || d != time.Second {
		t.Fatal("retry after")
	}

	if _, ok := activity.RetryAfterOf(base); ok {
		t.Fatal("plain error carries a delay")
	}
}
