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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

func attr(key, value string) map[string]any {
	return map[string]any{"key": key, "value": map[string]any{"stringValue": value}}
}

// records: an exception and a plain log of the same trace, in different
// spans, from one SDK runtime.
func records(t *testing.T, service, traceID, runtimeID string, when time.Time) []byte {
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
					attr("exception.type", "PaymentError"), attr("exception.message", "card declined"), attr("app.debug.runtime.id", runtimeID),
				},
			},
			map[string]any{
				"timeUnixNano": nanos, "severityNumber": 9, "traceId": traceID, "spanId": "00f067aa0ba902b9",
				"body": map[string]any{"stringValue": "checkout started"}, "attributes": []any{attr("app.debug.runtime.id", runtimeID)},
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

// Logs related to an occurrence of the error store are read from the real
// log store by trace, span, SDK runtime and service window.
func TestRelatedLogsLive(t *testing.T) {
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

	reader := svc.Errors()
	if !reader.Available() {
		t.Fatal("a log store is configured")
	}

	service, traceID, runtimeID := "related-"+randomHex(t, 4), randomHex(t, 16), randomHex(t, 8)
	when := time.Now().UTC().Truncate(time.Millisecond)
	send(t, collector, records(t, service, traceID, runtimeID, when))

	occurrence := &consolev1.ErrorOccurrence{Service: service, TraceId: traceID, SpanId: "00f067aa0ba902b8", RuntimeId: runtimeID}
	read := func(relation consolev1.ErrorRelation) *consolev1.RelatedLogsResponse {
		t.Helper()

		response, err := reader.Logs(t.Context(), &consolev1.RelatedLogsRequest{Relation: relation}, occurrence, when)
		if err != nil {
			t.Fatal(err)
		}

		return response
	}

	deadline := time.Now().Add(30 * time.Second)

	for {
		if byTrace := read(consolev1.ErrorRelation_ERROR_RELATION_SAME_TRACE); byTrace.GetStatus() == "available" && len(byTrace.GetLogs()) == 2 {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("trace", byTrace)
		}

		time.Sleep(time.Second)
	}

	if bySpan := read(consolev1.ErrorRelation_ERROR_RELATION_SAME_SPAN); len(bySpan.GetLogs()) != 1 || bySpan.GetLogs()[0].GetFields()["_msg"] != "charge failed" {
		t.Fatal("span", bySpan)
	}

	if byRuntime := read(consolev1.ErrorRelation_ERROR_RELATION_SAME_RUNTIME); len(byRuntime.GetLogs()) != 2 {
		t.Fatal("runtime", byRuntime)
	}

	if window := read(consolev1.ErrorRelation_ERROR_RELATION_TIME_WINDOW); len(window.GetLogs()) != 2 || window.GetLogs()[0].GetFields()["service.name"] != service {
		t.Fatal("window", window)
	}

	absent, err := reader.Logs(t.Context(), &consolev1.RelatedLogsRequest{Relation: consolev1.ErrorRelation_ERROR_RELATION_SAME_RUNTIME},
		&consolev1.ErrorOccurrence{Service: service}, when)
	if err != nil || absent.GetStatus() != "not_found" || absent.GetReason() != "correlation_key_absent" {
		t.Fatal("no runtime", absent, err)
	}

	if _, err := reader.Logs(t.Context(), &consolev1.RelatedLogsRequest{}, occurrence, when); status.Code(err) != codes.InvalidArgument {
		t.Fatal("no relation", err)
	}
}
