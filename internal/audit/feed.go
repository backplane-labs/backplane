package audit

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gopherex/xlog"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/store/db"
)

const (
	defaultPage     = 100
	maxPage         = 500
	maxCursorBytes  = 2048
	maxFilterBytes  = 2048
	maxConditions   = 32
	maxValues       = 64
	defaultBuckets  = 60
	maxBuckets      = 240
	maxFields       = 200
	defaultFacet    = 10
	maxFacet        = 50
	maxFacetTargets = 16
	sourcePlatform  = "platform"
)

//nolint:gochecknoglobals // immutable tables
var (
	// columns are the feed's columns of the fixed fields.
	columns = map[consolev1.AuditField]string{
		consolev1.AuditField_AUDIT_FIELD_SOURCE: "source", consolev1.AuditField_AUDIT_FIELD_SERVICE: "service",
		consolev1.AuditField_AUDIT_FIELD_ACTION: "action", consolev1.AuditField_AUDIT_FIELD_ACTOR: "actor",
		consolev1.AuditField_AUDIT_FIELD_SUBJECT: "subject", consolev1.AuditField_AUDIT_FIELD_OUTCOME: "outcome",
		consolev1.AuditField_AUDIT_FIELD_OPERATION: "operation", consolev1.AuditField_AUDIT_FIELD_SEVERITY: "severity",
		consolev1.AuditField_AUDIT_FIELD_TRACE_ID: "trace_id",
	}
	// ops name the operators in the feed query's conditions.
	ops = map[consolev1.AuditOperator]string{
		consolev1.AuditOperator_AUDIT_OPERATOR_IS:           "is",
		consolev1.AuditOperator_AUDIT_OPERATOR_IS_NOT:       "is_not",
		consolev1.AuditOperator_AUDIT_OPERATOR_CONTAINS:     "contains",
		consolev1.AuditOperator_AUDIT_OPERATOR_NOT_CONTAINS: "not_contains",
		consolev1.AuditOperator_AUDIT_OPERATOR_PREFIX:       "prefix",
		consolev1.AuditOperator_AUDIT_OPERATOR_EXISTS:       "exists",
		consolev1.AuditOperator_AUDIT_OPERATOR_NOT_EXISTS:   "not_exists",
		consolev1.AuditOperator_AUDIT_OPERATOR_GT:           "gt",
		consolev1.AuditOperator_AUDIT_OPERATOR_GTE:          "gte",
		consolev1.AuditOperator_AUDIT_OPERATOR_LT:           "lt",
		consolev1.AuditOperator_AUDIT_OPERATOR_LTE:          "lte",
	}
	likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	steps       = []time.Duration{
		time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute,
		10 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour,
		12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour,
	}
)

func rpcError(code codes.Code, message string) error {
	return status.Error(code, message) //nolint:wrapcheck // RPC boundary
}

func invalid(message string) error { return rpcError(codes.InvalidArgument, message) }

// feedFilter is an AuditFilter as the feed queries take it: equality on
// fields as arrays, single-valued attribute equality as one object, every
// other condition as a JSON element of Conditions.
type feedFilter struct {
	StartAt, EndAt                               *time.Time
	Sources, Services, Actions, Actors, Subjects []string
	Outcomes, Operations, Severities, TraceIDs   []string
	AttributesMust, Conditions                   *json.RawMessage
	Text                                         *string
}

// condition is one element of the feed query's @conditions.
type condition struct {
	Field    string            `json:"field,omitempty"`
	Key      string            `json:"key,omitempty"`
	Op       string            `json:"op"`
	Values   []json.RawMessage `json:"values,omitempty"`
	Patterns []string          `json:"patterns,omitempty"`
	Number   *float64          `json:"number,omitempty"`
}

