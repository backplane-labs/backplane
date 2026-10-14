package obs_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/obs"
)

func randomHex(t *testing.T, n int) string {
	t.Helper()

	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	return hex.EncodeToString(b)
}

// sdkRecord is the SDK's OTLP fixture made unique: its event, trace, time
// and service, in the body's envelope and the indexed attributes alike.
func sdkRecord(t *testing.T, service, eventID, traceID string, when time.Time) []byte {
	t.Helper()

	raw, err := os.ReadFile("../../web/tests/fixtures/errors/exception-otlp.json")
	if err != nil {
		t.Fatal(err)
	}

	var request map[string]any
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}

	resource := request["resourceLogs"].([]any)[0].(map[string]any)
	resource["resource"] = map[string]any{"attributes": []any{attr("service.name", service)}}
	record := resource["scopeLogs"].([]any)[0].(map[string]any)["logRecords"].([]any)[0].(map[string]any)
	nanos := strconv.FormatInt(when.UnixNano(), 10)
	record["timeUnixNano"], record["observedTimeUnixNano"], record["traceId"] = nanos, nanos, traceID

	body := record["body"].(map[string]any)

	var envelope map[string]any
	if err := json.Unmarshal([]byte(body["stringValue"].(string)), &envelope); err != nil {
		t.Fatal(err)
	}

	envelope["eventId"], envelope["timestampUnixNano"] = eventID, nanos
	envelope["trace"].(map[string]any)["traceId"] = traceID

	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}

	body["stringValue"] = string(encoded)

	for _, a := range record["attributes"].([]any) {
		if a.(map[string]any)["key"] == "app.debug.event.id" {
			a.(map[string]any)["value"] = map[string]any{"stringValue": eventID}
		}
	}

	out, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func attr(key, value string) map[string]any {
	return map[string]any{"key": key, "value": map[string]any{"stringValue": value}}
}

// otelRecords: an exception by the conventions (not the SDK) and a plain
// log of the same trace.
func otelRecords(t *testing.T, service, traceID string, when time.Time) []byte {
	t.Helper()

	nanos := strconv.FormatInt(when.UnixNano(), 10)
	later := strconv.FormatInt(when.Add(time.Second).UnixNano(), 10)
	request := map[string]any{"resourceLogs": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{attr("service.name", service), attr("deployment.environment.name", "test")}},
		"scopeLogs": []any{map[string]any{"logRecords": []any{
			map[string]any{
				"timeUnixNano": later, "severityNumber": 17, "traceId": traceID, "spanId": "00f067aa0ba902b8",
				"body": map[string]any{"stringValue": "charge failed"},
				"attributes": []any{
					attr("exception.type", "PaymentError"), attr("exception.message", "card declined"),
					attr("exception.stacktrace", "PaymentError: card declined\n\tat charge (pay.go:12)"),
				},
			},
			map[string]any{
				"timeUnixNano": nanos, "severityNumber": 9, "traceId": traceID, "spanId": "00f067aa0ba902b9",
				"body": map[string]any{"stringValue": "checkout started"},
			},
		}}},
	}}}

	out, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func send(t *testing.T, collector string, body []byte) {
	t.Helper()

	resp, err := http.Post(collector+"/v1/logs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatal("collector", resp.Status)
	}
}

