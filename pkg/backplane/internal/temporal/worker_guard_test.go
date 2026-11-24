package temporal_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	inftemporal "github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/decl"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal/temporaltest"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
	"github.com/backplane-labs/backplane/pkg/backplane/seal"
	"github.com/backplane-labs/backplane/pkg/backplane/workflows"
)

var errFuturePayload = errors.New("payload is newer than the worker")

// workerWithGuard uses the same encrypted transport for two builds of one queue.
func workerWithGuard(
	t *testing.T, addr, queue, build string, enc config.Seal, guard func([]*commonpb.Payload) error,
) client.Client {
	t.Helper()

	h := backplanetest.New(t, backplanetest.Name(queue))
	if guard != nil {
		workflows.GuardWorkerPayloads(h.Root(), guard)
	}

	workflows.Declare(h.Root(), "Inspect", func(_ workflow.Context, in *backplanev1.ActivityCall) (*backplanev1.ActivityCall, error) {
		return &backplanev1.ActivityCall{Activity: in.GetActivity(), Key: build}, nil
	})
	e, _ := decl.Env(h.Root(), "test worker")
	transport := temporal.New(temporal.Params{
		Conn:    inftemporal.Config{Addr: addr, Encryption: enc, RequireEncryption: true},
		Service: queue, Instance: queue + "-" + build, Log: testlog.Discard(), Env: e,
	})
	transport.UseFastRetry()

	g := temporaltest.NewGroup(t)
	if err := transport.Connect(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		g.Stop()
		_ = transport.StopWorker(ctx)
		_ = transport.Close(ctx)
	})

	conn, err := transport.SDK()
	if err != nil {
		t.Fatal(err)
	}

	if err := transport.StartWorker(t.Context(), g); err != nil {
		t.Fatal(err)
	}

	return conn
}

func TestWorkerPayloadGuard(t *testing.T) {
	t.Parallel()

	addr := temporaltest.Addr(t)
	queue := temporaltest.Name("payload-guard")
	enc := config.Seal{Key: config.Secret(seal.GenerateKey())}
	rejected := make(chan struct{}, 1)
	sender := workerWithGuard(t, addr, queue, "old", enc, func(payloads []*commonpb.Payload) error {
		for _, p := range payloads {
			if bytes.Contains(p.GetData(), []byte(`"future-input"`)) {
				select {
				case rejected <- struct{}{}:
				default:
				}

				return errFuturePayload
			}
		}

		return nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), callLimit)
	defer cancel()

	run, err := sender.ExecuteWorkflow(ctx, client.StartWorkflowOptions{TaskQueue: queue, WorkflowExecutionTimeout: callLimit},
		"Inspect", &backplanev1.ActivityCall{Activity: "future-input"})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-rejected:
	case <-ctx.Done():
		t.Fatal("old worker did not reject the decrypted payload")
	}

	// A rollout overlaps old and new pollers on one queue.
	workerWithGuard(t, addr, queue, "new", enc, nil)

	var out backplanev1.ActivityCall
	if err := run.Get(ctx, &out); err != nil || out.GetKey() != "new" || out.GetActivity() != "future-input" {
		t.Fatalf("new worker did not finish the original run: %s, %v", out.String(), err)
	}

	history := sender.GetWorkflowHistory(ctx, run.GetID(), run.GetRunID(), false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	found := false

	for history.HasNext() {
		event, err := history.Next()
		if err != nil {
			t.Fatal(err)
		}

		if event.GetWorkflowTaskFailedEventAttributes() != nil {
			found = true
		}
	}

	if !found {
		t.Fatal("history contains no failed workflow task")
	}
}