// feedOf checks filter and lays it out; skip's target is left out (a facet
// shows the values its own conditions would exclude).
func feedOf(filter *consolev1.AuditFilter, skip *consolev1.AuditCondition) (feedFilter, error) {
	var out feedFilter

	if len(filter.GetConditions()) > maxConditions || len(filter.GetText()) > maxFilterBytes {
		return out, invalid("audit filter exceeds its limits")
	}

	if err := timeRange(filter, &out); err != nil {
		return out, err
	}

	if text := filter.GetText(); text != "" {
		pattern := "%" + likeEscaper.Replace(text) + "%"
		out.Text = &pattern
	}

	must := map[string]json.RawMessage{}

	var rest []condition

	for _, c := range filter.GetConditions() {
		if err := check(c); err != nil {
			return out, err
		}

		if skip != nil && sameTarget(c, skip) {
			continue
		}

		if out.placed(c, must) {
			continue
		}

		rest = append(rest, conditionOf(c))
	}

	if len(must) > 0 {
		encoded, _ := json.Marshal(must) //nolint:errchkjson // JSON values
		raw := json.RawMessage(encoded)
		out.AttributesMust = &raw
	}

	if len(rest) > 0 {
		encoded, _ := json.Marshal(rest) //nolint:errchkjson // plain struct
		raw := json.RawMessage(encoded)
		out.Conditions = &raw
	}

	return out, nil
}

// placed puts equality where an index serves it: a field's first IS as its
// array, an attribute's single-valued IS into the object it must contain.
func (f *feedFilter) placed(c *consolev1.AuditCondition, must map[string]json.RawMessage) bool {
	if c.GetOp() != consolev1.AuditOperator_AUDIT_OPERATOR_IS {
		return false
	}

	if key := c.GetAttribute(); key != "" {
		if _, taken := must[key]; taken || len(c.GetValues()) != 1 {
			return false
		}

		must[key] = jsonOf(c.GetValues()[0])

		return true
	}

	slot := f.slot(c.GetField())
	if slot == nil || *slot != nil {
		return false
	}

	texts := make([]string, 0, len(c.GetValues()))
	for _, v := range c.GetValues() {
		texts = append(texts, textOf(v))
	}

	*slot = texts

	return true
}

func (f *feedFilter) slot(field consolev1.AuditField) *[]string {
	switch field {
	case consolev1.AuditField_AUDIT_FIELD_SOURCE:
		return &f.Sources
	case consolev1.AuditField_AUDIT_FIELD_SERVICE:
		return &f.Services
	case consolev1.AuditField_AUDIT_FIELD_ACTION:
		return &f.Actions
	case consolev1.AuditField_AUDIT_FIELD_ACTOR:
		return &f.Actors
	case consolev1.AuditField_AUDIT_FIELD_SUBJECT:
		return &f.Subjects
	case consolev1.AuditField_AUDIT_FIELD_OUTCOME:
		return &f.Outcomes
	case consolev1.AuditField_AUDIT_FIELD_OPERATION:
		return &f.Operations
	case consolev1.AuditField_AUDIT_FIELD_SEVERITY:
		return &f.Severities
	case consolev1.AuditField_AUDIT_FIELD_TRACE_ID:
		return &f.TraceIDs
	default:
		return nil
	}
}

func conditionOf(c *consolev1.AuditCondition) condition {
	out := condition{Field: columns[c.GetField()], Key: c.GetAttribute(), Op: ops[c.GetOp()]}

	for _, v := range c.GetValues() {
		switch c.GetOp() {
		case consolev1.AuditOperator_AUDIT_OPERATOR_CONTAINS, consolev1.AuditOperator_AUDIT_OPERATOR_NOT_CONTAINS:
			out.Patterns = append(out.Patterns, "%"+likeEscaper.Replace(textOf(v))+"%")
		case consolev1.AuditOperator_AUDIT_OPERATOR_PREFIX:
			out.Patterns = append(out.Patterns, likeEscaper.Replace(textOf(v))+"%")
		case consolev1.AuditOperator_AUDIT_OPERATOR_GT, consolev1.AuditOperator_AUDIT_OPERATOR_GTE,
			consolev1.AuditOperator_AUDIT_OPERATOR_LT, consolev1.AuditOperator_AUDIT_OPERATOR_LTE:
			n := v.GetNumberValue()
			out.Number = &n
		default:
			if out.Field != "" { // a field is text
				v = structpb.NewStringValue(textOf(v))
			}

			out.Values = append(out.Values, jsonOf(v))
		}
	}

	return out
}

