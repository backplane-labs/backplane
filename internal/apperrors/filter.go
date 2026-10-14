package apperrors

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
)

// Bounds of a query.
const (
	defaultPage        = 50
	maxPage            = 200
	maxGroupOffset     = 2000
	maxRange           = 31 * 24 * time.Hour
	maxValues          = 32
	maxConditions      = 32
	maxValueBytes      = 1024
	histogramBuckets   = 120
	defaultFacetLimit  = 10
	maxFacetLimit      = 50
	defaultGroupWindow = 24 * time.Hour
)

func invalid(message string) error {
	return status.Error(codes.InvalidArgument, message) //nolint:wrapcheck // a status
}

// where is the checked filter as the queries take it: every listing's
// parameters but the page's. Field names match the generated parameter
// structs, which are filled from it by name.
type where struct {
	Start, End time.Time

	ServiceIn, ServiceNot, ServicePrefix               []string
	EnvironmentIn, EnvironmentNot                      []string
	EnvironmentExists                                  int32
	ReleaseIn, ReleaseNot, ReleasePrefix               []string
	ReleaseExists                                      int32
	TypeIn, TypeNot, TypeLike, TypeNotLike, TypePrefix []string
	MessageIn, MessageNot, MessageLike                 []string
	MessageNotLike, MessagePrefix                      []string
	TraceIn, TraceNot                                  []string
	TraceExists                                        int32
	RuntimeIn, RuntimeNot                              []string
	RuntimeExists                                      int32
	GroupKeyIn, GroupKeyNot                            []string
	GroupKeyExists                                     int32
	OriginIn, OriginNot                                []string
	FingerprintIn, FingerprintNot                      [][]byte
	Handled                                            int32
	IngestIn, IngestNot                                []string
	InstanceIn, InstanceNot                            []string
	Text                                               string
	TextInStack                                        bool
	// Groups only: the states listed.
	StateIn []string
}

// fill sets the fields of dst (a pointer to a generated Params struct)
// from w by name.
func (w where) fill(dst any) {
	src, out := reflect.ValueOf(w), reflect.ValueOf(dst).Elem()

	for i := range src.NumField() {
		f := src.Type().Field(i)
		if target := out.FieldByName(f.Name); target.IsValid() && target.Type() == f.Type {
			target.Set(src.Field(i))
		}
	}

	// Empty arrays, not NULL: cardinality(NULL) is NULL.
	for i := range out.NumField() {
		if f := out.Field(i); f.Kind() == reflect.Slice && f.IsNil() {
			f.Set(reflect.MakeSlice(f.Type(), 0, 0))
		}
	}
}

// checkFilter reads the filter into the queries' parameters; skip's own
// conditions are left out (a facet shows the values its own conditions
// would exclude). Without a filter: the last 24 hours.
func checkFilter(filter *consolev1.ErrorFilter, skip consolev1.ErrorField) (where, error) {
	var w where

	if filter == nil {
		w.End = time.Now()
		w.Start = w.End.Add(-defaultGroupWindow)

		return w, nil
	}

	if filter.GetStart() == nil || filter.GetEnd() == nil ||
		filter.GetStart().CheckValid() != nil || filter.GetEnd().CheckValid() != nil {
		return w, invalid("error filter needs a valid start and end")
	}

	w.Start, w.End = filter.GetStart().AsTime(), filter.GetEnd().AsTime()
	if !w.End.After(w.Start) || w.End.Sub(w.Start) > maxRange {
		return w, invalid("error range must be positive and at most 31 days")
	}

	if len(filter.GetConditions()) > maxConditions || len(filter.GetText()) > maxValueBytes {
		return w, invalid("error filter exceeds its limits")
	}

	w.Text = filter.GetText()

	for _, c := range filter.GetConditions() {
		if err := checkCondition(c); err != nil {
			return w, err
		}

		if c.GetField() == skip {
			continue
		}

		if err := w.apply(c); err != nil {
			return w, err
		}
	}

	return w, nil
}

