// Package apperrors keeps application errors durably: every exception
// record the deployment's Collector forwards to backplane's audit/errors
// listener is stored whole — the exception, the record's attributes, its
// resource and the browser SDK's envelope (state and history) — grouped by
// fingerprint, bounded by burst limits and a retention, and served to the
// console as ErrorService. Related logs and traces are read from the
// independent telemetry stores when the installation has them.
package apperrors

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"

	"github.com/backplane-labs/backplane/internal/otlprecord"
	"github.com/backplane-labs/backplane/internal/store/db"
)

// Attributes of an error record: the OTel exception conventions, the
// browser SDK's app.debug.* and backplane's own.
const (
	AttrType       = "exception.type"
	AttrMessage    = "exception.message"
	AttrStacktrace = "exception.stacktrace"
	AttrEventName  = "event.name"
	AttrKind       = "app.debug.kind"
	AttrEventID    = "app.debug.event.id"
	AttrRuntime    = "app.debug.runtime.id"
	AttrMechanism  = "app.debug.exception.mechanism"
	AttrHandled    = "app.debug.exception.handled"
	AttrGroupKey   = "app.debug.group.key"
	AttrIngest     = "backplane.ingest"
	AttrAudit      = "backplane.audit"
	attrService    = "service.name"
	attrEnv        = "deployment.environment.name"
	attrRelease    = "service.version"
	attrInstance   = "service.instance.id"
	kindException  = "exception"
)

// Origins and ingests of a record.
const (
	OriginSDK     = "sdk"
	OriginOTelLog = "otel_log"
	IngestProxy   = "proxy"
	IngestTrusted = "trusted"
)

// Limits bound what a stored record takes.
type Limits struct {
	// MaxRecord bounds the record as sent; a bigger one has its stack and
	// body cut and is marked truncated.
	MaxRecord int
	// MaxStack bounds the stack trace kept.
	MaxStack int
	// MaxField bounds the small text fields.
	MaxField int
}

// Sizes.
const (
	kib              = 1 << 10
	defaultMaxRecord = 512 * kib
	defaultMaxStack  = 64 * kib
	defaultMaxField  = 4 * kib
	dayWindow        = 24 * time.Hour
)

// DefaultLimits of a stored record.
//
//nolint:gochecknoglobals // immutable
var DefaultLimits = Limits{MaxRecord: defaultMaxRecord, MaxStack: defaultMaxStack, MaxField: defaultMaxField}

// fingerprintBytes is the size of a fingerprint and of a dedup key.
const fingerprintBytes = 16

// Record is one exception record as the store takes it: the occurrence's
// row and its group's touch.
type Record struct {
	Insert db.InsertErrorParams
	Touch  db.TouchErrorGroupParams
}

// IsException reports whether the record is an exception: by the OTel
// conventions (exception.* attributes, event.name=exception) or the
// browser SDK's app.debug.kind=exception.
func IsException(record *logspb.LogRecord) bool {
	attrs := record.GetAttributes()

	return otlprecord.HasAttr(attrs, AttrType) || otlprecord.HasAttr(attrs, AttrMessage) ||
		otlprecord.HasAttr(attrs, AttrStacktrace) || otlprecord.StringAttr(attrs, AttrEventName) == kindException ||
		record.GetEventName() == kindException || otlprecord.StringAttr(attrs, AttrKind) == kindException
}

// IsAudit reports whether the record is marked application audit.
func IsAudit(record *logspb.LogRecord) bool {
	marked, _ := otlprecord.BoolAttr(record.GetAttributes(), AttrAudit)

	return marked
}

