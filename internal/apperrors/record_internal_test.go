package apperrors

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func str(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func flag(key string, value bool) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: value}}}
}

func export(resource []*commonpb.KeyValue, records ...*logspb.LogRecord) *collogspb.ExportLogsServiceRequest {
	return &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource:  &resourcepb.Resource{Attributes: resource},
		ScopeLogs: []*logspb.ScopeLogs{{Scope: &commonpb.InstrumentationScope{Name: "test", Version: "1"}, LogRecords: records}},
	}}}
}

func exception(body string, attrs ...*commonpb.KeyValue) *logspb.LogRecord {
	return &logspb.LogRecord{
		TimeUnixNano: uint64(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC).UnixNano()), SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
		Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}}, Attributes: attrs,
		TraceId: bytes.Repeat([]byte{0xab}, 16), SpanId: bytes.Repeat([]byte{0xcd}, 8),
	}
}

// sdkFixture is the browser SDK's OTLP fixture.
func sdkFixture(t *testing.T) *collogspb.ExportLogsServiceRequest {
	t.Helper()

	raw, err := os.ReadFile("../../web/tests/fixtures/errors/exception-otlp.json")
	if err != nil {
		t.Fatal(err)
	}

	var req collogspb.ExportLogsServiceRequest
	if err := protojson.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}

	// OTLP/JSON writes ids as hex, which protojson reads as base64.
	record := req.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0]
	record.TraceId, _ = hex.DecodeString("4bf92f3577b34da6a3ce929d0e0e4736")
	record.SpanId, _ = hex.DecodeString("00f067aa0ba902b7")

	return &req
}

func TestIsException(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		record *logspb.LogRecord
		want   bool
	}{
		"conventions":  {exception("x", str(AttrType, "E")), true},
		"message only": {exception("x", str(AttrMessage, "boom")), true},
		"event name":   {exception("x", str(AttrEventName, "exception")), true},
		"event field":  {&logspb.LogRecord{EventName: "exception"}, true},
		"sdk kind":     {exception("x", str(AttrKind, "exception")), true},
		"plain log":    {exception("x", str("http.method", "GET")), false},
		"sdk other":    {exception("x", str(AttrKind, "breadcrumb")), false},
	} {
		if got := IsException(c.record); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}

	if !IsAudit(exception("x", flag(AttrAudit, true))) || IsAudit(exception("x", str(AttrAudit, "true"))) {
		t.Error("audit is the bool attribute")
	}
}

func TestRecordsSDK(t *testing.T) {
	t.Parallel()

	now := time.Now()
	records, rejected := Records(sdkFixture(t), now, DefaultLimits)

	if rejected != 0 || len(records) != 1 {
		t.Fatalf("records %d rejected %d", len(records), rejected)
	}

	r := records[0].Insert
	if r.Origin != OriginSDK || r.Ingest != IngestTrusted || r.Service != "fixture-browser" || r.EventID != "d45238c7-4ff0-4e5f-819b-f8d0f4c3c143" ||
		r.Type != "FixtureError" || r.Message != "Synthetic fixture exception" || !strings.HasPrefix(r.Stacktrace, "FixtureError:") ||
		r.Mechanism != "manual" || r.Handled == nil || !*r.Handled || r.RuntimeID != "b35e19ae-b217-4f30-ab89-0a9084b7f144" ||
		r.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || r.Scope != "app-debug.browser@0.1.0" || r.Truncated {
		t.Fatalf("record %+v", r)
	}

	if !bytes.HasPrefix(r.Body, []byte(`{"schema":"app-debug"`)) || !bytes.Contains(r.Attributes, []byte(`"app.debug.kind":"exception"`)) ||
		!bytes.Contains(r.Resource, []byte(`"service.name":"fixture-browser"`)) {
		t.Fatalf("payloads %s %s %s", r.Body, r.Attributes, r.Resource)
	}

	if records[0].Touch.Minute != now.UTC().Truncate(time.Minute) || records[0].Touch.Day != now.UTC().Truncate(24*time.Hour) ||
		!records[0].Touch.Time.Equal(r.Time) || !bytes.Equal(records[0].Touch.Fingerprint, r.Fingerprint) {
		t.Fatalf("touch %+v", records[0].Touch)
	}

	// The retry has the same key; another event another.
	again, _ := Records(sdkFixture(t), now.Add(time.Minute), DefaultLimits)
	if !bytes.Equal(again[0].Insert.Key, r.Key) || again[0].Insert.ID == r.ID {
		t.Fatal("the retry keeps its key and gets its own id")
	}
}

