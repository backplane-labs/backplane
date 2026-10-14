package apperrors_test

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/apperrors"
	"github.com/backplane-labs/backplane/internal/store"
	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
)

func isolatedDatabase(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("BACKPLANE_TEST_PG")
	if dsn == "" {
		t.Skip("BACKPLANE_TEST_PG not set")
	}

	ctx := t.Context()

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	name := "backplane_errors_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, _ = admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")

		admin.Close(cleanup)
	})

	parsed, err := url.Parse(dsn)
	if err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		parsed.Path = "/" + name
		parsed.RawPath = ""

		return parsed.String()
	}

	return dsn + " dbname=" + name
}

func service(t *testing.T, dsn string, settings apperrors.Settings) (*apperrors.Service, apperrors.API) {
	t.Helper()

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(postgres.Config{DSN: config.Secret(dsn)}))
	svc := apperrors.New(h.Root(), st, settings)
	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	api, err := svc.ErrorService()
	if err != nil {
		t.Fatal(err)
	}

	return svc, api
}

// auditActions are the platform audit actions with prefix, oldest first.
func auditActions(t *testing.T, dsn, prefix string) []string {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close(t.Context())

	rows, err := conn.Query(t.Context(), "SELECT action FROM backplane.audit_entry WHERE action LIKE $1 ORDER BY sequence", prefix+"%")
	if err != nil {
		t.Fatal(err)
	}

	defer rows.Close()

	var out []string

	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatal(err)
		}

		out = append(out, action)
	}

	return out
}

func str(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

// exception is one OTel exception record of service at when.
func exception(service, env, typ, message string, when time.Time, attrs ...*commonpb.KeyValue) *collogspb.ExportLogsServiceRequest {
	record := &logspb.LogRecord{
		TimeUnixNano: uint64(when.UnixNano()), SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
		Body:       &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: message}},
		Attributes: append([]*commonpb.KeyValue{str(apperrors.AttrType, typ), str(apperrors.AttrMessage, message)}, attrs...),
		TraceId:    bytes.Repeat([]byte{0xab}, 16),
	}

	return &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource:  &resourcepb.Resource{Attributes: []*commonpb.KeyValue{str("service.name", service), str("deployment.environment.name", env), str("service.version", "v1")}},
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{record}}},
	}}}
}

func is(field consolev1.ErrorField, values ...string) *consolev1.ErrorCondition {
	return &consolev1.ErrorCondition{Field: field, Op: consolev1.ErrorOperator_ERROR_OPERATOR_IS, Values: values}
}