// Records are the exception records of the batch as the store takes them,
// and how many were rejected (invalid text, no resource). Records marked
// audit are not errors.
func Records(req *collogspb.ExportLogsServiceRequest, now time.Time, limits Limits) ([]Record, int64) {
	var (
		out      []Record
		rejected int64
	)

	for _, logs := range req.GetResourceLogs() {
		resource := logs.GetResource()
		resourceJSON, resourceErr := otlprecord.AttributesJSON(resource.GetAttributes())

		for _, scopeLogs := range logs.GetScopeLogs() {
			scope := scopeLogs.GetScope().GetName()
			if v := scopeLogs.GetScope().GetVersion(); v != "" {
				scope += "@" + v
			}

			for _, record := range scopeLogs.GetLogRecords() {
				if IsAudit(record) || !IsException(record) {
					continue
				}

				if resourceErr != nil {
					rejected++

					continue
				}

				r, err := recordOf(record, resource, resourceJSON, scope, now, limits)
				if err != nil {
					rejected++

					continue
				}

				out = append(out, r)
			}
		}
	}

	return out, rejected
}

// recordOf is one exception record's row.
func recordOf(
	record *logspb.LogRecord, resource *resourcepb.Resource, resourceJSON json.RawMessage, scope string,
	now time.Time, limits Limits,
) (Record, error) {
	attrs := record.GetAttributes()

	body, err := otlprecord.BodyText(record.GetBody())
	if err != nil {
		return Record{}, fmt.Errorf("body: %w", err)
	}

	attributes, err := otlprecord.AttributesJSON(attrs)
	if err != nil {
		return Record{}, fmt.Errorf("attributes: %w", err)
	}

	if !otlprecord.ValidText(record.GetSeverityText()) || !otlprecord.ValidText(record.GetEventName()) ||
		!otlprecord.ValidText(scope) {
		return Record{}, otlprecord.ErrInvalidText
	}

	service := otlprecord.StringAttr(resource.GetAttributes(), attrService)
	if service == "" {
		service = "unknown_service"
	}

	origin, eventID, ingest := originOf(attrs)

	typ, message, stack := otlprecord.StringAttr(attrs, AttrType), otlprecord.StringAttr(attrs, AttrMessage),
		otlprecord.StringAttr(attrs, AttrStacktrace)

	bodyJSON, envelope := bodyJSON(body, origin)
	if origin == OriginSDK {
		typ, message, stack = fromEnvelope(envelope, typ, message, stack)
	}

	if typ == "" && message == "" {
		message = body
	}

	p := payload{stack: stack, body: bodyJSON, attributes: attributes}
	p.bound(proto.Size(record), body, limits)

	when := otlprecord.RecordTime(record, now)
	groupKey := otlprecord.StringAttr(attrs, AttrGroupKey)
	fingerprint := Fingerprint(service, groupKey, typ, message, stack)
	cut := func(s string) string { return otlprecord.CutBytes(s, limits.MaxField) }

	return Record{
		Insert: db.InsertErrorParams{
			ID: uuid.Must(uuid.NewV7()), Key: recordKey(record, resource, service, eventID), Fingerprint: fingerprint,
			Service: cut(service), Time: when, Environment: cut(otlprecord.StringAttr(resource.GetAttributes(), attrEnv)),
			Release:  cut(otlprecord.StringAttr(resource.GetAttributes(), attrRelease)),
			Instance: cut(otlprecord.StringAttr(resource.GetAttributes(), attrInstance)), Origin: origin, Ingest: ingest,
			EventID: eventID, Type: cut(typ), Message: cut(message), Stacktrace: p.stack,
			Severity: int32(record.GetSeverityNumber()), SeverityText: cut(otlprecord.SeverityText(record)),
			EventName: cut(eventName(record)), Mechanism: cut(otlprecord.StringAttr(attrs, AttrMechanism)),
			Handled: handledOf(attrs), GroupKey: cut(groupKey), TraceID: hex.EncodeToString(record.GetTraceId()),
			SpanID: hex.EncodeToString(record.GetSpanId()), RuntimeID: cut(otlprecord.StringAttr(attrs, AttrRuntime)),
			Scope: cut(scope), Body: p.body, Attributes: p.attributes, Resource: resourceJSON, Truncated: p.truncated,
		},
		Touch: db.TouchErrorGroupParams{
			Fingerprint: fingerprint, Service: cut(service), Type: cut(typ), Message: cut(message), Time: when,
			Minute: now.UTC().Truncate(time.Minute), Day: now.UTC().Truncate(dayWindow),
		},
	}, nil
}