func TestRecordsOTel(t *testing.T) {
	t.Parallel()

	resource := []*commonpb.KeyValue{str("service.name", "api"), str("deployment.environment.name", "prod"), str("service.version", "1.2.3"), str("service.instance.id", "pod-1")}
	req := export(resource,
		exception("charge failed", str(AttrType, "PaymentError"), str(AttrMessage, "card declined"), str(AttrStacktrace, "PaymentError: card declined\n\tat charge (pay.go:12)"),
			str(AttrIngest, IngestProxy), str(AttrHandled, "false")),
		exception("only a body", str(AttrEventName, "exception")),
		exception("audit", str(AttrType, "E"), flag(AttrAudit, true)),
		exception("plain", str("http.method", "GET")),
		exception("bad\x00text", str(AttrType, "E")),
	)

	records, rejected := Records(req, time.Now(), DefaultLimits)
	if rejected != 1 || len(records) != 2 {
		t.Fatalf("records %d rejected %d", len(records), rejected)
	}

	r := records[0].Insert
	if r.Origin != OriginOTelLog || r.Ingest != IngestProxy || r.Environment != "prod" || r.Release != "1.2.3" || r.Instance != "pod-1" ||
		r.Type != "PaymentError" || r.Message != "card declined" || r.Handled == nil || *r.Handled || r.EventID != "" ||
		r.TraceID != "abababababababababababababababab" || r.SpanID != "cdcdcdcdcdcdcdcd" || r.SeverityText != "ERROR" ||
		string(r.Body) != `{"text":"charge failed"}` {
		t.Fatalf("record %+v", r)
	}

	if body := records[1].Insert; body.Type != "" || body.Message != "only a body" || body.EventName != "exception" {
		t.Fatalf("body-only record %+v", body)
	}

	// Without a resource the service is unknown; an invalid resource rejects.
	none, _ := Records(export(nil, exception("x", str(AttrType, "E"))), time.Now(), DefaultLimits)
	if none[0].Insert.Service != "unknown_service" {
		t.Fatal(none[0].Insert.Service)
	}

	_, bad := Records(export([]*commonpb.KeyValue{str("service.name", "a\x00b")}, exception("x", str(AttrType, "E"))), time.Now(), DefaultLimits)
	if bad != 1 {
		t.Fatal("invalid resource", bad)
	}
}

func TestRecordsTruncate(t *testing.T) {
	t.Parallel()

	limits := Limits{MaxRecord: 1 << 10, MaxStack: 64, MaxField: 8}
	stack := strings.Repeat("at f (x.js:1:1)\n", 100)
	req := export([]*commonpb.KeyValue{str("service.name", "very-long-service-name")},
		exception(strings.Repeat("b", 2048), str(AttrType, "SomeLongErrorName"), str(AttrStacktrace, stack)))

	records, _ := Records(req, time.Now(), limits)
	r := records[0].Insert

	if !r.Truncated || len(r.Stacktrace) != 64 || r.Service != "very-lon" || r.Type != "SomeLong" || len(r.Body) > 1<<10 ||
		!bytes.Contains(r.Body, []byte(`"truncated":true`)) {
		t.Fatalf("truncated %+v", r)
	}

	// A multi-byte boundary is kept whole.
	got, _ := Records(export([]*commonpb.KeyValue{str("service.name", "привет-мир")}, exception("x", str(AttrType, "E"))), time.Now(), limits)

	if got[0].Insert.Service != "прив" {
		t.Fatal(got[0].Insert.Service)
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()

	stack := func(id string) string {
		return "TypeError: Cannot read properties of undefined (reading '" + id + "')\n    at render (app.js:10:5)\n    at commit (react.js:200:1)\n    at flush (react.js:300:1)\n    at tick" + id + " (loop.js:1:1)"
	}

	a := Fingerprint("web", "", "TypeError", "Cannot read properties of undefined (reading 'user-1a2b3c4d')", stack("1"))
	b := Fingerprint("web", "", "TypeError", "Cannot read properties of undefined (reading 'user-99ff00aa')", stack("2"))
	c := Fingerprint("web", "", "TypeError", "Cannot read properties of undefined (reading 'name')", "TypeError: x\n    at other (app.js:10:5)")
	d := Fingerprint("api", "", "TypeError", "Cannot read properties of undefined (reading 'user-1a2b3c4d')", stack("1"))

	if !bytes.Equal(a, b) || bytes.Equal(a, c) || bytes.Equal(a, d) || len(a) != fingerprintBytes {
		t.Fatal("ids and the frames past the top ones do not matter; the top frames and the service do")
	}

	// The SDK's group key wins over everything else.
	k1 := Fingerprint("web", "checkout", "TypeError", "one", stack("1"))
	k2 := Fingerprint("web", "checkout", "RangeError", "two", "")

	if !bytes.Equal(k1, k2) || bytes.Equal(k1, Fingerprint("api", "checkout", "TypeError", "one", stack("1"))) {
		t.Fatal("group key")
	}

	// Go frames: the function lines before the file lines.
	goStack := "goroutine 1 [running]:\nmain.charge(0xc000010000, 0x3)\n\t/src/pay.go:12 +0x1a\nmain.main()\n\t/src/main.go:8 +0x2b\n"
	if got := frames(goStack); len(got) != 2 || got[0] != "main.charge" || got[1] != "main.main" {
		t.Fatal(got)
	}

	if got := frames(stack("7")); len(got) != 3 || got[0] != "render" || got[2] != "flush" {
		t.Fatal(got)
	}

	if got := frames("Error: x\n    at fn@https://cdn/app.js:1:2\nfn2@https://cdn/app.js:3:4"); len(got) != 1 || got[0] != "fn" {
		t.Fatal(got)
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"user 42 not found":                            "user # not found",
		"id 6f1a2b3c-4d5e-6f70-8192-a3b4c5d6e7f8 gone": "id # gone",
		`key "abc" missing`:                            "key ? missing",
		"deadbeefcafe expired   now":                   "# expired now",
		"  spaced   out  ":                             "spaced out",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}

	if _, ok := ParseFingerprint(HexFingerprint(bytes.Repeat([]byte{1}, fingerprintBytes))); !ok {
		t.Fatal("round trip")
	}

	if _, ok := ParseFingerprint("abcd"); ok {
		t.Fatal("short")
	}
}
