// Package otlprecord reads OTLP log records as backplane stores them:
// attributes as JSON, bodies as text, times, bounded text. The audit and
// error ingests share it.
package otlprecord

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// ErrInvalidText: PostgreSQL text and jsonb cannot store NUL or invalid
// UTF-8; such a record is rejected before any transaction.
var ErrInvalidText = errors.New("record contains invalid PostgreSQL text")

// maxTimestamp bounds a record's time: later is treated as unset.
//
//nolint:gochecknoglobals // a constant time
var maxTimestamp = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

// StringAttr is the string value of key among attrs, "" without.
func StringAttr(attrs []*commonpb.KeyValue, key string) string {
	for _, kv := range attrs {
		if kv.GetKey() == key {
			return kv.GetValue().GetStringValue()
		}
	}

	return ""
}

// BoolAttr is the bool value of key among attrs; false, false without.
func BoolAttr(attrs []*commonpb.KeyValue, key string) (bool, bool) {
	for _, kv := range attrs {
		if kv.GetKey() == key {
			if b, ok := kv.GetValue().GetValue().(*commonpb.AnyValue_BoolValue); ok {
				return b.BoolValue, true
			}

			return false, false
		}
	}

	return false, false
}

// HasAttr reports whether key is among attrs.
func HasAttr(attrs []*commonpb.KeyValue, key string) bool {
	for _, kv := range attrs {
		if kv.GetKey() == key {
			return true
		}
	}

	return false
}

// AttributesJSON is attrs as a JSON object.
func AttributesJSON(attrs []*commonpb.KeyValue) (json.RawMessage, error) {
	out := make(map[string]any, len(attrs))
	for _, kv := range attrs {
		if !ValidText(kv.GetKey()) {
			return nil, ErrInvalidText
		}

		value, err := AnyJSON(kv.GetValue())
		if err != nil {
			return nil, err
		}

		out[kv.GetKey()] = value
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("attributes: %w", err)
	}

	return encoded, nil
}

// ValidText reports whether s can be stored as PostgreSQL text.
func ValidText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

// AnyJSON is an OTLP value as JSON: bytes as base64, a key-value list as
// an object.
func AnyJSON(v *commonpb.AnyValue) (any, error) {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		if !ValidText(x.StringValue) {
			return nil, ErrInvalidText
		}

		return x.StringValue, nil
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue, nil
	case *commonpb.AnyValue_IntValue:
		return x.IntValue, nil
	case *commonpb.AnyValue_DoubleValue:
		return jsonNumber(x.DoubleValue), nil
	case *commonpb.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(x.BytesValue), nil
	case *commonpb.AnyValue_ArrayValue:
		out := make([]any, 0, len(x.ArrayValue.GetValues()))
		for _, item := range x.ArrayValue.GetValues() {
			value, err := AnyJSON(item)
			if err != nil {
				return nil, err
			}

			out = append(out, value)
		}

		return out, nil
	case *commonpb.AnyValue_KvlistValue:
		return AttributesJSON(x.KvlistValue.GetValues())
	default:
		return nil, nil //nolint:nilnil // an unset OTLP AnyValue is JSON null
	}
}

// jsonNumber matches protobuf JSON's representation of non-finite doubles.
func jsonNumber(value float64) any {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	default:
		return value
	}
}

// BodyText is a record's body as text: a string as is, anything else as
// JSON.
func BodyText(body *commonpb.AnyValue) (string, error) {
	if body == nil {
		return "", nil
	}

	if s, ok := body.GetValue().(*commonpb.AnyValue_StringValue); ok {
		if !ValidText(s.StringValue) {
			return "", ErrInvalidText
		}

		return s.StringValue, nil
	}

	value, err := AnyJSON(body)
	if err != nil {
		return "", err
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("body: %w", err)
	}

	return string(encoded), nil
}

// RecordTime is the record's time, else its observed time, else now.
func RecordTime(record *logspb.LogRecord, now time.Time) time.Time {
	for _, nanos := range []uint64{record.GetTimeUnixNano(), record.GetObservedTimeUnixNano()} {
		if nanos > 0 && nanos <= uint64(maxTimestamp.UnixNano()) {
			return time.Unix(0, int64(nanos)).UTC() //nolint:gosec // bounded above
		}
	}

	return now.UTC()
}

// SeverityText is the record's severity text, else its number's name.
func SeverityText(record *logspb.LogRecord) string {
	if text := record.GetSeverityText(); text != "" {
		return text
	}

	if record.GetSeverityNumber() != logspb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED {
		return strings.TrimPrefix(record.GetSeverityNumber().String(), "SEVERITY_NUMBER_")
	}

	return ""
}

// CutBytes bounds s to limit bytes on a rune boundary.
func CutBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}

	return s[:limit]
}

// CutRunes bounds s to limit runes.
func CutRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}

	return string([]rune(s)[:limit])
}