// originOf is the record's origin, the SDK's event id when it is the
// SDK's, and how the platform took it in.
func originOf(attrs []*commonpb.KeyValue) (string, string, string) {
	origin, eventID, ingest := OriginOTelLog, otlprecord.StringAttr(attrs, AttrEventID), IngestTrusted

	if otlprecord.StringAttr(attrs, AttrKind) == kindException && validUUID(eventID) {
		origin = OriginSDK
	} else {
		eventID = ""
	}

	if otlprecord.StringAttr(attrs, AttrIngest) == IngestProxy {
		ingest = IngestProxy
	}

	return origin, eventID, ingest
}

// payload is what of a record can exceed the limits.
type payload struct {
	stack            string
	body, attributes json.RawMessage
	truncated        bool
}

// bound cuts what exceeds the limits: a record bigger than MaxRecord as
// sent (size) is marked, a long stack is cut, a big body or attributes
// replaced by a marker.
func (p *payload) bound(size int, bodyText string, limits Limits) {
	p.truncated = size > limits.MaxRecord

	if len(p.stack) > limits.MaxStack {
		p.stack, p.truncated = otlprecord.CutBytes(p.stack, limits.MaxStack), true
	}

	if len(p.body) > limits.MaxRecord {
		p.body, p.truncated = truncatedBody(bodyText, limits.MaxRecord), true
	}

	if len(p.attributes) > limits.MaxRecord {
		p.attributes, p.truncated = json.RawMessage(`{"truncated":true}`), true
	}
}

func eventName(record *logspb.LogRecord) string {
	if name := record.GetEventName(); name != "" {
		return name
	}

	return otlprecord.StringAttr(record.GetAttributes(), AttrEventName)
}

// handledOf reads app.debug.exception.handled: a bool, or "true"/"false".
func handledOf(attrs []*commonpb.KeyValue) *bool {
	if b, ok := otlprecord.BoolAttr(attrs, AttrHandled); ok {
		return &b
	}

	switch otlprecord.StringAttr(attrs, AttrHandled) {
	case "true":
		return proto.Bool(true)
	case "false":
		return proto.Bool(false)
	default:
		return nil
	}
}

// bodyJSON is the body as jsonb: the SDK's envelope when it is a JSON
// object, else {"text": body}; and the envelope decoded when it is one.
func bodyJSON(body, origin string) (json.RawMessage, map[string]any) {
	if origin == OriginSDK && strings.HasPrefix(strings.TrimSpace(body), "{") {
		var envelope map[string]any
		if json.Unmarshal([]byte(body), &envelope) == nil && json.Valid([]byte(body)) {
			return json.RawMessage(body), envelope
		}
	}

	encoded, _ := json.Marshal(map[string]string{"text": body}) //nolint:errchkjson // a string marshals

	return encoded, nil
}

func truncatedBody(body string, limit int) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{ //nolint:errchkjson // plain values
		"text": otlprecord.CutBytes(body, limit/2), "truncated": true, //nolint:mnd // half for the text, the rest JSON
	})

	return encoded
}

// fromEnvelope fills what the attributes lack from the envelope's
// exception.
func fromEnvelope(envelope map[string]any, typ, message, stack string) (string, string, string) {
	exception, _ := envelope["exception"].(map[string]any)

	text := func(key, have string) string {
		if have != "" {
			return have
		}

		s, _ := exception[key].(string)

		return s
	}

	return text("type", typ), text("message", message), text("stacktrace", stack)
}

