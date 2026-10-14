package obs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/errors/envelope"
	"github.com/backplane-labs/backplane/internal/obs/logsql"
)

// Stored fields of an error record: the OTel log data model and exception
// conventions as VictoriaLogs keeps them, and the browser SDK's app.debug.*.
const (
	fTime      logsql.Field = "_time"
	fBody      logsql.Field = "_msg"
	fStream    logsql.Field = "_stream_id"
	fTrace     logsql.Field = "trace_id"
	fSpan      logsql.Field = "span_id"
	fSeverity  logsql.Field = "severity_number"
	fEventName logsql.Field = "event_name"
	fService   logsql.Field = "service.name"
	fEnv       logsql.Field = "deployment.environment.name"
	fRelease   logsql.Field = "service.version"
	fType      logsql.Field = "exception.type"
	fMessage   logsql.Field = "exception.message"
	fStack     logsql.Field = "exception.stacktrace"
	fSchema    logsql.Field = "app.debug.schema.version"
	fKind      logsql.Field = "app.debug.kind"
	fEventID   logsql.Field = "app.debug.event.id"
	fRuntime   logsql.Field = "app.debug.runtime.id"
	fSequence  logsql.Field = "app.debug.event.sequence"
	fMechanism logsql.Field = "app.debug.exception.mechanism"
	fHandled   logsql.Field = "app.debug.exception.handled"
	fGroup     logsql.Field = "app.debug.group.key"
	// fHash identifies an OTel-log occurrence, which has no event id: the
	// hash of its whole stored row, computed the same way on every read.
	fHash logsql.Field = "bp_row_hash"
	// Aliases of the facet aggregates.
	fSDKCount  logsql.Field = "bp_sdk"
	fOTelCount logsql.Field = "bp_otel"
	fCount     logsql.Field = "bp_count"
	fLastSeen  logsql.Field = "bp_last_seen"
	fHits      logsql.Field = "bp_hits"
)

const (
	originSDK          = "sdk"
	originOTel         = "otel-log"
	defaultErrorPage   = 50
	maxErrorPage       = 200
	maxErrorOffset     = 2000
	maxErrorRange      = 31 * 24 * time.Hour
	maxDetailRows      = 8
	maxErrorValues     = 32
	maxErrorConditions = 32
	maxErrorValueBytes = 1024
	histogramBuckets   = 120
	histogramEdges     = 2
	defaultErrorFacet  = 10
	maxErrorFacet      = 50
	relatedWindow      = 15 * time.Minute
	statusAvailable    = "available"
	statusPartial      = "partial"
	statusNotFound     = "not_found"
	statusUnavailable  = "unavailable"
	kindException      = "exception"
)

