package audit_test

import (
	"context"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/apperrors"
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