func checkCondition(c *consolev1.ErrorCondition) error {
	if c.GetField() == consolev1.ErrorField_ERROR_FIELD_UNSPECIFIED {
		return invalid("error condition needs a field")
	}

	if len(c.GetValues()) > maxValues {
		return invalid("error condition has too many values")
	}

	for _, v := range c.GetValues() {
		if len(v) > maxValueBytes {
			return invalid("error condition value exceeds byte budget")
		}
	}

	exists := c.GetOp() == consolev1.ErrorOperator_ERROR_OPERATOR_EXISTS ||
		c.GetOp() == consolev1.ErrorOperator_ERROR_OPERATOR_NOT_EXISTS
	if c.GetOp() == consolev1.ErrorOperator_ERROR_OPERATOR_UNSPECIFIED || exists == (len(c.GetValues()) > 0) {
		return invalid("error condition needs an operator and values (exists: none)")
	}

	return nil
}

// textField is a field with every operator: the values to keep, drop,
// match, not match, prefix, and whether it must be set.
type textField struct {
	in, not, like, notLike, prefix *[]string
	exists                         *int32
}

// apply adds one condition to the parameters.
//
//nolint:cyclop // one case per field
func (w *where) apply(c *consolev1.ErrorCondition) error {
	var f textField

	switch c.GetField() {
	case consolev1.ErrorField_ERROR_FIELD_SERVICE:
		f = textField{in: &w.ServiceIn, not: &w.ServiceNot, prefix: &w.ServicePrefix}
	case consolev1.ErrorField_ERROR_FIELD_ENVIRONMENT:
		f = textField{in: &w.EnvironmentIn, not: &w.EnvironmentNot, exists: &w.EnvironmentExists}
	case consolev1.ErrorField_ERROR_FIELD_RELEASE:
		f = textField{in: &w.ReleaseIn, not: &w.ReleaseNot, prefix: &w.ReleasePrefix, exists: &w.ReleaseExists}
	case consolev1.ErrorField_ERROR_FIELD_TYPE:
		f = textField{in: &w.TypeIn, not: &w.TypeNot, like: &w.TypeLike, notLike: &w.TypeNotLike, prefix: &w.TypePrefix}
	case consolev1.ErrorField_ERROR_FIELD_MESSAGE:
		f = textField{
			in: &w.MessageIn, not: &w.MessageNot, like: &w.MessageLike, notLike: &w.MessageNotLike, prefix: &w.MessagePrefix,
		}
	case consolev1.ErrorField_ERROR_FIELD_TRACE_ID:
		f = textField{in: &w.TraceIn, not: &w.TraceNot, exists: &w.TraceExists}
	case consolev1.ErrorField_ERROR_FIELD_RUNTIME_ID:
		f = textField{in: &w.RuntimeIn, not: &w.RuntimeNot, exists: &w.RuntimeExists}
	case consolev1.ErrorField_ERROR_FIELD_GROUP_KEY:
		f = textField{in: &w.GroupKeyIn, not: &w.GroupKeyNot, exists: &w.GroupKeyExists}
	case consolev1.ErrorField_ERROR_FIELD_ORIGIN:
		f = textField{in: &w.OriginIn, not: &w.OriginNot}

		for _, v := range c.GetValues() {
			if v != OriginSDK && v != OriginOTelLog && v != "otel-log" {
				return invalid("origin is sdk or otel_log")
			}
		}
	case consolev1.ErrorField_ERROR_FIELD_INGEST:
		f = textField{in: &w.IngestIn, not: &w.IngestNot}
	case consolev1.ErrorField_ERROR_FIELD_INSTANCE:
		f = textField{in: &w.InstanceIn, not: &w.InstanceNot}
	case consolev1.ErrorField_ERROR_FIELD_FINGERPRINT:
		return w.fingerprints(c)
	case consolev1.ErrorField_ERROR_FIELD_HANDLED:
		return w.handled(c)
	default:
		return invalid("unknown error field")
	}

	return f.apply(c)
}