//nolint:gochecknoglobals // immutable tables
var (
	errorFields = map[consolev1.ErrorField]logsql.Field{
		consolev1.ErrorField_ERROR_FIELD_SERVICE: fService, consolev1.ErrorField_ERROR_FIELD_ENVIRONMENT: fEnv,
		consolev1.ErrorField_ERROR_FIELD_TYPE: fType, consolev1.ErrorField_ERROR_FIELD_MESSAGE: fMessage,
		consolev1.ErrorField_ERROR_FIELD_RELEASE: fRelease, consolev1.ErrorField_ERROR_FIELD_TRACE_ID: fTrace,
		consolev1.ErrorField_ERROR_FIELD_RUNTIME_ID: fRuntime, consolev1.ErrorField_ERROR_FIELD_GROUP_KEY: fGroup,
	}
	indexFields = []logsql.Field{
		fTime, fStream, fTrace, fSpan, fSeverity, fEventName, fService, fEnv, fRelease, fType, fMessage,
		fSchema, fKind, fEventID, fRuntime, fSequence, fMechanism, fHandled, fGroup,
	}
	uuidV4    = `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
	traceHex  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	spanHex   = regexp.MustCompile(`^[0-9a-f]{16}$`)
	hashValue = regexp.MustCompile(`^\d{1,20}$`)
	// sdkMatch is a browser SDK record; otelMatch any other exception log.
	sdkMatch  = logsql.And(fKind.Eq(kindException), fEventID.Regexp(uuidV4))
	otelMatch = logsql.And(logsql.Or(fType.Set(), fMessage.Set(), fStack.Set(), fEventName.Eq(kindException),
		fKind.Eq(kindException)), logsql.Not(sdkMatch))
)

// Errors serves ErrorService over the log and trace stores of Service.
type Errors struct {
	consolev1.UnimplementedErrorServiceServer
	obs    *Service
	schema *jsonschema.Schema
}

// Errors is the error reader over s's stores.
func (s *Service) Errors() (*Errors, error) {
	schema, err := envelope.Schema()
	if err != nil {
		return nil, fmt.Errorf("error envelope schema: %w", err)
	}

	return &Errors{obs: s, schema: schema}, nil
}

// Register serves ErrorService on the console.
func (e *Errors) Register(registrar grpc.ServiceRegistrar) {
	consolev1.RegisterErrorServiceServer(registrar, e)
}

type row map[string]string

// errorQuery is a checked filter: its range, the user conditions (without
// origin) and which origins take part.
type errorQuery struct {
	start, end time.Time
	filter     *consolev1.ErrorFilter
	sdk, otel  bool
}

func invalid(message string) error { return rpcError(codes.InvalidArgument, message) }

func checkFilter(filter *consolev1.ErrorFilter) (errorQuery, error) {
	scope := errorQuery{filter: filter, sdk: true, otel: true}
	if filter.GetStart() == nil || filter.GetEnd() == nil ||
		filter.GetStart().CheckValid() != nil || filter.GetEnd().CheckValid() != nil {
		return scope, invalid("error filter needs a valid start and end")
	}

	scope.start, scope.end = filter.GetStart().AsTime(), filter.GetEnd().AsTime()
	if !scope.end.After(scope.start) || scope.end.Sub(scope.start) > maxErrorRange {
		return scope, invalid("error range must be positive and when most 31 days")
	}

	if len(filter.GetConditions()) > maxErrorConditions || len(filter.GetText()) > maxErrorValueBytes {
		return scope, invalid("error filter exceeds its limits")
	}

	for _, c := range filter.GetConditions() {
		if err := checkCondition(c); err != nil {
			return scope, err
		}

		if c.GetField() == consolev1.ErrorField_ERROR_FIELD_ORIGIN {
			scope.sdk, scope.otel = scope.sdk && originHolds(c, originSDK), scope.otel && originHolds(c, originOTel)
		}
	}

	return scope, nil
}

func originHolds(c *consolev1.ErrorCondition, origin string) bool {
	return slices.Contains(c.GetValues(), origin) == (c.GetOp() == consolev1.ErrorOperator_ERROR_OPERATOR_IS)
}

func checkCondition(c *consolev1.ErrorCondition) error {
	if len(c.GetValues()) > maxErrorValues {
		return invalid("error condition has too many values")
	}

	for _, v := range c.GetValues() {
		if len(v) > maxErrorValueBytes {
			return invalid("error condition value exceeds byte budget")
		}
	}

	exists := c.GetOp() == consolev1.ErrorOperator_ERROR_OPERATOR_EXISTS ||
		c.GetOp() == consolev1.ErrorOperator_ERROR_OPERATOR_NOT_EXISTS
	if c.GetOp() == consolev1.ErrorOperator_ERROR_OPERATOR_UNSPECIFIED || exists == (len(c.GetValues()) > 0) {
		return invalid("error condition needs an operator and values (exists: none)")
	}

	if c.GetField() == consolev1.ErrorField_ERROR_FIELD_ORIGIN {
		if c.GetOp() != consolev1.ErrorOperator_ERROR_OPERATOR_IS &&
			c.GetOp() != consolev1.ErrorOperator_ERROR_OPERATOR_IS_NOT {
			return invalid("origin takes is or is not")
		}

		for _, v := range c.GetValues() {
			if v != originSDK && v != originOTel {
				return invalid("origin is sdk or otel-log")
			}
		}

		return nil
	}

	if _, ok := errorFields[c.GetField()]; !ok {
		return invalid("unknown error field")
	}

	return nil
}

// user is the conditions and text as a filter, without skip's field (a
// facet shows the values its own conditions would exclude).
func (scope errorQuery) user(skip consolev1.ErrorField) logsql.Filter {
	parts := []logsql.Filter{}

	for _, c := range scope.filter.GetConditions() {
		if c.GetField() == consolev1.ErrorField_ERROR_FIELD_ORIGIN || c.GetField() == skip {
			continue
		}

		parts = append(parts, condition(errorFields[c.GetField()], c))
	}

	if text := scope.filter.GetText(); text != "" {
		parts = append(parts, fMessage.Contains(text, true))
	}

	return logsql.And(parts...)
}

func condition(field logsql.Field, c *consolev1.ErrorCondition) logsql.Filter {
	each := func(match func(string) logsql.Filter) logsql.Filter {
		parts := make([]logsql.Filter, 0, len(c.GetValues()))
		for _, v := range c.GetValues() {
			parts = append(parts, match(v))
		}

		return logsql.Or(parts...)
	}

	switch c.GetOp() {
	case consolev1.ErrorOperator_ERROR_OPERATOR_IS:
		return each(field.Eq)
	case consolev1.ErrorOperator_ERROR_OPERATOR_IS_NOT:
		return logsql.Not(each(field.Eq))
	case consolev1.ErrorOperator_ERROR_OPERATOR_CONTAINS:
		return each(func(v string) logsql.Filter { return field.Contains(v, true) })
	case consolev1.ErrorOperator_ERROR_OPERATOR_NOT_CONTAINS:
		return logsql.Not(each(func(v string) logsql.Filter { return field.Contains(v, true) }))
	case consolev1.ErrorOperator_ERROR_OPERATOR_PREFIX:
		return each(field.Prefix)
	case consolev1.ErrorOperator_ERROR_OPERATOR_EXISTS:
		return field.Set()
	default: // NOT_EXISTS, checked
		return logsql.Not(field.Set())
	}
}

// logs runs query over [start, end) and reads when most limit rows; more is
// partial.
func (e *Errors) logs(ctx context.Context, query logsql.Query, limit int) ([]row, bool, error) {
	release, err := e.obs.acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	defer release()

	body, info, err := e.obs.fetch(ctx, consolev1.ObsSignal_OBS_SIGNAL_LOGS, "/select/logsql/query",
		url.Values{"query": {query.String()}})
	if err != nil {
		return nil, false, err
	}

	rows, err := readRows(body, limit)
	if errors.Is(err, errRowBudget) {
		return rows, true, nil
	}

	return rows, info.GetPartial(), err
}

var errRowBudget = errors.New("row budget")

func readRows(body []byte, limit int) ([]row, error) {
	reader := bufio.NewReader(bytes.NewReader(body))
	rows := []row{}

	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var value row
			if json.Unmarshal(line, &value) != nil || value == nil {
				return nil, malformed()
			}

			if len(rows) == limit {
				return rows, errRowBudget
			}

			rows = append(rows, value)
		}

		if errors.Is(err, io.EOF) {
			return rows, nil
		}

		if err != nil {
			return nil, malformed()
		}
	}
}

// branch is one origin's filter over the range and conditions.
type branch struct {
	origin string
	filter logsql.Filter
}

// branches are the origins' filters, SDK first (a fixed order keeps pages
// stable among equal times).
func (scope errorQuery) branches(skip consolev1.ErrorField) []branch {
	out := []branch{}
	span, user := logsql.Range(scope.start, scope.end), scope.user(skip)

	if scope.sdk {
		out = append(out, branch{originSDK, logsql.And(span, sdkMatch, user)})
	}

	if scope.otel {
		out = append(out, branch{originOTel, logsql.And(span, otelMatch, user)})
	}

	return out
}

type pageCursor struct {
	Version int    `json:"v"`
	Filter  string `json:"f"`
	Offset  int    `json:"o"`
}

func filterHash(filter proto.Message) string {
	encoded, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(filter)
	sum := sha256.Sum256(encoded)

	return hex.EncodeToString(sum[:])
}

func encode(value any) string {
	encoded, _ := json.Marshal(value) //nolint:errchkjson // plain structs

	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decode(raw string, value any) bool {
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)

	return err == nil && len(raw) <= 4096 && json.Unmarshal(data, value) == nil
}

// SearchErrors reads a page of occurrences, newest first.
func (e *Errors) SearchErrors(
	ctx context.Context, req *consolev1.SearchErrorsRequest,
) (*consolev1.SearchErrorsResponse, error) {
	scope, err := checkFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}

	size, offset, hash, err := page(req)
	if err != nil {
		return nil, err
	}

	response := &consolev1.SearchErrorsResponse{}

	rows, err := e.searchRows(ctx, scope, offset+size+1, response)
	if err != nil {
		return nil, err
	}

	if offset < len(rows) {
		rows = rows[offset:]
	} else {
		rows = nil
	}

	if len(rows) > size {
		rows = rows[:size]
		if offset+size <= maxErrorOffset {
			response.NextPageCursor = encode(pageCursor{Version: 1, Filter: hash, Offset: offset + size})
		} else {
			response.Partial, response.Warnings = true, append(response.GetWarnings(), "offset_budget_exceeded")
		}
	}

	collect(rows, response)

	return response, nil
}

// page is the request's page size and offset, and its filter's hash.
func page(req *consolev1.SearchErrorsRequest) (int, int, string, error) {
	size := int(req.GetPageSize())
	if size == 0 {
		size = defaultErrorPage
	}

	if size > maxErrorPage {
		return 0, 0, "", invalid("error page exceeds maximum")
	}

	hash := filterHash(req.GetFilter())
	if raw := req.GetPageCursor(); raw != "" {
		var c pageCursor
		if !decode(raw, &c) || c.Version != 1 || c.Filter != hash || c.Offset < 0 || c.Offset > maxErrorOffset {
			return 0, 0, "", invalid("error cursor does not match the filter")
		}

		return size, c.Offset, hash, nil
	}

	return size, 0, hash, nil
}

// searchRows reads each origin's newest limit rows, merged newest first.
func (e *Errors) searchRows(
	ctx context.Context, scope errorQuery, limit int, response *consolev1.SearchErrorsResponse,
) ([]row, error) {
	rows := []row{}

	for _, b := range scope.branches(consolev1.ErrorField_ERROR_FIELD_UNSPECIFIED) {
		query := logsql.From(b.filter)
		order := []logsql.Order{{Field: fTime, Desc: true}, {Field: fEventID, Desc: true}, {Field: fStream, Desc: true}}

		if b.origin == originSDK {
			query = query.Fields(indexFields...).LastBy(fTime, fEventID)
		} else {
			query = query.Hash(fHash).Fields(append(slices.Clone(indexFields), fHash)...)
			order = append(order, logsql.Order{Field: fHash, Desc: true})
		}

		part, partial, err := e.logs(ctx, query.Sort(limit, order...), limit)
		if err != nil {
			return nil, err
		}

		response.Partial = response.GetPartial() || partial

		rows = append(rows, part...)
	}

	slices.SortStableFunc(rows, func(a, b row) int { return compareTime(b[string(fTime)], a[string(fTime)]) })

	return rows, nil
}

// collect adds the rows' occurrences, one per event id.
func collect(rows []row, response *consolev1.SearchErrorsResponse) {
	seen := map[string]bool{}

	for _, r := range rows {
		occurrence, ok := occurrenceOf(r)
		if !ok {
			response.Partial, response.Warnings = true, appendOnce(response.GetWarnings(), "invalid_stored_index")

			continue
		}

		if id := occurrence.GetEventId(); id != "" {
			if seen[id] {
				continue
			}

			seen[id] = true
		}

		response.Occurrences = append(response.Occurrences, occurrence)
	}
}

func appendOnce(list []string, value string) []string {
	if slices.Contains(list, value) {
		return list
	}

	return append(list, value)
}

func parseTime(value string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, value)

	return t, err == nil && t.UnixNano() >= 0
}

func compareTime(a, b string) int {
	when, _ := parseTime(a)
	bt, _ := parseTime(b)

	return when.Compare(bt)
}

// locator identifies one occurrence: its event id, or its stream and row
// hash, with its time.
type locator struct {
	Version int    `json:"v"`
	EventID string `json:"e,omitempty"`
	Time    int64  `json:"t"`
	Stream  string `json:"s,omitempty"`
	Hash    string `json:"h,omitempty"`
}

func validUUID(value string) bool {
	id, err := uuid.Parse(value)

	return err == nil && id.Version() == 4 && id.String() == value
}

func occurrenceOf(r row) (*consolev1.ErrorOccurrence, bool) {
	when, ok := parseTime(r[string(fTime)])
	if !ok {
		return nil, false
	}

	o := &consolev1.ErrorOccurrence{
		Origin: consolev1.ErrorOrigin_ERROR_ORIGIN_OTEL_LOG, Time: timestamppb.New(when), Service: r[string(fService)],
		Environment: r[string(fEnv)], Release: r[string(fRelease)], Type: r[string(fType)], Message: r[string(fMessage)],
		Mechanism: r[string(fMechanism)], GroupKey: r[string(fGroup)],
	}

	if severity, err := strconv.ParseInt(r[string(fSeverity)], 10, 32); err == nil {
		o.Severity = int32(severity)
	}

	if trace := r[string(fTrace)]; traceHex.MatchString(trace) {
		o.TraceId = trace
		if span := r[string(fSpan)]; spanHex.MatchString(span) {
			o.SpanId = span
		}
	}

	if runtime := r[string(fRuntime)]; validUUID(runtime) {
		o.RuntimeId = runtime
	}

	loc := locator{Version: 1, Time: when.UnixNano()}

	if r[string(fKind)] == kindException && validUUID(r[string(fEventID)]) {
		o.Origin, o.EventId, loc.EventID = consolev1.ErrorOrigin_ERROR_ORIGIN_SDK, r[string(fEventID)], r[string(fEventID)]
	} else {
		if r[string(fKind)] == kindException {
			o.Origin = consolev1.ErrorOrigin_ERROR_ORIGIN_SDK
		}

		loc.Stream, loc.Hash = r[string(fStream)], r[string(fHash)]
		if loc.Hash == "" {
			return nil, false
		}
	}

	o.Ref = encode(loc)

	return o, true
}

func parseLocator(raw string) (locator, error) {
	var loc locator
	if !decode(raw, &loc) || loc.Version != 1 || loc.Time < 0 {
		return loc, invalid("invalid error reference")
	}

	switch {
	case loc.EventID != "":
		if !validUUID(loc.EventID) || loc.Stream != "" || loc.Hash != "" {
			return loc, invalid("invalid error reference")
		}
	case !hashValue.MatchString(loc.Hash) || loc.Stream == "" || len(loc.Stream) > 1024:
		return loc, invalid("invalid error reference")
	}

	return loc, nil
}

// GetError reads one occurrence in full.
func (e *Errors) GetError(
	ctx context.Context, req *consolev1.GetErrorRequest,
) (*consolev1.GetErrorResponse, error) {
	loc, err := parseLocator(req.GetRef())
	if err != nil {
		return nil, err
	}

	rows, partial, err := e.record(ctx, loc)
	if err != nil {
		return nil, err
	}

	response := e.detail(rows[0])
	if partial || len(rows) > maxDetailRows {
		response.Warnings = append(response.GetWarnings(), "detail_row_budget_exceeded")
	}

	if len(rows) > 1 {
		if loc.EventID == "" {
			response.Warnings = append(response.GetWarnings(), "ambiguous_identical_logs")
		}

		for _, other := range rows[1:] {
			if other[string(fBody)] != rows[0][string(fBody)] {
				response.Warnings = append(response.GetWarnings(), "event_id_body_conflict")

				break
			}
		}
	}

	return response, nil
}

// record reads the stored rows of an occurrence (more than one: retried
// with the same event id, or identical OTel logs).
func (e *Errors) record(ctx context.Context, loc locator) ([]row, bool, error) {
	when := time.Unix(0, loc.Time)
	span := logsql.Range(when, when.Add(time.Nanosecond))

	query := logsql.From(logsql.And(span, fEventID.Eq(loc.EventID)))
	if loc.EventID == "" {
		query = logsql.From(logsql.And(span, fStream.Eq(loc.Stream))).Hash(fHash).Where(fHash.Eq(loc.Hash))
	}

	rows, partial, err := e.logs(ctx, query.Sort(maxDetailRows+1, logsql.Order{Field: fTime}), maxDetailRows+1)
	if err != nil {
		return nil, false, err
	}

	if len(rows) == 0 {
		return nil, false, rpcError(codes.NotFound, "error not found (it may have aged out of the log store)")
	}

	return rows, partial, nil
}

func (e *Errors) detail(r row) *consolev1.GetErrorResponse {
	occurrence, _ := occurrenceOf(withHash(r))
	response := &consolev1.GetErrorResponse{
		Occurrence: occurrence, Stacktrace: r[string(fStack)], Fields: map[string]string{},
	}

	for key, value := range r {
		if key != string(fBody) && key != string(fHash) {
			response.Fields[key] = value
		}
	}

	if occurrence.GetOrigin() == consolev1.ErrorOrigin_ERROR_ORIGIN_SDK {
		e.envelope(r, response)
	}

	return response
}

// withHash keeps a row decodable when its hash was not asked for.
func withHash(r row) row {
	if r[string(fHash)] != "" {
		return r
	}

	out := row{string(fHash): "0"}
	for k, v := range r {
		out[k] = v
	}

	return out
}

// envelope decodes and checks the SDK's envelope; failures are its status,
// not a failure to read the occurrence.
func (e *Errors) envelope(r row, response *consolev1.GetErrorResponse) {
	fail := func(status, warning string) {
		response.EnvelopeStatus, response.Warnings = status, append(response.GetWarnings(), warning)
	}

	body, err := envelope.Decode(r[string(fBody)])
	if err != nil || body["schema"] != "app-debug" {
		fail("invalid", "invalid_payload")

		return
	}

	if fmt.Sprint(body["schemaVersion"]) != "1" {
		fail("unsupported_version", "unsupported_version")

		return
	}

	if exception, ok := body["exception"].(map[string]any); ok {
		if stack, isText := exception["stacktrace"].(string); isText && response.GetStacktrace() == "" {
			response.Stacktrace = stack
		}
	}

	if e.schema.Validate(body) != nil {
		fail("invalid", "invalid_payload")

		return
	}

	plain, err := plainJSON(body)
	if err != nil {
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

	if mismatch(r, body) {
		response.Warnings = append(response.GetWarnings(), "index_payload_mismatch")
	}
}

// plainJSON turns json.Number values into float64 (structpb's numbers).
func plainJSON(value map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode envelope: %w", err)
	}

	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}

	return out, nil
}

// mismatch: the indexed fields disagree with the envelope they came from.
func mismatch(r row, body map[string]any) bool {
	exception, _ := body["exception"].(map[string]any)
	runtime, _ := body["runtime"].(map[string]any)
	expected := map[logsql.Field]any{
		fEventID: body["eventId"], fSchema: body["schemaVersion"], fKind: body["kind"], fGroup: body["groupKey"],
		fRuntime: runtime["id"], fSequence: runtime["sequence"], fType: exception["type"], fMessage: exception["message"],
		fStack: exception["stacktrace"], fMechanism: exception["mechanism"], fHandled: exception["handled"],
		fTrace: nil, fSpan: nil,
	}

	if link, ok := body["trace"].(map[string]any); ok {
		expected[fTrace], expected[fSpan] = link["traceId"], link["spanId"]
	}

	for field, value := range expected {
		stored, exists := r[string(field)]
		if value == nil {
			if exists && stored != "" {
				return true
			}

			continue
		}

		if !exists || stored != fmt.Sprint(value) {
			return true
		}
	}

	return false
}

// ErrorHistogram counts occurrences per bucket.
func (e *Errors) ErrorHistogram(
	ctx context.Context, req *consolev1.ErrorHistogramRequest,
) (*consolev1.ErrorHistogramResponse, error) {
	scope, err := checkFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}

	step := histogramStep(scope.end.Sub(scope.start))
	response := &consolev1.ErrorHistogramResponse{StepMillis: step.Milliseconds()}
	counts := map[int64]uint64{}

	for _, b := range scope.branches(consolev1.ErrorField_ERROR_FIELD_UNSPECIFIED) {
		query := logsql.From(b.filter)
		if b.origin == originSDK {
			query = query.Fields(fTime, fEventID).LastBy(fTime, fEventID)
		}

		rows, partial, err := e.logs(ctx, query.CountByTime(fTime, step, fHits), histogramBuckets+histogramEdges)
		if err != nil {
			return nil, err
		}

		response.Partial = response.GetPartial() || partial

		for _, r := range rows {
			when, ok := parseTime(r[string(fTime)])
			hits, err := strconv.ParseUint(r[string(fHits)], 10, 64)

			if !ok || err != nil {
				return nil, malformed()
			}

			counts[when.UnixNano()] += hits
		}
	}

	for when := scope.start.Truncate(step); when.Before(scope.end); when = when.Add(step) {
		count := counts[when.UnixNano()]
		response.Total += count
		response.Buckets = append(response.Buckets, &consolev1.ErrorBucket{Start: timestamppb.New(when), Count: count})
	}

	return response, nil
}

func histogramStep(span time.Duration) time.Duration {
	for _, step := range []time.Duration{
		time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond, time.Second, 5 * time.Second,
		10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour,
		6 * time.Hour, 24 * time.Hour,
	} {
		if span/step <= histogramBuckets {
			return step
		}
	}

	return time.Duration((int64(span)/histogramBuckets/int64(time.Millisecond) + 1) * int64(time.Millisecond))
}

// ErrorFacets lists each field's most frequent values.
func (e *Errors) ErrorFacets(
	ctx context.Context, req *consolev1.ErrorFacetsRequest,
) (*consolev1.ErrorFacetsResponse, error) {
	scope, err := checkFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}

	limit := int(req.GetLimit())
	if limit == 0 {
		limit = defaultErrorFacet
	}

	if limit > maxErrorFacet || len(req.GetFields()) > len(errorFields) {
		return nil, invalid("error facets exceed their limits")
	}

	response := &consolev1.ErrorFacetsResponse{}

	for _, field := range req.GetFields() {
		stored, ok := errorFields[field]
		if !ok {
			return nil, invalid("unknown error facet field")
		}

		facet, partial, err := e.facet(ctx, scope, field, stored, limit)
		if err != nil {
			return nil, err
		}

		response.Partial = response.GetPartial() || partial
		response.Facets = append(response.Facets, facet)
	}

	return response, nil
}

func (e *Errors) facet(
	ctx context.Context, scope errorQuery, field consolev1.ErrorField, stored logsql.Field, limit int,
) (*consolev1.ErrorFacet, bool, error) {
	branches := scope.branches(field)
	parts := make([]logsql.Filter, 0, len(branches))

	for _, b := range branches {
		parts = append(parts, b.filter)
	}

	query := logsql.From(logsql.And(logsql.Or(parts...), stored.Set())).
		Stats(stored, logsql.CountUniqIf(fEventID, sdkMatch, fSDKCount), logsql.CountIf(otelMatch, fOTelCount),
			logsql.Max(fTime, fLastSeen)).
		Sum(fSDKCount, fOTelCount, fCount).
		Sort(limit, logsql.Order{Field: fCount, Desc: true}, logsql.Order{Field: stored}).
		Fields(stored, fCount, fLastSeen)

	facet := &consolev1.ErrorFacet{Field: field}
	if len(parts) == 0 {
		return facet, false, nil
	}

	rows, partial, err := e.logs(ctx, query, limit)
	if err != nil {
		return nil, false, err
	}

	for _, r := range rows {
		count, err := strconv.ParseFloat(r[string(fCount)], 64)
		seen, ok := parseTime(r[string(fLastSeen)])

		if err != nil || !ok || count < 1 {
			return nil, false, malformed()
		}

		facet.Values = append(facet.Values, &consolev1.ErrorFacetValue{
			Value: r[string(stored)], Count: uint64(count), LastSeen: timestamppb.New(seen),
		})
	}

	return facet, partial, nil
}

// RelatedLogs reads logs related to an occurrence.
func (e *Errors) RelatedLogs(
	ctx context.Context, req *consolev1.RelatedLogsRequest,
) (*consolev1.RelatedLogsResponse, error) {
	loc, err := parseLocator(req.GetRef())
	if err != nil {
		return nil, err
	}

	size := int(req.GetPageSize())
	if size == 0 {
		size = defaultErrorPage
	}

	if size > maxErrorPage {
		return nil, invalid("related page exceeds maximum")
	}

	start, end, err := relatedRange(req, loc)
	if err != nil {
		return nil, err
	}

	rows, _, err := e.record(ctx, loc)
	if err != nil {
		return nil, err
	}

	detail := e.detail(rows[0])
	if slices.Contains(detail.GetWarnings(), "index_payload_mismatch") ||
		slices.Contains(detail.GetWarnings(), "event_id_body_conflict") {
		return &consolev1.RelatedLogsResponse{Status: statusUnavailable, Reason: "conflicting_identifiers"}, nil
	}

	key := relation(req.GetRelation(), detail.GetOccurrence())
	if key.IsZero() {
		if req.GetRelation() == consolev1.ErrorRelation_ERROR_RELATION_UNSPECIFIED {
			return nil, invalid("unknown relation")
		}

		return &consolev1.RelatedLogsResponse{Status: statusNotFound, Reason: "correlation_key_absent"}, nil
	}

	related, partial, err := e.logs(ctx, logsql.From(logsql.And(logsql.Range(start, end), key)).
		Sort(size+1, logsql.Order{Field: fTime}), size+1)
	if err != nil {
		//nolint:nilerr // the log store's failure is the response's state
		return &consolev1.RelatedLogsResponse{Status: statusUnavailable, Reason: "log_store_unavailable"}, nil
	}

	return relatedResponse(related, partial, size), nil
}

// relatedRange is the request's range, else a window around the occurrence.
func relatedRange(req *consolev1.RelatedLogsRequest, loc locator) (time.Time, time.Time, error) {
	when := time.Unix(0, loc.Time)
	if req.GetStart() == nil && req.GetEnd() == nil {
		return when.Add(-relatedWindow), when.Add(relatedWindow), nil
	}

	start, end := req.GetStart().AsTime(), req.GetEnd().AsTime()
	if req.GetStart().CheckValid() != nil || req.GetEnd().CheckValid() != nil || !end.After(start) ||
		end.Sub(start) > maxErrorRange {
		return start, end, invalid("invalid related range")
	}

	return start, end, nil
}

func relation(kind consolev1.ErrorRelation, o *consolev1.ErrorOccurrence) logsql.Filter {
	switch kind {
	case consolev1.ErrorRelation_ERROR_RELATION_SAME_SPAN:
		if o.GetTraceId() != "" && o.GetSpanId() != "" {
			return logsql.And(fTrace.Eq(o.GetTraceId()), fSpan.Eq(o.GetSpanId()))
		}
	case consolev1.ErrorRelation_ERROR_RELATION_SAME_TRACE:
		if o.GetTraceId() != "" {
			return fTrace.Eq(o.GetTraceId())
		}
	case consolev1.ErrorRelation_ERROR_RELATION_SAME_RUNTIME:
		if o.GetRuntimeId() != "" {
			return fRuntime.Eq(o.GetRuntimeId())
		}
	case consolev1.ErrorRelation_ERROR_RELATION_TIME_WINDOW:
		if o.GetService() != "" {
			return fService.Eq(o.GetService())
		}

		return logsql.All()
	default:
	}

	return logsql.Filter{}
}

func relatedResponse(rows []row, partial bool, size int) *consolev1.RelatedLogsResponse {
	response := &consolev1.RelatedLogsResponse{Status: statusAvailable}
	if len(rows) == 0 {
		return &consolev1.RelatedLogsResponse{Status: statusNotFound}
	}

	if len(rows) > size {
		rows, partial = rows[:size], true
		response.Reason = "row_limit"
	}

	for _, r := range rows {
		when, ok := parseTime(r[string(fTime)])
		if !ok {
			partial, response.Reason = true, "invalid_stored_time"

			continue
		}

		response.Logs = append(response.Logs, &consolev1.RelatedLog{Time: timestamppb.New(when), Fields: r})
	}

	if partial {
		response.Status = statusPartial
		if response.GetReason() == "" {
			response.Reason = "upstream_partial"
		}
	}

	return response
}
