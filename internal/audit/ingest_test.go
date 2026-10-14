package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/audit"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
)

func str(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func flag(key string, value bool) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: value}}}
}

func num(key string, value int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: value}}}
}

func export(service string, records ...*logspb.LogRecord) *collogspb.ExportLogsServiceRequest {
	return &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource:  &resourcepb.Resource{Attributes: []*commonpb.KeyValue{str("service.name", service), str("deployment.environment", "test")}},
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: records}},
	}}}
}

func auditRecord(at time.Time, body string, attrs ...*commonpb.KeyValue) *logspb.LogRecord {
	return &logspb.LogRecord{
		TimeUnixNano: uint64(at.UnixNano()), SeverityText: "INFO",
		Body:       &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}},
		Attributes: append([]*commonpb.KeyValue{flag(audit.LabelAudit, true)}, attrs...),
		TraceId:    []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
	}
}

// Only records marked backplane.audit=true become rows; the action is
// event.name, else the event name, else the body; the same record hashes
// the same.
func TestRecords(t *testing.T) {
	t.Parallel()

	noon := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	unmarked := auditRecord(noon, "plain log")
	unmarked.Attributes = []*commonpb.KeyValue{flag(audit.LabelAudit, false)}
	stringMark := auditRecord(noon, "string label")
	stringMark.Attributes = []*commonpb.KeyValue{str(audit.LabelAudit, "true")}
	named := auditRecord(noon, "deleted", str("event.name", "identity.deleted"), str(audit.LabelActor, "user:7"),
		str(audit.LabelSubject, "identity/42"), str(audit.LabelOutcome, "succeeded"), num("count", 3))
	evented := auditRecord(time.Time{}, "ignored body")
	evented.TimeUnixNano, evented.ObservedTimeUnixNano, evented.EventName = 0, uint64(noon.UnixNano()), "session.revoked"

	rows, rejected := audit.Records(export("iam", named, unmarked, stringMark, evented, auditRecord(noon, "login")), noon.Add(time.Hour))
	if rejected != 2 || len(rows) != 3 {
		t.Fatalf("rows %d rejected %d", len(rows), rejected)
	}

	first := rows[0]
	if first.Service != "iam" || first.Action != "identity.deleted" || first.Actor != "user:7" || first.Subject != "identity/42" ||
		first.Outcome != "succeeded" || first.Body != "deleted" || !first.Time.Equal(noon) || first.TraceID != "0102030405060708090a0b0c0d0e0f10" {
		t.Fatalf("row %+v", first)
	}

	var attrs map[string]any
	if err := json.Unmarshal(first.Attributes, &attrs); err != nil || attrs["count"] != float64(3) || attrs[audit.LabelAudit] != true {
		t.Fatalf("attributes %s %v", first.Attributes, err)
	}

	if rows[1].Action != "session.revoked" || !rows[1].Time.Equal(noon) || rows[2].Action != "login" {
		t.Fatalf("fallbacks %q %v %q", rows[1].Action, rows[1].Time, rows[2].Action)
	}

	again, _ := audit.Records(export("iam", named), noon)
	if !bytes.Equal(again[0].Key, first.Key) || again[0].ID == first.ID {
		t.Fatal("the same record must hash the same under a new id")
	}

	withID := auditRecord(noon, "x", str(audit.LabelID, "op-1"))
	other := auditRecord(noon.Add(time.Second), "y", str(audit.LabelID, "op-1"))
	a, _ := audit.Records(export("iam", withID), noon)
	b, _ := audit.Records(export("iam", other), noon)
	c, _ := audit.Records(export("billing", other), noon)

	if !bytes.Equal(a[0].Key, b[0].Key) || bytes.Equal(a[0].Key, c[0].Key) {
		t.Fatal("backplane.audit.id deduplicates within a service only")
	}
}

