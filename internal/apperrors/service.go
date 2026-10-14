package apperrors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/errors/envelope"
	"github.com/backplane-labs/backplane/internal/store/db"
)

// Related reads what the telemetry stores hold around an occurrence.
type Related interface {
	// Available: a log store is configured.
	Available() bool
	// Logs related to the occurrence, by the request's relation and range
	// (absent: a window around when).
	Logs(ctx context.Context, req *consolev1.RelatedLogsRequest, o *consolev1.ErrorOccurrence, when time.Time,
	) (*consolev1.RelatedLogsResponse, error)
}

// API serves ErrorService over the store.
type API struct {
	consolev1.UnimplementedErrorServiceServer

	s      *Service
	schema *jsonschema.Schema
}

// ErrorService is the console's ErrorService.
func (s *Service) ErrorService() (API, error) {
	schema, err := envelope.Schema()
	if err != nil {
		return API{}, fmt.Errorf("error envelope schema: %w", err)
	}

	return API{s: s, schema: schema}, nil
}

// Register registers ErrorService on r.
func (s *Service) Register(r grpc.ServiceRegistrar) {
	api, err := s.ErrorService()
	if err != nil {
		s.Log().Error("error service", xlog.Err(err))

		return
	}

	consolev1.RegisterErrorServiceServer(r, api)
}

// status maps errors to gRPC codes.
func (a API) status(ctx context.Context, err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}

	code := codes.Internal

	switch {
	case errors.Is(err, errStoreUnavailable):
		code = codes.Unavailable
	case errors.Is(err, pgx.ErrNoRows):
		code = codes.NotFound
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	default:
		a.s.Log().Ctx().Error(ctx, "errors api", xlog.Err(err))
	}

	return status.Error(code, err.Error()) //nolint:wrapcheck // a gRPC status travels as is
}

func (a API) queries() (*db.Queries, error) {
	if !a.s.Ready() {
		return nil, errStoreUnavailable
	}

	return a.s.store.Get().Q, nil
}

// ErrorInfo implements ErrorService.
func (a API) ErrorInfo(context.Context, *consolev1.ErrorInfoRequest) (*consolev1.ErrorInfoResponse, error) {
	return &consolev1.ErrorInfoResponse{
		Retention:        durationpb.New(a.s.settings.Retention),
		BurstPerMinute:   uint32(max(a.s.settings.BurstPerMinute, 0)), //nolint:gosec // bounded by the schema
		BurstPerDay:      uint32(max(a.s.settings.BurstPerDay, 0)),    //nolint:gosec // bounded by the schema
		MaxRecordBytes:   uint64(a.s.Limits().MaxRecord),              //nolint:gosec // positive
		RelatedAvailable: a.s.related != nil && a.s.related.Available(),
	}, nil
}

// SearchErrors implements ErrorService: occurrences newest first, keyset
// paged.
func (a API) SearchErrors(
	ctx context.Context, req *consolev1.SearchErrorsRequest,
) (*consolev1.SearchErrorsResponse, error) {
	w, err := checkFilter(req.GetFilter(), consolev1.ErrorField_ERROR_FIELD_UNSPECIFIED)
	if err != nil {
		return nil, err
	}

	size, err := pageSize(req.GetPageSize())
	if err != nil {
		return nil, err
	}

	hash := filterHash(req.GetFilter(), "occurrences")

	params := db.SearchErrorsParams{
		BeforeTime: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), BeforeID: uuid.Max, PageSize: size + 1,
	}
	w.fill(&params)

	if raw := req.GetPageCursor(); raw != "" {
		var c pageCursor
		if !decodeCursor(raw, &c) || c.Version != 1 || c.Filter != hash || c.ID == "" {
			return nil, invalid("error cursor does not match the filter")
		}

		id, err := uuid.Parse(c.ID)
		if err != nil {
			return nil, invalid("error cursor does not match the filter")
		}

		params.BeforeTime, params.BeforeID = time.Unix(0, c.Time).UTC(), id
	}

	queries, err := a.queries()
	if err != nil {
		return nil, a.status(ctx, err)
	}

	rows, err := queries.SearchErrors(ctx, params)
	if err != nil {
		return nil, a.status(ctx, err)
	}

	response := &consolev1.SearchErrorsResponse{}

	for i := range rows {
		if int64(i) == size {
			last := rows[i-1]
			response.NextPageCursor = encodeCursor(pageCursor{
				Version: 1, Filter: hash, Time: last.Time.UnixNano(), ID: last.ID.String(),
			})

			break
		}

		response.Occurrences = append(response.Occurrences, occurrenceOf(&rows[i]))
	}

	return response, nil
}