// check rejects a condition the feed cannot take.
func check(c *consolev1.AuditCondition) error {
	if len(c.GetValues()) > maxValues {
		return invalid("audit condition has too many values")
	}

	for _, v := range c.GetValues() {
		if len(jsonOf(v)) > maxFilterBytes {
			return invalid("audit condition value exceeds byte budget")
		}
	}

	if err := checkTarget(c); err != nil {
		return err
	}

	return checkOperator(c)
}

func checkTarget(c *consolev1.AuditCondition) error {
	switch t := c.GetTarget().(type) {
	case *consolev1.AuditCondition_Field:
		if _, ok := columns[t.Field]; !ok {
			return invalid("unknown audit field")
		}
	case *consolev1.AuditCondition_Attribute:
		if t.Attribute == "" || len(t.Attribute) > maxFilterBytes {
			return invalid("invalid audit attribute")
		}
	default:
		return invalid("audit condition has no field or attribute")
	}

	return nil
}

func checkOperator(c *consolev1.AuditCondition) error {
	values := c.GetValues()

	switch c.GetOp() {
	case consolev1.AuditOperator_AUDIT_OPERATOR_UNSPECIFIED:
		return invalid("audit condition has no operator")
	case consolev1.AuditOperator_AUDIT_OPERATOR_EXISTS, consolev1.AuditOperator_AUDIT_OPERATOR_NOT_EXISTS:
		if len(values) != 0 {
			return invalid("exists takes no values")
		}
	case consolev1.AuditOperator_AUDIT_OPERATOR_GT, consolev1.AuditOperator_AUDIT_OPERATOR_GTE,
		consolev1.AuditOperator_AUDIT_OPERATOR_LT, consolev1.AuditOperator_AUDIT_OPERATOR_LTE:
		if c.GetAttribute() == "" {
			return invalid("a field is compared as text: is, contains, prefix or exists")
		}

		if _, ok := oneNumber(values); !ok {
			return invalid("a comparison takes one number")
		}
	default:
		if len(values) == 0 {
			return invalid("audit condition has no values")
		}
	}

	return nil
}

func oneNumber(values []*structpb.Value) (float64, bool) {
	if len(values) != 1 {
		return 0, false
	}

	n, ok := values[0].GetKind().(*structpb.Value_NumberValue)
	if !ok {
		return 0, false
	}

	return n.NumberValue, true
}

func sameTarget(a, b *consolev1.AuditCondition) bool {
	if a.GetAttribute() != "" || b.GetAttribute() != "" {
		return a.GetAttribute() == b.GetAttribute()
	}

	return a.GetField() == b.GetField()
}

func timeRange(filter *consolev1.AuditFilter, out *feedFilter) error {
	if start := filter.GetStart(); start != nil {
		if err := start.CheckValid(); err != nil {
			return invalid("invalid audit start time")
		}

		at := start.AsTime()
		out.StartAt = &at
	}

	if end := filter.GetEnd(); end != nil {
		if err := end.CheckValid(); err != nil {
			return invalid("invalid audit end time")
		}

		at := end.AsTime()
		out.EndAt = &at
	}

	if out.StartAt != nil && out.EndAt != nil && !out.EndAt.After(*out.StartAt) {
		return invalid("invalid audit time range")
	}

	return nil
}

func jsonOf(v *structpb.Value) json.RawMessage {
	encoded, _ := v.MarshalJSON()

	return encoded
}

// textOf is a value as a field holds it: a string as is, anything else as
// JSON.
func textOf(v *structpb.Value) string {
	if s, ok := v.GetKind().(*structpb.Value_StringValue); ok {
		return s.StringValue
	}

	return string(jsonOf(v))
}