// Records are stored once per key, counted past the group's burst, listed,
// faceted, bucketed, grouped and read back whole; expiry removes old
// occurrences and empty groups.
func TestErrorsLive(t *testing.T) {
	t.Parallel()

	dsn := isolatedDatabase(t)
	svc, api := service(t, dsn, apperrors.Settings{Retention: 24 * time.Hour, BurstPerMinute: 3, BurstPerDay: 100, MaxRecordBytes: 64 << 10})

	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	base := now.Add(-time.Hour)

	save := func(req *collogspb.ExportLogsServiceRequest) (int64, int64) {
		t.Helper()

		records, rejected := apperrors.Records(req, now, svc.Limits())
		if rejected != 0 {
			t.Fatal("rejected", rejected)
		}

		stored, counted, err := svc.Save(ctx, records)
		if err != nil {
			t.Fatal(err)
		}

		return stored, counted
	}

	// The same record twice stores once; the group counts it once.
	first := exception("api", "prod", "PaymentError", "card declined for user 1", base)
	if stored, counted := save(first); stored != 1 || counted != 0 {
		t.Fatal(stored, counted)
	}

	if stored, counted := save(first); stored != 0 || counted != 0 {
		t.Fatal("retry", stored, counted)
	}

	// Five more of one group in a minute: the burst of three is reached
	// after two, the rest only count.
	for i := range 5 {
		save(exception("api", "prod", "PaymentError", "card declined for user "+string(rune('2'+i)), base.Add(time.Duration(i+1)*time.Second)))
	}

	save(exception("web", "prod", "TypeError", "x is undefined", base.Add(time.Minute), str(apperrors.AttrHandled, "false"), str(apperrors.AttrIngest, apperrors.IngestProxy)))
	save(exception("web", "staging", "TypeError", "x is undefined", base.Add(2*time.Minute)))

	filter := &consolev1.ErrorFilter{Start: timestamppb.New(base.Add(-time.Minute)), End: timestamppb.New(now)}

	found, err := api.SearchErrors(ctx, &consolev1.SearchErrorsRequest{Filter: filter})
	if err != nil || len(found.GetOccurrences()) != 5 {
		t.Fatalf("occurrences %v %v", found, err)
	}

	newest := found.GetOccurrences()[0]
	if newest.GetService() != "web" || newest.GetEnvironment() != "staging" || newest.GetFingerprint() == "" || newest.GetIngest() != apperrors.IngestTrusted ||
		found.GetOccurrences()[1].GetUnhandled() != true || found.GetOccurrences()[1].GetIngest() != apperrors.IngestProxy {
		t.Fatalf("newest first %v", found.GetOccurrences()[:2])
	}

	// Keyset pages do not repeat.
	page, err := api.SearchErrors(ctx, &consolev1.SearchErrorsRequest{Filter: filter, PageSize: 4})
	if err != nil || len(page.GetOccurrences()) != 4 || page.GetNextPageCursor() == "" {
		t.Fatal("page", page, err)
	}

	rest, err := api.SearchErrors(ctx, &consolev1.SearchErrorsRequest{Filter: filter, PageSize: 4, PageCursor: page.GetNextPageCursor()})
	if err != nil || len(rest.GetOccurrences()) != 1 || rest.GetNextPageCursor() != "" || rest.GetOccurrences()[0].GetRef() == page.GetOccurrences()[3].GetRef() {
		t.Fatal("rest", rest, err)
	}

	if _, err := api.SearchErrors(ctx, &consolev1.SearchErrorsRequest{Filter: &consolev1.ErrorFilter{Start: filter.GetStart(), End: filter.GetEnd(), Text: "x"}, PageCursor: page.GetNextPageCursor()}); status.Code(err) != codes.InvalidArgument {
		t.Fatal("a cursor of another filter", err)
	}

	for name, c := range map[string]struct {
		conditions []*consolev1.ErrorCondition
		text       string
		want       int
	}{
		"service":     {[]*consolev1.ErrorCondition{is(consolev1.ErrorField_ERROR_FIELD_SERVICE, "web")}, "", 2},
		"env":         {[]*consolev1.ErrorCondition{is(consolev1.ErrorField_ERROR_FIELD_ENVIRONMENT, "staging")}, "", 1},
		"type like":   {[]*consolev1.ErrorCondition{{Field: consolev1.ErrorField_ERROR_FIELD_TYPE, Op: consolev1.ErrorOperator_ERROR_OPERATOR_CONTAINS, Values: []string{"payment"}}}, "", 3},
		"not service": {[]*consolev1.ErrorCondition{{Field: consolev1.ErrorField_ERROR_FIELD_SERVICE, Op: consolev1.ErrorOperator_ERROR_OPERATOR_IS_NOT, Values: []string{"api"}}}, "", 2},
		"unhandled":   {[]*consolev1.ErrorCondition{is(consolev1.ErrorField_ERROR_FIELD_HANDLED, "false")}, "", 1},
		"ingest":      {[]*consolev1.ErrorCondition{is(consolev1.ErrorField_ERROR_FIELD_INGEST, "proxy")}, "", 1},
		"fingerprint": {[]*consolev1.ErrorCondition{is(consolev1.ErrorField_ERROR_FIELD_FINGERPRINT, newest.GetFingerprint())}, "", 2},
		"text":        {nil, "DECLINED", 3},
		"text miss":   {nil, "nothing", 0},
		"trace":       {[]*consolev1.ErrorCondition{is(consolev1.ErrorField_ERROR_FIELD_TRACE_ID, "abababababababababababababababab")}, "", 5},
	} {
		got, err := api.SearchErrors(ctx, &consolev1.SearchErrorsRequest{Filter: &consolev1.ErrorFilter{Start: filter.GetStart(), End: filter.GetEnd(), Conditions: c.conditions, Text: c.text}})
		if err != nil || len(got.GetOccurrences()) != c.want {
			t.Errorf("%s: %d %v", name, len(got.GetOccurrences()), err)
		}
	}

	facets, err := api.ErrorFacets(ctx, &consolev1.ErrorFacetsRequest{Filter: &consolev1.ErrorFilter{
		Start: filter.GetStart(), End: filter.GetEnd(), Conditions: []*consolev1.ErrorCondition{is(consolev1.ErrorField_ERROR_FIELD_SERVICE, "web")},
	}, Fields: []consolev1.ErrorField{consolev1.ErrorField_ERROR_FIELD_SERVICE, consolev1.ErrorField_ERROR_FIELD_TYPE, consolev1.ErrorField_ERROR_FIELD_HANDLED}})
	if err != nil || len(facets.GetFacets()) != 3 || len(facets.GetFacets()[0].GetValues()) != 2 || facets.GetFacets()[0].GetValues()[0].GetValue() != "api" ||
		facets.GetFacets()[0].GetValues()[0].GetCount() != 3 || len(facets.GetFacets()[1].GetValues()) != 1 || facets.GetFacets()[1].GetValues()[0].GetValue() != "TypeError" {
		t.Fatalf("facets (a field's own condition is skipped) %v %v", facets, err)
	}

	histogram, err := api.ErrorHistogram(ctx, &consolev1.ErrorHistogramRequest{Filter: filter})
	if err != nil || histogram.GetTotal() != 5 || len(histogram.GetBuckets()) == 0 || histogram.GetStepMillis() != 60_000 {
		t.Fatal("histogram", histogram.GetTotal(), histogram.GetStepMillis(), err)
	}

	// Groups: the api's counts six (one retry uncounted), three stored.
	groups, err := api.SearchErrorGroups(ctx, &consolev1.SearchErrorGroupsRequest{Filter: filter, Sort: consolev1.ErrorGroupSort_ERROR_GROUP_SORT_COUNT})
	if err != nil || len(groups.GetGroups()) != 2 {
		t.Fatalf("groups %v %v", groups, err)
	}

	payment, typeErr := groups.GetGroups()[0], groups.GetGroups()[1]
	if payment.GetService() != "api" || payment.GetType() != "PaymentError" || payment.GetCount() != 6 || payment.GetStored() != 3 || payment.GetCountInRange() != 3 ||
		!payment.GetNew() || payment.GetSampleRef() == "" || len(payment.GetTrend()) == 0 || payment.GetEnvironments()[0] != "prod" || payment.GetReleases()[0] != "v1" {
		t.Fatalf("payment group %v", payment)
	}

	if typeErr.GetCount() != 2 || !typeErr.GetUnhandled() || len(typeErr.GetEnvironments()) != 2 {
		t.Fatalf("type error group %v", typeErr)
	}

	var sum uint64
	for _, n := range payment.GetTrend() {
		sum += n
	}

	if sum != 3 {
		t.Fatal("trend of the stored", payment.GetTrend())
	}

	byLast, err := api.SearchErrorGroups(ctx, &consolev1.SearchErrorGroupsRequest{Filter: filter, PageSize: 1})
	if err != nil || byLast.GetGroups()[0].GetService() != "web" || byLast.GetNextPageCursor() == "" {
		t.Fatal("by last seen, paged", byLast, err)
	}

	second, err := api.SearchErrorGroups(ctx, &consolev1.SearchErrorGroupsRequest{Filter: filter, PageSize: 1, PageCursor: byLast.GetNextPageCursor()})
	if err != nil || second.GetGroups()[0].GetService() != "api" || second.GetNextPageCursor() != "" {
		t.Fatal("second page", second, err)
	}

	group, err := api.GetErrorGroup(ctx, &consolev1.GetErrorGroupRequest{Fingerprint: payment.GetFingerprint(), Service: "api", Filter: filter})
	if err != nil || group.GetGroup().GetCount() != 6 || len(group.GetBuckets()) == 0 || group.GetStepMillis() != histogram.GetStepMillis() {
		t.Fatal("group", group, err)
	}

	if _, err := api.GetErrorGroup(ctx, &consolev1.GetErrorGroupRequest{Fingerprint: payment.GetFingerprint(), Service: "nope"}); status.Code(err) != codes.NotFound {
		t.Fatal("missing group", err)
	}

	detail, err := api.GetError(ctx, &consolev1.GetErrorRequest{Ref: found.GetOccurrences()[1].GetRef()})
	if err != nil || detail.GetGroup().GetFingerprint() != typeErr.GetFingerprint() || detail.GetFields()["deployment.environment.name"] != "prod" ||
		detail.GetFields()["app.debug.exception.handled"] != "false" || detail.GetResource().GetFields()["service.version"].GetStringValue() != "v1" ||
		detail.GetAttributes().GetFields()["exception.type"].GetStringValue() != "TypeError" || detail.GetEnvelopeStatus() != "" {
		t.Fatalf("detail %v %v", detail, err)
	}

	if _, err := api.GetError(ctx, &consolev1.GetErrorRequest{Ref: uuid.NewString()}); status.Code(err) != codes.NotFound {
		t.Fatal("missing", err)
	}

	info, err := api.ErrorInfo(ctx, &consolev1.ErrorInfoRequest{})
	if err != nil || info.GetRetention().AsDuration() != 24*time.Hour || info.GetBurstPerMinute() != 3 || info.GetRelatedAvailable() {
		t.Fatal("info", info, err)
	}

	related, err := api.RelatedLogs(ctx, &consolev1.RelatedLogsRequest{Ref: newest.GetRef()})
	if err != nil || related.GetStatus() != "unavailable" {
		t.Fatal("related without a log store", related, err)
	}

	// Triage: a resolved group leaves the open list and comes back, marked
	// regressed, on an occurrence after its resolution; a muted one stays
	// out; both are audited.
	resolved, err := api.SetErrorGroupState(ctx, &consolev1.SetErrorGroupStateRequest{
		Fingerprint: payment.GetFingerprint(), Service: "api", State: consolev1.ErrorGroupState_ERROR_GROUP_STATE_RESOLVED, Note: "fixed in v2",
	})
	if err != nil || resolved.GetGroup().GetState() != consolev1.ErrorGroupState_ERROR_GROUP_STATE_RESOLVED || resolved.GetGroup().GetStateBy() != apperrors.DefaultAuthor ||
		resolved.GetGroup().GetStateNote() != "fixed in v2" || resolved.GetGroup().GetStateAt() == nil {
		t.Fatal("resolve", resolved, err)
	}

	open, err := api.SearchErrorGroups(ctx, &consolev1.SearchErrorGroupsRequest{Filter: filter})
	if err != nil || len(open.GetGroups()) != 1 || open.GetGroups()[0].GetService() != "web" {
		t.Fatal("open groups", open, err)
	}

	resolvedOnly, err := api.SearchErrorGroups(ctx, &consolev1.SearchErrorGroupsRequest{Filter: filter, States: []consolev1.ErrorGroupState{consolev1.ErrorGroupState_ERROR_GROUP_STATE_RESOLVED}})
	if err != nil || len(resolvedOnly.GetGroups()) != 1 || resolvedOnly.GetGroups()[0].GetService() != "api" {
		t.Fatal("resolved groups", resolvedOnly, err)
	}

	// A record from before the resolution does not reopen; one after does.
	save(exception("api", "prod", "PaymentError", "card declined for user 8", base.Add(10*time.Second)))

	stillResolved, err := api.GetErrorGroup(ctx, &consolev1.GetErrorGroupRequest{Fingerprint: payment.GetFingerprint(), Service: "api"})
	if err != nil || stillResolved.GetGroup().GetState() != consolev1.ErrorGroupState_ERROR_GROUP_STATE_RESOLVED {
		t.Fatal("late record", stillResolved, err)
	}

	later := time.Now().UTC().Add(time.Second)
	save(exception("api", "prod", "PaymentError", "card declined for user 9", later))

	reopened, err := api.GetErrorGroup(ctx, &consolev1.GetErrorGroupRequest{Fingerprint: payment.GetFingerprint(), Service: "api"})
	if err != nil || reopened.GetGroup().GetState() != consolev1.ErrorGroupState_ERROR_GROUP_STATE_OPEN || reopened.GetGroup().GetRegressedAt() == nil ||
		reopened.GetGroup().GetStateNote() != "fixed in v2" {
		t.Fatal("regression", reopened, err)
	}

	if _, err := api.SetErrorGroupState(ctx, &consolev1.SetErrorGroupStateRequest{
		Fingerprint: typeErr.GetFingerprint(), Service: "web", State: consolev1.ErrorGroupState_ERROR_GROUP_STATE_MUTED,
	}); err != nil {
		t.Fatal(err)
	}

	wideFilter := &consolev1.ErrorFilter{Start: filter.GetStart(), End: timestamppb.New(later.Add(time.Minute))}

	afterMute, err := api.SearchErrorGroups(ctx, &consolev1.SearchErrorGroupsRequest{Filter: wideFilter})
	if err != nil || len(afterMute.GetGroups()) != 1 || afterMute.GetGroups()[0].GetService() != "api" || afterMute.GetGroups()[0].GetRegressedAt() == nil {
		t.Fatal("after mute", afterMute, err)
	}

	all, err := api.SearchErrorGroups(ctx, &consolev1.SearchErrorGroupsRequest{Filter: wideFilter, States: []consolev1.ErrorGroupState{
		consolev1.ErrorGroupState_ERROR_GROUP_STATE_OPEN, consolev1.ErrorGroupState_ERROR_GROUP_STATE_MUTED,
	}})
	if err != nil || len(all.GetGroups()) != 2 {
		t.Fatal("all states", all, err)
	}

	reopen, err := api.SetErrorGroupState(ctx, &consolev1.SetErrorGroupStateRequest{
		Fingerprint: payment.GetFingerprint(), Service: "api", State: consolev1.ErrorGroupState_ERROR_GROUP_STATE_OPEN,
	})
	if err != nil || reopen.GetGroup().GetRegressedAt() != nil || reopen.GetGroup().GetStateNote() != "" {
		t.Fatal("reopen clears the regression", reopen, err)
	}

	for name, req := range map[string]*consolev1.SetErrorGroupStateRequest{
		"unknown":   {Fingerprint: apperrors.HexFingerprint(bytes.Repeat([]byte{7}, 16)), Service: "api", State: consolev1.ErrorGroupState_ERROR_GROUP_STATE_MUTED},
		"no state":  {Fingerprint: payment.GetFingerprint(), Service: "api"},
		"long note": {Fingerprint: payment.GetFingerprint(), Service: "api", State: consolev1.ErrorGroupState_ERROR_GROUP_STATE_MUTED, Note: strings.Repeat("x", 2000)},
	} {
		_, err := api.SetErrorGroupState(ctx, req)
		if code := status.Code(err); (name == "unknown" && code != codes.NotFound) || (name != "unknown" && code != codes.InvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}

	audited := auditActions(t, dsn, "error_group.")
	if len(audited) != 3 || audited[0] != "error_group.resolved" || audited[1] != "error_group.muted" || audited[2] != "error_group.open" {
		t.Fatal("audit", audited)
	}

	// Expiry: an old occurrence goes, its group with it once empty.
	save(exception("old", "prod", "OldError", "gone", now.Add(-48*time.Hour)))

	if err := svc.Expire(ctx, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	wide := &consolev1.ErrorFilter{Start: timestamppb.New(now.Add(-72 * time.Hour)), End: timestamppb.New(now)}

	after, err := api.SearchErrors(ctx, &consolev1.SearchErrorsRequest{Filter: wide})
	if err != nil || len(after.GetOccurrences()) != 5 {
		t.Fatal("after expiry", len(after.GetOccurrences()), err)
	}

	afterGroups, err := api.SearchErrorGroups(ctx, &consolev1.SearchErrorGroupsRequest{Filter: wide, States: []consolev1.ErrorGroupState{
		consolev1.ErrorGroupState_ERROR_GROUP_STATE_OPEN, consolev1.ErrorGroupState_ERROR_GROUP_STATE_MUTED,
	}})
	if err != nil || len(afterGroups.GetGroups()) != 2 {
		t.Fatal("groups after expiry", afterGroups, err)
	}
}

// The SDK's envelope is kept whole and checked on read.
func TestErrorsLiveSDK(t *testing.T) {
	t.Parallel()

	dsn := isolatedDatabase(t)
	svc, api := service(t, dsn, apperrors.Settings{BurstPerMinute: 60, BurstPerDay: 2000})

	raw, err := os.ReadFile("../../web/tests/fixtures/errors/exception-otlp.json")
	if err != nil {
		t.Fatal(err)
	}

	var req collogspb.ExportLogsServiceRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	records, rejected := apperrors.Records(&req, now, svc.Limits())

	if stored, _, err := svc.Save(t.Context(), records); err != nil || rejected != 0 || stored != 1 {
		t.Fatal(stored, rejected, err)
	}

	when := records[0].Insert.Time
	filter := &consolev1.ErrorFilter{Start: timestamppb.New(when.Add(-time.Minute)), End: timestamppb.New(when.Add(time.Minute))}

	found, err := api.SearchErrors(t.Context(), &consolev1.SearchErrorsRequest{Filter: filter})
	if err != nil || len(found.GetOccurrences()) != 1 || found.GetOccurrences()[0].GetOrigin() != consolev1.ErrorOrigin_ERROR_ORIGIN_SDK {
		t.Fatal(found, err)
	}

	detail, err := api.GetError(t.Context(), &consolev1.GetErrorRequest{Ref: found.GetOccurrences()[0].GetRef()})
	if err != nil || detail.GetEnvelopeStatus() != "available" || detail.GetEnvelope().GetFields()["eventId"].GetStringValue() != "d45238c7-4ff0-4e5f-819b-f8d0f4c3c143" ||
		detail.GetException().GetFields()["type"].GetStringValue() != "FixtureError" || len(detail.GetWarnings()) != 0 || detail.GetScope() != "app-debug.browser@0.1.0" {
		t.Fatalf("sdk detail %v %v", detail, err)
	}

	origin, err := api.ErrorFacets(t.Context(), &consolev1.ErrorFacetsRequest{Filter: filter, Fields: []consolev1.ErrorField{consolev1.ErrorField_ERROR_FIELD_ORIGIN}})
	if err != nil || origin.GetFacets()[0].GetValues()[0].GetValue() != apperrors.OriginSDK {
		t.Fatal(origin, err)
	}
}