func ingestReplica(t *testing.T, dsn string, keys ...string) (*audit.Service, *audit.Ingest) {
	t.Helper()

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: config.Secret(dsn)}))
	svc := audit.New(h.Root(), st, audit.WithPublisher(func(context.Context, audit.Entry) error { return nil }))

	settings := audit.Settings{Listen: "127.0.0.1:0"}
	for _, key := range keys {
		settings.Keys = append(settings.Keys, config.Secret(key))
	}

	in := audit.NewIngest(h.Root(), st, settings)
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	return svc, in
}

func where(conditions ...*consolev1.AuditCondition) *consolev1.AuditFilter {
	return &consolev1.AuditFilter{Conditions: conditions}
}

func cond(op consolev1.AuditOperator, field consolev1.AuditField, values ...*structpb.Value) *consolev1.AuditCondition {
	return &consolev1.AuditCondition{Target: &consolev1.AuditCondition_Field{Field: field}, Op: op, Values: values}
}

func is(field consolev1.AuditField, values ...string) *consolev1.AuditCondition {
	out := make([]*structpb.Value, 0, len(values))
	for _, v := range values {
		out = append(out, structpb.NewStringValue(v))
	}

	return cond(consolev1.AuditOperator_AUDIT_OPERATOR_IS, field, out...)
}

func attr(key string, op consolev1.AuditOperator, values ...*structpb.Value) *consolev1.AuditCondition {
	return &consolev1.AuditCondition{Target: &consolev1.AuditCondition_Attribute{Attribute: key}, Op: op, Values: values}
}