// Errors are read back from the real log store: an SDK record sent twice
// is one occurrence with its envelope; an exception by the conventions is
// the other origin; filters, facets, the histogram and related logs work.
func TestErrorsLive(t *testing.T) {
	t.Parallel()

	collector, logs := os.Getenv("BACKPLANE_TEST_OTLP"), os.Getenv("BACKPLANE_TEST_LOGS_URL")
	if collector == "" || logs == "" {
		t.Skip("set BACKPLANE_TEST_OTLP and BACKPLANE_TEST_LOGS_URL (make test-otlp's stack)")
	}

	svc, err := obs.New(obs.Config{LogsURL: logs})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(svc.Close)

	reader, err := svc.Errors()
	if err != nil {
		t.Fatal(err)
	}

	service, eventID, traceID := "errors-"+randomHex(t, 4), uuid.NewString(), randomHex(t, 16)
	when := time.Now().UTC().Truncate(time.Millisecond)
	record := sdkRecord(t, service+"-web", eventID, traceID, when)
	send(t, collector, record)
	send(t, collector, record) // the SDK's retry
	send(t, collector, otelRecords(t, service+"-api", traceID, when))

	filter := func(conditions ...*consolev1.ErrorCondition) *consolev1.ErrorFilter {
		return &consolev1.ErrorFilter{
			Start: timestamppb.New(when.Add(-time.Minute)), End: timestamppb.New(when.Add(time.Minute)),
			Conditions: append([]*consolev1.ErrorCondition{{
				Field: consolev1.ErrorField_ERROR_FIELD_SERVICE,
				Op:    consolev1.ErrorOperator_ERROR_OPERATOR_PREFIX, Values: []string{service},
			}}, conditions...),
		}
	}

	var found *consolev1.SearchErrorsResponse

	deadline := time.Now().Add(30 * time.Second)

	for {
		found, err = reader.SearchErrors(t.Context(), &consolev1.SearchErrorsRequest{Filter: filter()})
		if err == nil && len(found.GetOccurrences()) == 2 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("occurrences", found, err)
		}

		time.Sleep(time.Second)
	}

	api, web := found.GetOccurrences()[0], found.GetOccurrences()[1]
	if api.GetOrigin() != consolev1.ErrorOrigin_ERROR_ORIGIN_OTEL_LOG || api.GetType() != "PaymentError" || api.GetEnvironment() != "test" ||
		web.GetOrigin() != consolev1.ErrorOrigin_ERROR_ORIGIN_SDK || web.GetEventId() != eventID || web.GetTraceId() != traceID {
		t.Fatalf("occurrences %v", found.GetOccurrences())
	}

	sdkOnly, err := reader.SearchErrors(t.Context(), &consolev1.SearchErrorsRequest{Filter: filter(&consolev1.ErrorCondition{
		Field: consolev1.ErrorField_ERROR_FIELD_ORIGIN, Op: consolev1.ErrorOperator_ERROR_OPERATOR_IS, Values: []string{"sdk"},
	})})
	if err != nil || len(sdkOnly.GetOccurrences()) != 1 {
		t.Fatal("origin filter", sdkOnly, err)
	}

	byText, err := reader.SearchErrors(t.Context(), &consolev1.SearchErrorsRequest{Filter: &consolev1.ErrorFilter{
		Start: filter().GetStart(), End: filter().GetEnd(), Conditions: filter().GetConditions(), Text: "CARD DECLINED",
	}})
	if err != nil || len(byText.GetOccurrences()) != 1 {
		t.Fatal("text filter", byText, err)
	}

	detail, err := reader.GetError(t.Context(), &consolev1.GetErrorRequest{Ref: web.GetRef()})
	if err != nil || detail.GetEnvelopeStatus() != "available" || detail.GetEnvelope().GetFields()["eventId"].GetStringValue() != eventID ||
		detail.GetStacktrace() == "" || len(detail.GetWarnings()) != 0 {
		t.Fatal("sdk detail", detail, err)
	}

	otel, err := reader.GetError(t.Context(), &consolev1.GetErrorRequest{Ref: api.GetRef()})
	if err != nil || otel.GetEnvelopeStatus() != "" || otel.GetStacktrace() != "PaymentError: card declined\n\tat charge (pay.go:12)" {
		t.Fatal("otel detail", otel, err)
	}

	related, err := reader.RelatedLogs(t.Context(), &consolev1.RelatedLogsRequest{Ref: api.GetRef(), Relation: consolev1.ErrorRelation_ERROR_RELATION_SAME_TRACE})
	if err != nil || related.GetStatus() != "available" || len(related.GetLogs()) < 2 {
		t.Fatal("related", related, err)
	}

	facets, err := reader.ErrorFacets(t.Context(), &consolev1.ErrorFacetsRequest{Filter: filter(), Fields: []consolev1.ErrorField{
		consolev1.ErrorField_ERROR_FIELD_SERVICE, consolev1.ErrorField_ERROR_FIELD_TYPE,
	}})
	if err != nil || len(facets.GetFacets()) != 2 || len(facets.GetFacets()[0].GetValues()) != 2 || facets.GetFacets()[1].GetValues()[0].GetCount() != 1 {
		t.Fatal("facets", facets, err)
	}

	histogram, err := reader.ErrorHistogram(t.Context(), &consolev1.ErrorHistogramRequest{Filter: filter()})
	if err != nil || histogram.GetTotal() != 2 {
		t.Fatal("histogram", histogram, err)
	}
}