// pageCursor is the last record of a page, bound to the installation and
// the filter.
type pageCursor struct {
	Version      int       `json:"v"`
	Installation string    `json:"i"`
	Filter       string    `json:"f"`
	Time         time.Time `json:"t"`
	ID           uuid.UUID `json:"id"`
}

func filterHash(filter *consolev1.AuditFilter) string {
	encoded, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(filter)
	sum := sha256.Sum256(encoded)

	return hex.EncodeToString(sum[:])
}

func encodeCursor(c pageCursor) string {
	encoded, _ := json.Marshal(c) //nolint:errchkjson // plain struct

	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCursor(raw, installation, filter string) (pageCursor, error) {
	var c pageCursor

	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(raw) > maxCursorBytes || json.Unmarshal(decoded, &c) != nil ||
		c.Version != 1 || c.Installation != installation || c.Filter != filter {
		return c, invalid("audit cursor does not match installation/filter")
	}

	return c, nil
}

// SearchAudit reads a page of the feed, newest first.
func (s *Service) SearchAudit(
	ctx context.Context, req *consolev1.SearchAuditRequest,
) (*consolev1.SearchAuditResponse, error) {
	f, err := feedOf(req.GetFilter(), nil)
	if err != nil {
		return nil, err
	}

	size := int64(req.GetPageSize())
	if size == 0 {
		size = defaultPage
	}

	if size > maxPage {
		return nil, invalid("audit page exceeds maximum")
	}

	st := s.store.Get()
	installation, hash := st.Installation.String(), filterHash(req.GetFilter())
	params := db.SearchAuditParams{
		StartAt: f.StartAt, EndAt: f.EndAt, Sources: f.Sources, Services: f.Services, Actions: f.Actions,
		Actors: f.Actors, Subjects: f.Subjects, Outcomes: f.Outcomes, Operations: f.Operations,
		Severities: f.Severities, TraceIds: f.TraceIDs, AttributesMust: f.AttributesMust, Text: f.Text,
		Conditions: f.Conditions, PageSize: size + 1,
	}

	if raw := req.GetPageCursor(); raw != "" {
		after, cursorErr := decodeCursor(raw, installation, hash)
		if cursorErr != nil {
			return nil, cursorErr
		}

		params.BeforeTime, params.BeforeID = &after.Time, &after.ID
	}

	rows, err := st.Q.SearchAudit(ctx, params)
	if err != nil {
		return nil, s.databaseError(ctx, err)
	}

	response := &consolev1.SearchAuditResponse{}

	if int64(len(rows)) > size {
		rows = rows[:size]
		last := rows[len(rows)-1]
		response.NextPageCursor = encodeCursor(pageCursor{
			Version: 1, Installation: installation, Filter: hash, Time: last.Time, ID: last.ID,
		})
	}

	for i := range rows {
		response.Records = append(response.Records, recordOf(&rows[i]))
	}

	return response, nil
}

func recordOf(row *db.SearchAuditRow) *consolev1.AuditRecord {
	record := &consolev1.AuditRecord{
		Id: row.ID.String(), Source: consolev1.AuditSource_AUDIT_SOURCE_APPLICATION, Time: timestamppb.New(row.Time),
		ReceivedAt: timestamppb.New(row.ReceivedAt), Service: row.Service, Actor: row.Actor, Action: row.Action,
		Subject: row.Subject, Outcome: row.Outcome, Message: row.Message, Sequence: uint64(max(row.Sequence, 0)),
		Attributes: structOf(row.Attributes), Resource: structOf(row.Resource), Severity: row.Severity,
		TraceId: row.TraceID, SpanId: row.SpanID,
	}

	if row.Source == sourcePlatform {
		record.Source, record.OperationId = consolev1.AuditSource_AUDIT_SOURCE_PLATFORM, row.Operation
	}

	return record
}

func structOf(raw []byte) *structpb.Struct {
	out := &structpb.Struct{}
	if err := out.UnmarshalJSON(raw); err != nil {
		return &structpb.Struct{}
	}

	return out
}

// AuditHistogram counts matching records per bucket of the range.
func (s *Service) AuditHistogram(
	ctx context.Context, req *consolev1.AuditHistogramRequest,
) (*consolev1.AuditHistogramResponse, error) {
	f, err := feedOf(req.GetFilter(), nil)
	if err != nil {
		return nil, err
	}

	count := int(req.GetBuckets())
	if count == 0 {
		count = defaultBuckets
	}

	if count > maxBuckets {
		return nil, invalid("too many audit buckets")
	}

	st := s.store.Get()
	response := &consolev1.AuditHistogramResponse{}

	end := time.Now().UTC()
	if f.EndAt != nil {
		end = *f.EndAt
	}

	start, err := s.histogramStart(ctx, f)
	if err != nil || start == nil {
		return response, err
	}

	if !end.After(*start) {
		end = start.Add(time.Second)
	}

	step := stepFor(end.Sub(*start), count)
	origin := start.Truncate(step)
	response.StepSeconds = int64(step / time.Second)

	rows, err := st.Q.AuditHistogram(ctx, db.AuditHistogramParams{
		StepSeconds: step.Seconds(), Origin: origin, StartAt: &origin, EndAt: &end, Sources: f.Sources,
		Services: f.Services, Actions: f.Actions, Actors: f.Actors, Subjects: f.Subjects, Outcomes: f.Outcomes,
		Operations: f.Operations, Severities: f.Severities, TraceIds: f.TraceIDs, AttributesMust: f.AttributesMust,
		Text: f.Text, Conditions: f.Conditions,
	})
	if err != nil {
		return nil, s.databaseError(ctx, err)
	}

	counted := make(map[time.Time]db.AuditHistogramRow, len(rows))
	for _, row := range rows {
		if row.Bucket != nil {
			counted[row.Bucket.UTC()] = row
		}
	}

	for at := origin.UTC(); at.Before(end); at = at.Add(step) {
		row := counted[at]
		response.Buckets = append(response.Buckets, &consolev1.AuditBucket{
			Start: timestamppb.New(at), Platform: uint64(max(row.Platform, 0)),
			Application: uint64(max(row.Application, 0)), Failed: uint64(max(row.Failed, 0)),
		})
	}

	return response, nil
}

// histogramStart is the filter's start, else the first matching record's
// time; nil when nothing matches.
func (s *Service) histogramStart(ctx context.Context, f feedFilter) (*time.Time, error) {
	if f.StartAt != nil {
		return f.StartAt, nil
	}

	first, err := s.store.Get().Q.AuditFirstTime(ctx, db.AuditFirstTimeParams{
		EndAt: f.EndAt, Sources: f.Sources, Services: f.Services, Actions: f.Actions, Actors: f.Actors,
		Subjects: f.Subjects, Outcomes: f.Outcomes, Operations: f.Operations, Severities: f.Severities,
		TraceIds: f.TraceIDs, AttributesMust: f.AttributesMust, Text: f.Text, Conditions: f.Conditions,
	})
	if err != nil {
		return nil, s.databaseError(ctx, err)
	}

	return first.First, nil
}

// stepFor is the smallest round step spanning span in at most count
// buckets.
func stepFor(span time.Duration, count int) time.Duration {
	for _, step := range steps {
		if span <= step*time.Duration(count) {
			return step
		}
	}

	longest := steps[len(steps)-1]
	n := int64(span/longest)/int64(count) + 1

	return time.Duration(n * int64(longest))
}

// AuditFields lists the attribute keys of the matching records.
func (s *Service) AuditFields(
	ctx context.Context, req *consolev1.AuditFieldsRequest,
) (*consolev1.AuditFieldsResponse, error) {
	f, err := feedOf(req.GetFilter(), nil)
	if err != nil {
		return nil, err
	}

	rows, err := s.store.Get().Q.AuditFields(ctx, db.AuditFieldsParams{
		StartAt: f.StartAt, EndAt: f.EndAt, Sources: f.Sources, Services: f.Services, Actions: f.Actions,
		Actors: f.Actors, Subjects: f.Subjects, Outcomes: f.Outcomes, Operations: f.Operations,
		Severities: f.Severities, TraceIds: f.TraceIDs, AttributesMust: f.AttributesMust, Text: f.Text,
		Conditions: f.Conditions, LimitFields: maxFields,
	})
	if err != nil {
		return nil, s.databaseError(ctx, err)
	}

	response := &consolev1.AuditFieldsResponse{}

	for _, row := range rows {
		response.Fields = append(response.Fields, &consolev1.AuditFieldCount{
			Attribute: row.Attribute, Count: uint64(max(row.Count, 0)),
		})
	}

	return response, nil
}

// AuditFacets lists each target's most frequent values over the records
// matching the filter without the target's own conditions.
func (s *Service) AuditFacets(
	ctx context.Context, req *consolev1.AuditFacetsRequest,
) (*consolev1.AuditFacetsResponse, error) {
	limit := int64(req.GetLimit())
	if limit == 0 {
		limit = defaultFacet
	}

	if limit > maxFacet || len(req.GetTargets()) > maxFacetTargets {
		return nil, invalid("audit facets exceed their limits")
	}

	response := &consolev1.AuditFacetsResponse{}

	for _, target := range req.GetTargets() {
		facet, err := s.facet(ctx, req.GetFilter(), target, limit)
		if err != nil {
			return nil, err
		}

		response.Facets = append(response.Facets, facet)
	}

	return response, nil
}

func (s *Service) facet(
	ctx context.Context, filter *consolev1.AuditFilter, target *consolev1.AuditCondition, limit int64,
) (*consolev1.AuditFacet, error) {
	f, err := feedOf(filter, target)
	if err != nil {
		return nil, err
	}

	params := db.AuditFacetParams{
		StartAt: f.StartAt, EndAt: f.EndAt, Sources: f.Sources, Services: f.Services, Actions: f.Actions,
		Actors: f.Actors, Subjects: f.Subjects, Outcomes: f.Outcomes, Operations: f.Operations,
		Severities: f.Severities, TraceIds: f.TraceIDs, AttributesMust: f.AttributesMust, Text: f.Text,
		Conditions: f.Conditions, LimitValues: limit,
	}

	switch t := target.GetTarget().(type) {
	case *consolev1.AuditCondition_Field:
		column, ok := columns[t.Field]
		if !ok {
			return nil, invalid("unknown audit field")
		}

		params.TargetField = &column
	case *consolev1.AuditCondition_Attribute:
		if t.Attribute == "" {
			return nil, invalid("invalid audit attribute")
		}

		params.TargetKey = &t.Attribute
	default:
		return nil, invalid("audit facet has no field or attribute")
	}

	rows, err := s.store.Get().Q.AuditFacet(ctx, params)
	if err != nil {
		return nil, s.databaseError(ctx, err)
	}

	facet := &consolev1.AuditFacet{Target: target}

	for _, row := range rows {
		v := &structpb.Value{}
		if err := v.UnmarshalJSON(row.Value); err != nil {
			return nil, rpcError(codes.Internal, "invalid stored audit value")
		}

		facet.Values = append(facet.Values, &consolev1.AuditFacetValue{Value: v, Count: uint64(max(row.Count, 0))})

		if row.Total != nil {
			facet.Total = uint64(max(*row.Total, 0))
		}
	}

	return facet, nil
}

func (s *Service) databaseError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return rpcError(status.Code(status.FromContextError(ctx.Err()).Err()), ctx.Err().Error())
	}

	if _, ok := status.FromError(err); ok && !errors.Is(err, context.Canceled) {
		return err
	}

	s.Log().Warn("audit read failed", xlog.Err(err))

	return rpcError(codes.Unavailable, "audit database unavailable")
}