// listed is what the listing's row and the detail's row share.
type listed struct {
	ID          uuid.UUID
	Fingerprint []byte
	Service     string
	Time        time.Time
	ReceivedAt  time.Time
	Environment string
	Release     string
	Instance    string
	Origin      string
	Ingest      string
	EventID     string
	Type        string
	Message     string
	Severity    int32
	Mechanism   string
	Handled     *bool
	GroupKey    string
	TraceID     string
	SpanID      string
	RuntimeID   string
	Truncated   bool
}

func occurrenceOf(r *db.SearchErrorsRow) *consolev1.ErrorOccurrence {
	return occurrence(listed{
		ID: r.ID, Fingerprint: r.Fingerprint, Service: r.Service, Time: r.Time, ReceivedAt: r.ReceivedAt,
		Environment: r.Environment, Release: r.Release, Instance: r.Instance, Origin: r.Origin, Ingest: r.Ingest,
		EventID: r.EventID, Type: r.Type, Message: r.Message, Severity: r.Severity, Mechanism: r.Mechanism,
		Handled: r.Handled, GroupKey: r.GroupKey, TraceID: r.TraceID, SpanID: r.SpanID, RuntimeID: r.RuntimeID,
		Truncated: r.Truncated,
	})
}

func occurrence(r listed) *consolev1.ErrorOccurrence {
	origin := consolev1.ErrorOrigin_ERROR_ORIGIN_OTEL_LOG
	if r.Origin == OriginSDK {
		origin = consolev1.ErrorOrigin_ERROR_ORIGIN_SDK
	}

	return &consolev1.ErrorOccurrence{
		Ref: r.ID.String(), Origin: origin, EventId: r.EventID, Time: timestamppb.New(r.Time), Service: r.Service,
		Environment: r.Environment, Release: r.Release, Type: r.Type, Message: r.Message, Severity: r.Severity,
		TraceId: r.TraceID, SpanId: r.SpanID, RuntimeId: r.RuntimeID, Mechanism: r.Mechanism, GroupKey: r.GroupKey,
		Fingerprint: HexFingerprint(r.Fingerprint), Unhandled: r.Handled != nil && !*r.Handled, Ingest: r.Ingest,
		ReceivedAt: timestamppb.New(r.ReceivedAt), Truncated: r.Truncated,
	}
}

// ErrorHistogram implements ErrorService.
func (a API) ErrorHistogram(
	ctx context.Context, req *consolev1.ErrorHistogramRequest,
) (*consolev1.ErrorHistogramResponse, error) {
	w, err := checkFilter(req.GetFilter(), consolev1.ErrorField_ERROR_FIELD_UNSPECIFIED)
	if err != nil {
		return nil, err
	}

	queries, err := a.queries()
	if err != nil {
		return nil, a.status(ctx, err)
	}

	step := histogramStep(w.End.Sub(w.Start))
	params := db.ErrorHistogramParams{Step: interval(step)}
	w.fill(&params)

	rows, err := queries.ErrorHistogram(ctx, params)
	if err != nil {
		return nil, a.status(ctx, err)
	}

	counts := map[int64]uint64{}

	for _, r := range rows {
		if r.Bucket != nil {
			counts[r.Bucket.UnixNano()] += uint64(max(r.Count, 0))
		}
	}

	response := &consolev1.ErrorHistogramResponse{StepMillis: step.Milliseconds()}

	for when := w.Start; when.Before(w.End); when = when.Add(step) {
		count := counts[when.UnixNano()]
		response.Total += count
		response.Buckets = append(response.Buckets, &consolev1.ErrorBucket{Start: timestamppb.New(when), Count: count})
	}

	return response, nil
}