// The feed is platform entries and application records alike: filtered by
// fields, attributes (a platform entry's detail too) and text, paged,
// watched, counted over time and by value; the Collector's retries store
// nothing twice; keys guard the listener.
func TestAuditFeedLive(t *testing.T) {
	t.Parallel()

	dsn := isolatedDatabase(t)
	svc, in := ingestReplica(t, dsn, "collector-key-0123456789")
	logs := collogspb.NewLogsServiceClient(client(t, in.Register))
	api := consolev1.NewAuditServiceClient(client(t, func(r grpc.ServiceRegistrar) { consolev1.RegisterAuditServiceServer(r, svc) }))

	if _, err := logs.Export(t.Context(), export("iam")); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("without a key: %v", err)
	}

	ctx := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer collector-key-0123456789")
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	batch := export("iam",
		auditRecord(base, "created", str("event.name", "identity.created"), str(audit.LabelSubject, "identity/1"), str("tenant", "a"), num("count", 1)),
		auditRecord(base.Add(time.Minute), "deleted", str("event.name", "identity.deleted"), str(audit.LabelSubject, "identity/1"),
			str(audit.LabelActor, "user:7"), str(audit.LabelOutcome, "failed"), str("tenant", "b"), num("count", 5)),
		auditRecord(base.Add(2*time.Minute), "Deleted 100% of sessions", str("event.name", "session.revoked"), str("tenant", "a")),
		&logspb.LogRecord{Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "not audit"}}},
	)

	for range 2 { // the Collector's retry
		response, err := logs.Export(ctx, batch)
		if err != nil || response.GetPartialSuccess().GetRejectedLogRecords() != 1 {
			t.Fatalf("export: %v %v", response, err)
		}
	}

	if _, err := logs.Export(ctx, export("billing", auditRecord(base.Add(3*time.Minute), "charged", str("event.name", "invoice.charged")))); err != nil {
		t.Fatal(err)
	}

	st := storeOf(t, dsn)
	if _, err := st.AppendAudit(t.Context(), store.AuditDraft{
		Actor: "console:1", Action: "workflow.signal", Subject: "hello", Outcome: "succeeded", Service: "hello",
		Detail: store.AuditDetail{WorkflowID: "wf-1", Signal: "go"},
	}); err != nil {
		t.Fatal(err)
	}

	search := func(filter *consolev1.AuditFilter, size uint32, cursor string) *consolev1.SearchAuditResponse {
		t.Helper()

		response, err := api.SearchAudit(t.Context(), &consolev1.SearchAuditRequest{Filter: filter, PageSize: size, PageCursor: cursor})
		if err != nil {
			t.Fatal(err)
		}

		return response
	}
	actions := func(records []*consolev1.AuditRecord) []string {
		out := make([]string, 0, len(records))
		for _, record := range records {
			out = append(out, record.GetAction())
		}

		return out
	}

	all := search(nil, 0, "")
	if got := actions(all.GetRecords()); len(got) != 5 || got[0] != "workflow.signal" || got[4] != "identity.created" {
		t.Fatalf("newest first, stored once: %v", got)
	}

	if record := all.GetRecords()[3]; record.GetActor() != "user:7" || record.GetSource() != consolev1.AuditSource_AUDIT_SOURCE_APPLICATION ||
		record.GetAttributes().GetFields()["tenant"].GetStringValue() != "b" ||
		record.GetResource().GetFields()["deployment.environment"].GetStringValue() != "test" {
		t.Fatalf("record %v", record)
	}

	if platform := all.GetRecords()[0]; platform.GetSource() != consolev1.AuditSource_AUDIT_SOURCE_PLATFORM ||
		platform.GetOperationId() == "" || platform.GetAttributes().GetFields()["workflow_id"].GetStringValue() != "wf-1" {
		t.Fatalf("platform %v", platform)
	}

	for name, c := range map[string]struct {
		filter *consolev1.AuditFilter
		want   int
	}{
		"source":        {where(is(consolev1.AuditField_AUDIT_FIELD_SOURCE, "platform")), 1},
		"services":      {where(is(consolev1.AuditField_AUDIT_FIELD_SERVICE, "billing", "hello")), 2},
		"not outcome":   {where(cond(consolev1.AuditOperator_AUDIT_OPERATOR_IS_NOT, consolev1.AuditField_AUDIT_FIELD_OUTCOME, structpb.NewStringValue("failed"))), 4},
		"prefix":        {where(cond(consolev1.AuditOperator_AUDIT_OPERATOR_PREFIX, consolev1.AuditField_AUDIT_FIELD_ACTION, structpb.NewStringValue("identity."))), 2},
		"actor set":     {where(cond(consolev1.AuditOperator_AUDIT_OPERATOR_EXISTS, consolev1.AuditField_AUDIT_FIELD_ACTOR)), 2},
		"attribute":     {where(attr("tenant", consolev1.AuditOperator_AUDIT_OPERATOR_IS, structpb.NewStringValue("a"))), 2},
		"detail":        {where(attr("signal", consolev1.AuditOperator_AUDIT_OPERATOR_IS, structpb.NewStringValue("go"))), 1},
		"number":        {where(attr("count", consolev1.AuditOperator_AUDIT_OPERATOR_GT, structpb.NewNumberValue(2))), 1},
		"number exact":  {where(attr("count", consolev1.AuditOperator_AUDIT_OPERATOR_IS, structpb.NewNumberValue(1))), 1},
		"has":           {where(attr("tenant", consolev1.AuditOperator_AUDIT_OPERATOR_NOT_EXISTS)), 2},
		"contains attr": {where(attr("event.name", consolev1.AuditOperator_AUDIT_OPERATOR_CONTAINS, structpb.NewStringValue("DELETED"))), 1},
		"and":           {where(attr("tenant", consolev1.AuditOperator_AUDIT_OPERATOR_IS, structpb.NewStringValue("a")), is(consolev1.AuditField_AUDIT_FIELD_ACTION, "session.revoked")), 1},
		"text":          {&consolev1.AuditFilter{Text: "100%"}, 1},
		"like":          {&consolev1.AuditFilter{Text: "1_0"}, 0},
		"text in attrs": {&consolev1.AuditFilter{Text: "wf-1"}, 1},
		"time":          {&consolev1.AuditFilter{Start: timestamppb.New(base.Add(time.Minute)), End: timestamppb.New(base.Add(3 * time.Minute))}, 2},
	} {
		if got := len(search(c.filter, 0, "").GetRecords()); got != c.want {
			t.Errorf("%s: %d records, want %d", name, got, c.want)
		}
	}

	page := search(nil, 3, "")
	next := search(nil, 3, page.GetNextPageCursor())

	if len(page.GetRecords()) != 3 || len(next.GetRecords()) != 2 || next.GetNextPageCursor() != "" || actions(next.GetRecords())[1] != "identity.created" {
		t.Fatalf("pages %v %v", actions(page.GetRecords()), actions(next.GetRecords()))
	}

	if _, err := api.SearchAudit(t.Context(), &consolev1.SearchAuditRequest{
		Filter: where(is(consolev1.AuditField_AUDIT_FIELD_SERVICE, "iam")), PageCursor: page.GetNextPageCursor(),
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("cursor of another filter: %v", err)
	}

	for name, bad := range map[string]*consolev1.AuditFilter{
		"comparison on a field": where(cond(consolev1.AuditOperator_AUDIT_OPERATOR_GT, consolev1.AuditField_AUDIT_FIELD_SERVICE, structpb.NewNumberValue(1))),
		"no values":             where(cond(consolev1.AuditOperator_AUDIT_OPERATOR_IS, consolev1.AuditField_AUDIT_FIELD_SERVICE)),
		"text comparison":       where(attr("count", consolev1.AuditOperator_AUDIT_OPERATOR_GT, structpb.NewStringValue("2"))),
	} {
		if _, err := api.SearchAudit(t.Context(), &consolev1.SearchAuditRequest{Filter: bad}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}

	facets, err := api.AuditFacets(t.Context(), &consolev1.AuditFacetsRequest{
		Filter: where(is(consolev1.AuditField_AUDIT_FIELD_SERVICE, "iam")),
		Targets: []*consolev1.AuditCondition{
			{Target: &consolev1.AuditCondition_Field{Field: consolev1.AuditField_AUDIT_FIELD_SERVICE}},
			{Target: &consolev1.AuditCondition_Attribute{Attribute: "tenant"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	services, tenants := facets.GetFacets()[0], facets.GetFacets()[1]
	if services.GetTotal() != 5 || len(services.GetValues()) != 3 || services.GetValues()[0].GetValue().GetStringValue() != "iam" ||
		services.GetValues()[0].GetCount() != 3 {
		t.Fatalf("service facet leaves its own condition out: %v", services)
	}

	if tenants.GetTotal() != 3 || tenants.GetValues()[0].GetValue().GetStringValue() != "a" || tenants.GetValues()[0].GetCount() != 2 {
		t.Fatalf("tenant facet: %v", tenants)
	}

	fields, err := api.AuditFields(t.Context(), &consolev1.AuditFieldsRequest{})
	if err != nil || len(fields.GetFields()) == 0 {
		t.Fatal(fields, err)
	}

	counts := map[string]uint64{}
	for _, f := range fields.GetFields() {
		counts[f.GetAttribute()] = f.GetCount()
	}

	if counts["tenant"] != 3 || counts["workflow_id"] != 1 || counts[audit.LabelAudit] != 4 {
		t.Fatalf("fields %v", counts)
	}

	histogram, err := api.AuditHistogram(t.Context(), &consolev1.AuditHistogramRequest{
		Filter: &consolev1.AuditFilter{Start: timestamppb.New(base), End: timestamppb.New(base.Add(4 * time.Minute))}, Buckets: 4,
	})
	if err != nil || histogram.GetStepSeconds() != 60 || len(histogram.GetBuckets()) != 4 {
		t.Fatal(histogram, err)
	}

	if b := histogram.GetBuckets()[1]; b.GetApplication() != 1 || b.GetFailed() != 1 || b.GetPlatform() != 0 {
		t.Fatalf("bucket %v", b)
	}
}

func storeOf(t *testing.T, dsn string) *store.Store {
	t.Helper()

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: config.Secret(dsn)}))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	return st.Get()
}
