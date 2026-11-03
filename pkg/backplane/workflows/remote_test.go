package workflows_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	tactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	internal "github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal/temporaltest"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
)

type chargeIn struct {
	Amount int `json:"amount"`
}

type chargeOut struct {
	Receipt string `json:"receipt"`
}

// Malformed services and names panic where the ref is made.
func TestRefNames(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		make func()
	}{
		{"activity service", func() { workflows.ActivityOf[chargeIn, chargeOut]("Billing", "Charge") }},
		{"activity name", func() { workflows.ActivityOf[chargeIn, chargeOut]("billing", "charge") }},
		{"workflow service", func() { workflows.WorkflowOf[chargeIn, chargeOut]("billing_x", "Ship") }},
		{"workflow name", func() { workflows.WorkflowOf[chargeIn, chargeOut]("billing", "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()

			tc.make()
		})
	}

	ref := workflows.WorkflowOf[chargeIn, chargeOut]("billing", "Settle")
	if ref.Name() != "Settle" || ref.Queue() != "billing" {
		t.Fatalf("ref %s on %s", ref.Name(), ref.Queue())
	}
}

// A workflow of one service runs another service's activity and child
// workflow on that service's queue: only the owner's worker has them.
//
//nolint:paralleltest // one Temporal dev server
func TestCallsOnTheOwnersQueue(t *testing.T) {
	c := temporaltest.Dial(t)
	billing, orders := temporaltest.Name("billing"), temporaltest.Name("orders")

	charge := workflows.ActivityOf[chargeIn, chargeOut](billing, "Charge")
	settle := workflows.WorkflowOf[chargeIn, chargeOut](billing, "Settle")

	// billing: the activity over the envelope, as activity.Handle registers
	// it, and a workflow.
	start(t, c, billing, func(w worker.Worker) {
		w.RegisterActivityWithOptions(internal.ActivityFunc(billing, "Charge", func(_ context.Context, in []byte) ([]byte, error) {
			var req chargeIn
			if err := decl.Decode(in, &req); err != nil {
				return nil, err
			}

			return decl.Encode(chargeOut{Receipt: "r-" + string(rune('0'+req.Amount))})
		}), tactivity.RegisterOptions{Name: "Charge"})
		w.RegisterWorkflowWithOptions(func(_ workflow.Context, in chargeIn) (chargeOut, error) {
			return chargeOut{Receipt: "settled-" + string(rune('0'+in.Amount))}, nil
		}, workflow.RegisterOptions{Name: "Settle"})
	})

	// orders: a workflow calling both; it registers neither.
	start(t, c, orders, func(w worker.Worker) {
		w.RegisterWorkflowWithOptions(func(ctx workflow.Context, in chargeIn) (string, error) {
			ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})

			receipt, err := charge.Execute(ctx, in)
			if err != nil {
				return "", err
			}

			settled, err := settle.ExecuteChild(ctx, in)
			if err != nil {
				return "", err
			}

			return receipt.Receipt + "/" + settled.Receipt, nil
		}, workflow.RegisterOptions{Name: "Order"})
	})

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "order-" + uuid.NewString(), TaskQueue: orders},
		"Order", chargeIn{Amount: 7})
	if err != nil {
		t.Fatal(err)
	}

	var got string
	if err := run.Get(ctx, &got); err != nil || got != "r-7/settled-7" {
		t.Fatalf("order: %q %v", got, err)
	}

	// From outside a workflow: Start on the owner's queue.
	settledRun, err := settle.Start(ctx, c, chargeIn{Amount: 3}, client.StartWorkflowOptions{ID: "settle-" + uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}

	var out chargeOut
	if err := settledRun.Get(ctx, &out); err != nil || out.Receipt != "settled-3" {
		t.Fatalf("start: %+v %v", out, err)
	}
}

func start(t *testing.T, c client.Client, queue string, register func(worker.Worker)) {
	t.Helper()

	w := worker.New(c, queue, worker.Options{})
	register(w)

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(w.Stop)
}
