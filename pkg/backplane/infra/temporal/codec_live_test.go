package temporal_test

import (
	"bytes"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/temporal/temporaltest"
	"github.com/backplane-labs/backplane/pkg/backplane/seal"
)

func echo(_ workflow.Context, in string) (string, error) { return in, nil }

// With encryption the history holds sealed payloads only: the run works,
// and its input read through the raw API is ciphertext until decoded.
func TestEncryptedHistory(t *testing.T) {
	t.Parallel()

	addr := temporaltest.Addr(t)
	enc := config.Seal{Key: config.Secret(seal.GenerateKey())}

	opts, err := temporal.Options(temporal.Config{Addr: addr, Namespace: temporaltest.Namespace, Encryption: enc}, "test", nil)
	if err != nil {
		t.Fatal(err)
	}

	c, err := temporal.Dial(t.Context(), temporal.Config{Addr: addr}, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	queue := temporaltest.Name("sealed")
	w := worker.New(c, queue, worker.Options{})
	w.RegisterWorkflow(echo)

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	run, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{
		TaskQueue: queue, WorkflowExecutionTimeout: 30 * time.Second,
	}, echo, "whsec_secret")
	if err != nil {
		t.Fatal(err)
	}

	var out string
	if err := run.Get(t.Context(), &out); err != nil || out != "whsec_secret" {
		t.Fatalf("run: %q %v", out, err)
	}

	iter := c.GetWorkflowHistory(t.Context(), run.GetID(), run.GetRunID(), false,
		enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)

	e, err := iter.Next()
	if err != nil {
		t.Fatal(err)
	}

	input := e.GetWorkflowExecutionStartedEventAttributes().GetInput().GetPayloads()[0]
	if string(input.GetMetadata()[converter.MetadataEncoding]) != temporal.EncodingSealed ||
		bytes.Contains(input.GetData(), []byte("whsec_secret")) {
		t.Fatalf("stored input: %v", input)
	}

	codec, _ := temporal.NewCodec(enc)
	if err := codec.DecodeMessage(t.Context(), e); err != nil {
		t.Fatal(err)
	}

	var decoded string
	if err := converter.GetDefaultDataConverter().FromPayload(
		e.GetWorkflowExecutionStartedEventAttributes().GetInput().GetPayloads()[0], &decoded); err != nil ||
		decoded != "whsec_secret" {
		t.Fatalf("decoded input: %q %v", decoded, err)
	}
}