// facetValue is one value of a facet, as every facet query returns it.
type facetValue struct {
	Value    string
	Count    int64
	LastSeen *time.Time
}

// ErrorFacets implements ErrorService.
func (a API) ErrorFacets(
	ctx context.Context, req *consolev1.ErrorFacetsRequest,
) (*consolev1.ErrorFacetsResponse, error) {
	limit := int64(req.GetLimit())
	if limit == 0 {
		limit = defaultFacetLimit
	}

	if limit > maxFacetLimit || len(req.GetFields()) > 16 {
		return nil, invalid("error facets exceed their limits")
	}

	queries, err := a.queries()
	if err != nil {
		return nil, a.status(ctx, err)
	}

	response := &consolev1.ErrorFacetsResponse{}

	for _, field := range req.GetFields() {
		w, err := checkFilter(req.GetFilter(), field)
		if err != nil {
			return nil, err
		}

		values, err := a.facet(ctx, queries, field, w, limit)
		if err != nil {
			return nil, a.status(ctx, err)
		}

		facet := &consolev1.ErrorFacet{Field: field}

		for _, v := range values {
			entry := &consolev1.ErrorFacetValue{Value: v.Value, Count: uint64(max(v.Count, 0))}
			if v.LastSeen != nil {
				entry.LastSeen = timestamppb.New(*v.LastSeen)
			}

			facet.Values = append(facet.Values, entry)
		}

		response.Facets = append(response.Facets, facet)
	}

	return response, nil
}

// facet is one field's values with counts; each field has its own query.
func (a API) facet(
	ctx context.Context, queries *db.Queries, field consolev1.ErrorField, w where, limit int64,
) ([]facetValue, error) {
	switch field {
	case consolev1.ErrorField_ERROR_FIELD_SERVICE:
		return facetOf(ctx, w, db.ErrorFacetServiceParams{PageSize: limit}, queries.ErrorFacetService,
			func(r db.ErrorFacetServiceRow) facetValue { return facetValue{r.Value, r.Count, r.LastSeen} })
	case consolev1.ErrorField_ERROR_FIELD_TYPE:
		return facetOf(ctx, w, db.ErrorFacetTypeParams{PageSize: limit}, queries.ErrorFacetType,
			func(r db.ErrorFacetTypeRow) facetValue { return facetValue{r.Value, r.Count, r.LastSeen} })
	case consolev1.ErrorField_ERROR_FIELD_ENVIRONMENT:
		return facetOf(ctx, w, db.ErrorFacetEnvironmentParams{PageSize: limit}, queries.ErrorFacetEnvironment,
			func(r db.ErrorFacetEnvironmentRow) facetValue { return facetValue{r.Value, r.Count, r.LastSeen} })
	case consolev1.ErrorField_ERROR_FIELD_RELEASE:
		return facetOf(ctx, w, db.ErrorFacetReleaseParams{PageSize: limit}, queries.ErrorFacetRelease,
			func(r db.ErrorFacetReleaseRow) facetValue { return facetValue{r.Value, r.Count, r.LastSeen} })
	case consolev1.ErrorField_ERROR_FIELD_ORIGIN:
		return facetOf(ctx, w, db.ErrorFacetOriginParams{PageSize: limit}, queries.ErrorFacetOrigin,
			func(r db.ErrorFacetOriginRow) facetValue { return facetValue{r.Value, r.Count, r.LastSeen} })
	case consolev1.ErrorField_ERROR_FIELD_INGEST:
		return facetOf(ctx, w, db.ErrorFacetIngestParams{PageSize: limit}, queries.ErrorFacetIngest,
			func(r db.ErrorFacetIngestRow) facetValue { return facetValue{r.Value, r.Count, r.LastSeen} })
	case consolev1.ErrorField_ERROR_FIELD_INSTANCE:
		return facetOf(ctx, w, db.ErrorFacetInstanceParams{PageSize: limit}, queries.ErrorFacetInstance,
			func(r db.ErrorFacetInstanceRow) facetValue { return facetValue{r.Value, r.Count, r.LastSeen} })
	case consolev1.ErrorField_ERROR_FIELD_HANDLED:
		return facetOf(ctx, w, db.ErrorFacetHandledParams{PageSize: limit}, queries.ErrorFacetHandled,
			func(r db.ErrorFacetHandledRow) facetValue { return facetValue{r.Value, r.Count, r.LastSeen} })
	default:
		return nil, invalid("the field has no facet")
	}
}

