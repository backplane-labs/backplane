package audit_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/apperrors"
	"github.com/backplane-labs/backplane/internal/apperrors/upstream"
	"github.com/backplane-labs/backplane/internal/audit"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
)

// Exception records on the audit listener go to the error store, not the
// audit log, and are not rejected; audit records marked as such stay audit.
func TestExceptionsRoutedToErrors(t *testing.T) {
	t.Parallel()

	dsn := isolatedDatabase(t)

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: config.Secret(dsn)}))
	svc := audit.New(h.Root(), st, audit.WithPublisher(func(context.Context, audit.Entry) error { return nil }))
	errs := apperrors.New(h.Root(), st, apperrors.Settings{BurstPerMinute: 60, BurstPerDay: 2000})
	in := audit.NewIngest(h.Root(), st, audit.Settings{Listen: "127.0.0.1:0", Keys: []config.Secret{"collector-key-0123456789"}}, audit.WithErrors(errs))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	logs := collogspb.NewLogsServiceClient(client(t, in.Register))
	auditAPI := consolev1.NewAuditServiceClient(client(t, func(r grpc.ServiceRegistrar) { consolev1.RegisterAuditServiceServer(r, svc) }))
	errorAPI := consolev1.NewErrorServiceClient(client(t, errs.Register))

	ctx := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer collector-key-0123456789")
	base := time.Now().UTC().Add(-time.Minute)
	exception := &logspb.LogRecord{
		TimeUnixNano: uint64(base.UnixNano()), SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
		Body:       &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "boom"}},
		Attributes: []*commonpb.KeyValue{str("exception.type", "PaymentError"), str("exception.message", "card declined")},
	}
	batch := export("api",
		auditRecord(base, "created", str("event.name", "identity.created")),
		exception,
		&logspb.LogRecord{Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "not audit"}}},
	)

	for range 2 { // the Collector's retry
		response, err := logs.Export(ctx, batch)
		if err != nil || response.GetPartialSuccess().GetRejectedLogRecords() != 1 {
			t.Fatalf("export: %v %v", response, err)
		}
	}

	entries, err := auditAPI.SearchAudit(t.Context(), &consolev1.SearchAuditRequest{})
	if err != nil || len(entries.GetRecords()) != 1 || entries.GetRecords()[0].GetAction() != "identity.created" {
		t.Fatalf("audit keeps only audit: %v %v", entries, err)
	}

	filter := &consolev1.ErrorFilter{Start: timestamppb.New(base.Add(-time.Minute)), End: timestamppb.New(base.Add(time.Minute))}

	found, err := errorAPI.SearchErrors(t.Context(), &consolev1.SearchErrorsRequest{Filter: filter})
	if err != nil || len(found.GetOccurrences()) != 1 || found.GetOccurrences()[0].GetType() != "PaymentError" || found.GetOccurrences()[0].GetService() != "api" {
		t.Fatalf("the exception is stored once: %v %v", found, err)
	}

	groups, err := errorAPI.SearchErrorGroups(t.Context(), &consolev1.SearchErrorGroupsRequest{Filter: filter})
	if err != nil || len(groups.GetGroups()) != 1 || groups.GetGroups()[0].GetCount() != 1 {
		t.Fatalf("the retry is not counted: %v %v", groups, err)
	}
}

// The exceptions the error store accepted reach the upstream as received,
// once: not the audit records (an exception marked audit included), not
// the unmarked ones, not the Collector's retry.
func TestAcceptedExceptionsForwardedUpstream(t *testing.T) {
	t.Parallel()

	dsn := isolatedDatabase(t)

	var (
		mu       sync.Mutex
		received []*collogspb.ExportLogsServiceRequest
	)

	got := make(chan struct{}, 8)
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Errorf("body: %v", err)
		}

		plain, _ := io.ReadAll(zr)
		export := &collogspb.ExportLogsServiceRequest{}
		if err := proto.Unmarshal(plain, export); err != nil {
			t.Errorf("body: %v", err)
		}

		mu.Lock()
		received = append(received, export)
		mu.Unlock()

		got <- struct{}{}
	}))
	t.Cleanup(receiver.Close)

	settings := upstream.Settings{
		URL: receiver.URL + "/v1/logs", Service: "core", Queue: 10, Batch: 10, Flush: 50 * time.Millisecond,
		Timeout: time.Second, GiveUp: time.Second,
	}
	if err := backplanetest.SetLive(&settings.Enabled, true); err != nil {
		t.Fatal(err)
	}

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: config.Secret(dsn)}))
	forward := upstream.New(h.Root(), settings, upstream.WithTransport(receiver.Client().Transport))
	errs := apperrors.New(h.Root(), st, apperrors.Settings{BurstPerMinute: 60, BurstPerDay: 2000},
		apperrors.WithUpstream(forward))
	in := audit.NewIngest(h.Root(), st, audit.Settings{Listen: "127.0.0.1:0"}, audit.WithErrors(errs))
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	logs := collogspb.NewLogsServiceClient(client(t, in.Register))
	base := time.Now().UTC().Add(-time.Minute)
	exception := &logspb.LogRecord{
		TimeUnixNano: uint64(base.UnixNano()), SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
		Body:       &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "boom"}},
		Attributes: []*commonpb.KeyValue{str("exception.type", "PaymentError"), str("exception.message", "card declined")},
		TraceId:    []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
	}
	batch := export("api",
		auditRecord(base, "created", str("event.name", "identity.created")),
		auditRecord(base, "audited failure", str("exception.type", "AuditedError")),
		exception,
		&logspb.LogRecord{Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "not audit"}}},
	)

	for range 2 { // the Collector's retry
		if _, err := logs.Export(t.Context(), batch); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing forwarded")
	}

	backplanetest.Stop(h, forward)

	want := &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource:  batch.GetResourceLogs()[0].GetResource(),
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{exception}}},
	}}}

	mu.Lock()
	defer mu.Unlock()

	if len(received) != 1 || !proto.Equal(received[0], want) {
		t.Fatalf("upstream got %v, want %v", received, want)
	}
}