// recordKey deduplicates the Collector's retries: the service and the
// SDK's event id, else the record and its resource as sent.
func recordKey(record *logspb.LogRecord, resource *resourcepb.Resource, service, eventID string) []byte {
	sum := sha256.New()

	if eventID != "" {
		sum.Write([]byte("event\x00" + service + "\x00" + eventID))
	} else {
		deterministic := proto.MarshalOptions{Deterministic: true}
		encoded, _ := deterministic.Marshal(resource)
		sum.Write(encoded)
		encoded, _ = deterministic.Marshal(record)
		sum.Write(encoded)
	}

	return sum.Sum(nil)[:fingerprintBytes]
}

var (
	uuidRE   = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	hexRE    = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)
	numberRE = regexp.MustCompile(`\d+`)
	quotedRE = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	spaceRE  = regexp.MustCompile(`\s+`)
	// frameRE matches one frame of a JavaScript stack: "at fn (file:1:2)",
	// "fn@file:1:2"; Go frames are the function lines before a file line.
	frameRE = regexp.MustCompile(`^\s*at (.+?)(?: \(.*\))?$|^(.*?)@.*$`)
)

// topFrames bounds how many frames the fingerprint takes.
const topFrames = 3

// Fingerprint identifies the same exception at the same place: the
// service and the SDK's group key when it sends one; else the service,
// the type, the message normalized (numbers, hex ids, uuids and quoted
// text replaced) and the top frames' functions.
func Fingerprint(service, groupKey, typ, message, stack string) []byte {
	sum := sha256.New()

	if groupKey != "" {
		sum.Write([]byte("group\x00" + service + "\x00" + groupKey))

		return sum.Sum(nil)[:fingerprintBytes]
	}

	sum.Write([]byte(service + "\x00" + typ + "\x00" + Normalize(message) + "\x00" + strings.Join(frames(stack), "\n")))

	return sum.Sum(nil)[:fingerprintBytes]
}

// Normalize replaces what varies between occurrences of one error.
func Normalize(message string) string {
	out := uuidRE.ReplaceAllString(message, "#")
	out = quotedRE.ReplaceAllString(out, "?")
	out = hexRE.ReplaceAllString(out, "#")
	out = numberRE.ReplaceAllString(out, "#")

	return strings.TrimSpace(spaceRE.ReplaceAllString(out, " "))
}

// frames are the top frames' functions of a stack, digits stripped.
func frames(stack string) []string {
	var out []string

	lines := strings.Split(stack, "\n")

	for i := 0; i < len(lines) && len(out) < topFrames; i++ {
		line := lines[i]

		if m := frameRE.FindStringSubmatch(line); m != nil && strings.TrimSpace(line) != "" {
			fn := m[1]
			if fn == "" {
				fn = m[2]
			}

			if fn = strings.TrimSpace(fn); fn != "" && !strings.ContainsAny(fn, ":/") {
				out = append(out, numberRE.ReplaceAllString(fn, ""))
			}

			continue
		}

		// A Go frame: a function line (not indented, not the goroutine
		// header) followed by an indented file line.
		if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") &&
			!strings.HasPrefix(line, "goroutine ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "\t") {
			fn := strings.TrimSpace(line)
			if at := strings.LastIndex(fn, "("); at > 0 {
				fn = fn[:at]
			}

			out = append(out, fn)
		}
	}

	return out
}

func validUUID(value string) bool {
	id, err := uuid.Parse(value)

	return err == nil && id.String() == value
}

// HexFingerprint is a fingerprint as the API shows it.
func HexFingerprint(fingerprint []byte) string { return hex.EncodeToString(fingerprint) }

// ParseFingerprint reads a fingerprint the API shows.
func ParseFingerprint(text string) ([]byte, bool) {
	raw, err := hex.DecodeString(text)
	if err != nil || len(raw) != fingerprintBytes {
		return nil, false
	}

	return raw, true
}
