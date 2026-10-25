package temporal_test

import (
	"bytes"
	"errors"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
	"github.com/backplane-labs/backplane/pkg/backplane/seal"
)

func payload(t *testing.T, v any) *commonpb.Payload {
	t.Helper()

	p, err := converter.GetDefaultDataConverter().ToPayload(v)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func TestCodec(t *testing.T) {
	t.Parallel()

	codec, err := temporal.NewCodec(config.Seal{Key: config.Secret(seal.GenerateKey())})
	if err != nil {
		t.Fatal(err)
	}

	plain := payload(t, map[string]string{"secret": "whsec_x"})

	sealed, err := codec.Encode([]*commonpb.Payload{plain})
	if err != nil || string(sealed[0].GetMetadata()[converter.MetadataEncoding]) != temporal.EncodingSealed ||
		bytes.Contains(sealed[0].GetData(), []byte("whsec_x")) {
		t.Fatalf("encode: %v %v", sealed, err)
	}

	opened, err := codec.Decode(sealed)
	if err != nil || !proto.Equal(opened[0], plain) {
		t.Fatalf("decode: %v %v", opened, err)
	}

	// History from before encryption passes as it is.
	if passed, err := codec.Decode([]*commonpb.Payload{plain}); err != nil || passed[0] != plain {
		t.Fatalf("plain payload: %v %v", passed, err)
	}

	// Strict, it takes only what it sealed.
	if _, err := codec.Strict(true).Decode([]*commonpb.Payload{plain}); !errors.Is(err, temporal.ErrUnsealed) {
		t.Fatalf("strict, plain payload: %v", err)
	}

	if got, err := codec.Strict(true).Decode(sealed); err != nil || !proto.Equal(got[0], plain) {
		t.Fatalf("strict, sealed payload: %v %v", got, err)
	}

	// Without the key a sealed payload does not open.
	if _, err := temporal.Plain.Decode(sealed); !errors.Is(err, seal.ErrOpen) {
		t.Fatalf("no key: %v", err)
	}

	// Every payload of a message read through the raw API: lists, maps,
	// nested messages.
	memo := payload(t, "komeet")
	sealedMemo, _ := codec.Encode([]*commonpb.Payload{memo})
	event := &historypb.HistoryEvent{Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
		WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
			Input: &commonpb.Payloads{Payloads: sealed},
			Memo:  &commonpb.Memo{Fields: map[string]*commonpb.Payload{"service": sealedMemo[0]}},
		},
	}}

	if err := codec.DecodeMessage(t.Context(), event); err != nil {
		t.Fatal(err)
	}

	started := event.GetWorkflowExecutionStartedEventAttributes()
	if !proto.Equal(started.GetInput().GetPayloads()[0], plain) || !proto.Equal(started.GetMemo().GetFields()["service"], memo) {
		t.Fatalf("decoded event: %v", started)
	}
}