// facetOf runs one facet query with the filter filled into its params.
func facetOf[P, R any](
	ctx context.Context, w where, params P, query func(context.Context, P) ([]R, error), value func(R) facetValue,
) ([]facetValue, error) {
	w.fill(&params)

	rows, err := query(ctx, params)
	if err != nil {
		return nil, err
	}

	out := make([]facetValue, 0, len(rows))
	for _, r := range rows {
		out = append(out, value(r))
	}

	return out, nil
}

// GetError implements ErrorService: one occurrence in full.
func (a API) GetError(ctx context.Context, req *consolev1.GetErrorRequest) (*consolev1.GetErrorResponse, error) {
	id, err := uuid.Parse(req.GetRef())
	if err != nil {
		return nil, invalid("invalid error reference")
	}

	queries, err := a.queries()
	if err != nil {
		return nil, a.status(ctx, err)
	}

	r, err := queries.GetError(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "error not found (it may have aged out)") //nolint:wrapcheck // a status
	}

	if err != nil {
		return nil, a.status(ctx, err)
	}

	response := a.detail(&r)

	g, err := queries.GetErrorGroup(ctx, db.GetErrorGroupParams{Fingerprint: r.Fingerprint, Service: r.Service})
	if err == nil {
		response.Group = storedGroup(g.Fingerprint, g.Service, g.Type, g.Message, g.FirstSeen, g.LastSeen, g.Count, g.Stored,
			triage{State: g.State, StateAt: g.StateAt, StateBy: g.StateBy, StateNote: g.StateNote, RegressedAt: g.RegressedAt})
	}

	return response, nil
}

// detail is the occurrence's full response.
func (a API) detail(r *db.GetErrorRow) *consolev1.GetErrorResponse {
	o := occurrence(listed{
		ID: r.ID, Fingerprint: r.Fingerprint, Service: r.Service, Time: r.Time, ReceivedAt: r.ReceivedAt,
		Environment: r.Environment, Release: r.Release, Instance: r.Instance, Origin: r.Origin, Ingest: r.Ingest,
		EventID: r.EventID, Type: r.Type, Message: r.Message, Severity: r.Severity, Mechanism: r.Mechanism,
		Handled: r.Handled, GroupKey: r.GroupKey, TraceID: r.TraceID, SpanID: r.SpanID, RuntimeID: r.RuntimeID,
		Truncated: r.Truncated,
	})

	response := &consolev1.GetErrorResponse{
		Occurrence: o, Stacktrace: r.Stacktrace, Scope: r.Scope,
		Fields: map[string]string{
			"service.name": r.Service, "deployment.environment.name": r.Environment, "service.version": r.Release,
			"service.instance.id": r.Instance, "exception.type": r.Type, "exception.message": r.Message,
			"trace_id": r.TraceID, "span_id": r.SpanID, "event_name": r.EventName,
			"severity_number": strconv.Itoa(int(r.Severity)),
			"severity_text":   r.SeverityText, "backplane.origin": r.Origin, "backplane.ingest": r.Ingest,
			"backplane.fingerprint": HexFingerprint(r.Fingerprint),
			"backplane.received_at": r.ReceivedAt.Format(time.RFC3339Nano),
			"app.debug.event.id":    r.EventID, "app.debug.runtime.id": r.RuntimeID, "app.debug.group.key": r.GroupKey,
			"app.debug.exception.mechanism": r.Mechanism, "instrumentation.scope": r.Scope,
		},
	}

	if r.Handled != nil {
		response.GetFields()["app.debug.exception.handled"] = strconv.FormatBool(*r.Handled)
	}

	if r.Truncated {
		response.Warnings = append(response.Warnings, "truncated")
	}

	for key, value := range response.GetFields() {
		if value == "" {
			delete(response.GetFields(), key)
		}
	}

	response.Attributes = structOf(r.Attributes, response.GetFields())
	response.Resource = structOf(r.Resource, response.GetFields())

	if r.Origin == OriginSDK {
		a.envelope(r.Body, response)
	}

	return response
}