func (f textField) apply(c *consolev1.ErrorCondition) error {
	values := c.GetValues()

	var target *[]string

	switch c.GetOp() {
	case consolev1.ErrorOperator_ERROR_OPERATOR_IS:
		target = f.in
	case consolev1.ErrorOperator_ERROR_OPERATOR_IS_NOT:
		target = f.not
	case consolev1.ErrorOperator_ERROR_OPERATOR_CONTAINS:
		target = f.like
	case consolev1.ErrorOperator_ERROR_OPERATOR_NOT_CONTAINS:
		target = f.notLike
	case consolev1.ErrorOperator_ERROR_OPERATOR_PREFIX:
		target = f.prefix
	case consolev1.ErrorOperator_ERROR_OPERATOR_EXISTS, consolev1.ErrorOperator_ERROR_OPERATOR_NOT_EXISTS:
		if f.exists == nil {
			return invalid("the field is always set")
		}

		*f.exists = 1
		if c.GetOp() == consolev1.ErrorOperator_ERROR_OPERATOR_NOT_EXISTS {
			*f.exists = -1
		}

		return nil
	default:
		return invalid("unknown operator")
	}

	if target == nil {
		return invalid("the operator does not apply to the field")
	}

	for _, v := range values {
		if v == "otel-log" { // the log store's spelling of the origin
			v = OriginOTelLog
		}

		*target = append(*target, v)
	}

	return nil
}

func (w *where) fingerprints(c *consolev1.ErrorCondition) error {
	var target *[][]byte

	switch c.GetOp() {
	case consolev1.ErrorOperator_ERROR_OPERATOR_IS:
		target = &w.FingerprintIn
	case consolev1.ErrorOperator_ERROR_OPERATOR_IS_NOT:
		target = &w.FingerprintNot
	default:
		return invalid("fingerprint takes is or is not")
	}

	for _, v := range c.GetValues() {
		raw, ok := ParseFingerprint(v)
		if !ok {
			return invalid("invalid fingerprint")
		}

		*target = append(*target, raw)
	}

	return nil
}

// Values of a handled condition.
const (
	trueText  = "true"
	falseText = "false"
)

func (w *where) handled(c *consolev1.ErrorCondition) error {
	if len(c.GetValues()) != 1 || (c.GetValues()[0] != trueText && c.GetValues()[0] != falseText) {
		return invalid("handled takes one of true, false")
	}

	want := c.GetValues()[0] == trueText
	if c.GetOp() == consolev1.ErrorOperator_ERROR_OPERATOR_IS_NOT {
		want = !want
	} else if c.GetOp() != consolev1.ErrorOperator_ERROR_OPERATOR_IS {
		return invalid("handled takes is or is not")
	}

	w.Handled = -1
	if want {
		w.Handled = 1
	}

	return nil
}

// interval is a duration as the queries take it.
func interval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

// histogramStep is the bucket size for a range: at most histogramBuckets
// buckets.
func histogramStep(span time.Duration) time.Duration {
	for _, step := range []time.Duration{
		time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute,
		15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour,
	} {
		if span/step <= histogramBuckets {
			return step
		}
	}

	return time.Duration((int64(span)/histogramBuckets/int64(time.Second) + 1) * int64(time.Second))
}

// pageCursor continues a listing: the filter it is of and where it is.
type pageCursor struct {
	Version int    `json:"v"`
	Filter  string `json:"f"`
	// Occurrences: the last row's time and id.
	Time int64  `json:"t,omitempty"`
	ID   string `json:"i,omitempty"`
	// Groups: the offset.
	Offset int `json:"o,omitempty"`
}

func filterHash(filter proto.Message, extra string) string {
	encoded, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(filter)
	sum := sha256.Sum256(append(encoded, []byte(extra)...))

	return hex.EncodeToString(sum[:])
}

func encodeCursor(value any) string {
	encoded, _ := json.Marshal(value) //nolint:errchkjson // plain structs

	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCursor(raw string, value any) bool {
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)

	return err == nil && len(raw) <= 4096 && json.Unmarshal(data, value) == nil
}

// pageSize bounds a request's page size.
func pageSize(size uint32) (int64, error) {
	if size == 0 {
		return defaultPage, nil
	}

	if size > maxPage {
		return 0, invalid("error page exceeds maximum")
	}

	return int64(size), nil
}
