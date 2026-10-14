package audit_test

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/audit"
)

func specialNumber(value float64) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: value}}
}

func TestAuditSpecialValues(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value float64
		want  string
	}{{math.NaN(), "NaN"}, {math.Inf(1), "Infinity"}, {math.Inf(-1), "-Infinity"}} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			record := auditRecord(time.Now(), "event", &commonpb.KeyValue{Key: "number", Value: specialNumber(tc.value)})
			record.Body = &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.AnyValue{specialNumber(tc.value)}}}}
			batch := export("test", record)
			batch.ResourceLogs[0].Resource.Attributes = append(batch.ResourceLogs[0].Resource.Attributes,
				&commonpb.KeyValue{Key: "number", Value: specialNumber(tc.value)})

			rows, rejected := audit.Records(batch, time.Now())
			if rejected != 0 || len(rows) != 1 {
				t.Fatalf("rows=%d rejected=%d", len(rows), rejected)
			}

			for _, raw := range []json.RawMessage{rows[0].Attributes, rows[0].Resource} {
				var values map[string]any
				if err := json.Unmarshal(raw, &values); err != nil || values["number"] != tc.want {
					t.Fatalf("non-finite value lost: %s: %v", raw, err)
				}
			}

			if rows[0].Body != `["`+tc.want+`"]` {
				t.Fatalf("body=%s", rows[0].Body)
			}
		})
	}
}

func TestAuditInvalidText(t *testing.T) {
	t.Parallel()

	for name, change := range map[string]func(*collogspb.ExportLogsServiceRequest){
		"body": func(b *collogspb.ExportLogsServiceRequest) {
			b.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Body = str("", "bad\x00body").GetValue()
		},
		"attribute": func(b *collogspb.ExportLogsServiceRequest) {
			b.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes = append(b.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes, str("bad", "\x00"))
		},
		"resource": func(b *collogspb.ExportLogsServiceRequest) {
			b.ResourceLogs[0].Resource.Attributes = append(b.ResourceLogs[0].Resource.Attributes, str("bad\x00key", "value"))
		},
		"severity": func(b *collogspb.ExportLogsServiceRequest) {
			b.ResourceLogs[0].ScopeLogs[0].LogRecords[0].SeverityText = "\x00"
		},
		"event": func(b *collogspb.ExportLogsServiceRequest) {
			b.ResourceLogs[0].ScopeLogs[0].LogRecords[0].EventName = "\x00"
		},
		"utf8": func(b *collogspb.ExportLogsServiceRequest) {
			b.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Body = str("", string([]byte{255})).GetValue()
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			batch := export("test", auditRecord(time.Now(), "event"))
			change(batch)

			rows, rejected := audit.Records(batch, time.Now())
			if len(rows) != 0 || rejected != 1 {
				t.Fatalf("invalid record admitted: rows=%d rejected=%d", len(rows), rejected)
			}
		})
	}
}

func TestAuditMixedBatchLive(t *testing.T) {
	t.Parallel()
	svc, in := ingestReplica(t, isolatedDatabase(t))
	logs := collogspb.NewLogsServiceClient(client(t, in.Register))

	batch := export("mixed",
		auditRecord(time.Now(), "good"),
		auditRecord(time.Now(), "number", &commonpb.KeyValue{Key: "ratio", Value: specialNumber(math.NaN())}),
		auditRecord(time.Now(), "invalid\x00body"),
		auditRecord(time.Now(), "invalid attribute", str("key", "\x00")),
	)
	for range 2 {
		response, err := logs.Export(t.Context(), batch)
		if err != nil || response.GetPartialSuccess().GetRejectedLogRecords() != 2 {
			t.Fatalf("export=%v error=%v", response, err)
		}
	}

	feed, err := svc.SearchAudit(t.Context(), &consolev1.SearchAuditRequest{})
	if err != nil || len(feed.GetRecords()) != 2 {
		t.Fatalf("valid rows must survive and retries deduplicate: %v, %v", feed, err)
	}

	for _, record := range feed.GetRecords() {
		if record.GetMessage() == "number" && record.GetAttributes().GetFields()["ratio"].GetStringValue() != "NaN" {
			t.Fatalf("special value not stored: %v", record)
		}
	}
}