// structOf is a JSON object as a Struct; its scalar members also land in
// fields, which chips filter on.
func structOf(raw json.RawMessage, fields map[string]string) *structpb.Struct {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil
	}

	for key, v := range value {
		switch x := v.(type) {
		case string:
			if _, taken := fields[key]; !taken {
				fields[key] = x
			}
		case float64, bool:
			if _, taken := fields[key]; !taken {
				fields[key] = fmt.Sprint(x)
			}
		}
	}

	out, err := structpb.NewStruct(value)
	if err != nil {
		return nil
	}

	return out
}

// envelope checks the SDK's envelope; failures are its status, not a
// failure to read the occurrence.
func (a API) envelope(body json.RawMessage, response *consolev1.GetErrorResponse) {
	fail := func(status, warning string) {
		response.EnvelopeStatus, response.Warnings = status, append(response.GetWarnings(), warning)
	}

	decoded, err := envelope.Decode(string(body))
	if err != nil || decoded["schema"] != "app-debug" {
		fail("invalid", "invalid_payload")

		return
	}

	if fmt.Sprint(decoded["schemaVersion"]) != "1" {
		fail("unsupported_version", "unsupported_version")

		return
	}

	if a.schema.Validate(decoded) != nil {
		fail("invalid", "invalid_payload")

		return
	}

	var plain map[string]any
	if json.Unmarshal(body, &plain) != nil {
		fail("invalid", "invalid_payload")

		return
	}

	envelopeStruct, err := structpb.NewStruct(plain)
	if err != nil {
		fail("invalid", "invalid_payload")

		return
	}

	response.Envelope, response.EnvelopeStatus = envelopeStruct, "available"
	if exception, ok := plain["exception"].(map[string]any); ok {
		response.Exception, _ = structpb.NewStruct(exception)
	}
}

// RelatedLogs implements ErrorService: through the telemetry stores.
func (a API) RelatedLogs(
	ctx context.Context, req *consolev1.RelatedLogsRequest,
) (*consolev1.RelatedLogsResponse, error) {
	id, err := uuid.Parse(req.GetRef())
	if err != nil {
		return nil, invalid("invalid error reference")
	}

	if a.s.related == nil || !a.s.related.Available() {
		return &consolev1.RelatedLogsResponse{Status: "unavailable", Reason: "log_store_not_configured"}, nil
	}

	queries, err := a.queries()
	if err != nil {
		return nil, a.status(ctx, err)
	}

	r, err := queries.GetError(ctx, id)
	if err != nil {
		return nil, a.status(ctx, err)
	}

	o := occurrence(listed{
		ID: r.ID, Service: r.Service, Time: r.Time, Origin: r.Origin, TraceID: r.TraceID, SpanID: r.SpanID,
		RuntimeID: r.RuntimeID,
	})

	return a.s.related.Logs(ctx, req, o, r.Time) //nolint:wrapcheck // the store's own status
}

// SearchErrorGroups implements ErrorService.
func (a API) SearchErrorGroups(
	ctx context.Context, req *consolev1.SearchErrorGroupsRequest,
) (*consolev1.SearchErrorGroupsResponse, error) {
	w, err := checkFilter(req.GetFilter(), consolev1.ErrorField_ERROR_FIELD_UNSPECIFIED)
	if err != nil {
		return nil, err
	}

	size, err := pageSize(req.GetPageSize())
	if err != nil {
		return nil, err
	}

	if w.StateIn, err = statesOf(req.GetStates()); err != nil {
		return nil, err
	}

	hash := filterHash(req.GetFilter(), fmt.Sprintf("groups:%s:%v", req.GetSort(), w.StateIn))

	offset, err := groupOffset(req.GetPageCursor(), hash)
	if err != nil {
		return nil, err
	}

	queries, err := a.queries()
	if err != nil {
		return nil, a.status(ctx, err)
	}

	params := db.SearchErrorGroupsParams{Sort: int32(req.GetSort()), PageSize: size + 1, PageOffset: int64(offset)}
	w.fill(&params)

	rows, err := queries.SearchErrorGroups(ctx, params)
	if err != nil {
		return nil, a.status(ctx, err)
	}

	response := &consolev1.SearchErrorGroupsResponse{}

	if int64(len(rows)) > size {
		rows = rows[:size]
		if offset+int(size) <= maxGroupOffset {
			response.NextPageCursor = encodeCursor(pageCursor{Version: 1, Filter: hash, Offset: offset + int(size)})
		}
	}

	fingerprints := make([][]byte, 0, len(rows))
	for i := range rows {
		fingerprints = append(fingerprints, rows[i].Fingerprint)
	}

	step := histogramStep(w.End.Sub(w.Start))
	response.StepMillis = step.Milliseconds()

	trends, err := a.trends(ctx, queries, w, step, fingerprints)
	if err != nil {
		return nil, a.status(ctx, err)
	}

	for i := range rows {
		key := string(rows[i].Fingerprint) + "\x00" + rows[i].Service
		response.Groups = append(response.Groups, groupOf(&rows[i], w, trends[key]))
	}

	return response, nil
}

// groupOffset is where a groups cursor continues; none is the start.
func groupOffset(raw, hash string) (int, error) {
	if raw == "" {
		return 0, nil
	}

	var c pageCursor
	if !decodeCursor(raw, &c) || c.Version != 1 || c.Filter != hash || c.Offset < 0 || c.Offset > maxGroupOffset {
		return 0, invalid("error cursor does not match the filter")
	}

	return c.Offset, nil
}

// trends are the groups' stored occurrences per bucket, by
// fingerprint\x00service.
func (a API) trends(
	ctx context.Context, queries *db.Queries, w where, step time.Duration, fingerprints [][]byte,
) (map[string][]uint64, error) {
	out := map[string][]uint64{}
	if len(fingerprints) == 0 {
		return out, nil
	}

	buckets := int(w.End.Sub(w.Start)/step) + 1
	params := db.ErrorGroupTrendsParams{Step: interval(step), Fingerprints: fingerprints}
	w.fill(&params)

	rows, err := queries.ErrorGroupTrends(ctx, params)
	if err != nil {
		return nil, err //nolint:wrapcheck // mapped by status
	}

	for _, r := range rows {
		if r.Bucket == nil {
			continue
		}

		key := string(r.Fingerprint) + "\x00" + r.Service
		if out[key] == nil {
			out[key] = make([]uint64, buckets)
		}

		if at := int(r.Bucket.Sub(w.Start) / step); at >= 0 && at < buckets {
			out[key][at] += uint64(max(r.Count, 0))
		}
	}

	return out, nil
}

func groupOf(r *db.SearchErrorGroupsRow, w where, trend []uint64) *consolev1.ErrorGroup {
	g := &consolev1.ErrorGroup{
		Fingerprint: HexFingerprint(r.Fingerprint), Service: r.Service, Type: r.Type, Message: r.Message,
		FirstSeen: timestamppb.New(r.FirstSeen), LastSeen: timestamppb.New(r.LastSeen),
		Count: uint64(max(r.Count, 0)), Stored: uint64(max(r.Stored, 0)),
		CountInRange: uint64(max(r.CountInRange, 0)),
		Trend:        trend, Environments: r.Environments, Releases: r.Releases,
		Origin: consolev1.ErrorOrigin_ERROR_ORIGIN_OTEL_LOG, New: !r.FirstSeen.Before(w.Start),
	}

	if r.NewestID != nil {
		g.SampleRef = r.NewestID.String()
	}

	if r.AnySdk != nil && *r.AnySdk {
		g.Origin = consolev1.ErrorOrigin_ERROR_ORIGIN_SDK
	}

	if r.AnyUnhandled != nil {
		g.Unhandled = *r.AnyUnhandled
	}

	triage{
		State: r.State, StateAt: r.StateAt, StateBy: r.StateBy, StateNote: r.StateNote, RegressedAt: r.RegressedAt,
	}.apply(g)

	return g
}

// GetErrorGroup implements ErrorService: the group over the range with
// its trend.
func (a API) GetErrorGroup(
	ctx context.Context, req *consolev1.GetErrorGroupRequest,
) (*consolev1.GetErrorGroupResponse, error) {
	fingerprint, ok := ParseFingerprint(req.GetFingerprint())
	if !ok || req.GetService() == "" {
		return nil, invalid("a group is a fingerprint and a service")
	}

	w, err := checkFilter(req.GetFilter(), consolev1.ErrorField_ERROR_FIELD_FINGERPRINT)
	if err != nil {
		return nil, err
	}

	w.FingerprintIn, w.ServiceIn = [][]byte{fingerprint}, []string{req.GetService()}
	w.ServiceNot, w.ServicePrefix = nil, nil

	queries, err := a.queries()
	if err != nil {
		return nil, a.status(ctx, err)
	}

	stored, err := queries.GetErrorGroup(ctx, db.GetErrorGroupParams{Fingerprint: fingerprint, Service: req.GetService()})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "error group not found") //nolint:wrapcheck // a status
	}

	if err != nil {
		return nil, a.status(ctx, err)
	}

	params := db.SearchErrorGroupsParams{PageSize: 1}
	w.fill(&params)

	rows, err := queries.SearchErrorGroups(ctx, params)
	if err != nil {
		return nil, a.status(ctx, err)
	}

	step := histogramStep(w.End.Sub(w.Start))

	trends, err := a.trends(ctx, queries, w, step, [][]byte{fingerprint})
	if err != nil {
		return nil, a.status(ctx, err)
	}

	trend := trends[string(fingerprint)+"\x00"+req.GetService()]
	response := &consolev1.GetErrorGroupResponse{StepMillis: step.Milliseconds()}

	if len(rows) > 0 {
		response.Group = groupOf(&rows[0], w, trend)
	} else {
		response.Group = storedGroup(stored.Fingerprint, stored.Service, stored.Type, stored.Message, stored.FirstSeen,
			stored.LastSeen, stored.Count, stored.Stored, triage{
				State: stored.State, StateAt: stored.StateAt, StateBy: stored.StateBy, StateNote: stored.StateNote,
				RegressedAt: stored.RegressedAt,
			})
	}

	for i, count := range trend {
		response.Buckets = append(response.Buckets, &consolev1.ErrorBucket{
			Start: timestamppb.New(w.Start.Add(time.Duration(i) * step)), Count: count,
		})
	}

	if response.Buckets == nil {
		for when := w.Start; when.Before(w.End); when = when.Add(step) {
			response.Buckets = append(response.Buckets, &consolev1.ErrorBucket{Start: timestamppb.New(when)})
		}
	}

	slices.SortFunc(response.GetBuckets(), func(x, y *consolev1.ErrorBucket) int {
		return x.GetStart().AsTime().Compare(y.GetStart().AsTime())
	})

	return response, nil
}
