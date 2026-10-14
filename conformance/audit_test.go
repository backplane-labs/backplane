package conformance_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	consolev1 "github.com/backplane-labs/backplane/backplanepb/console/v1"
	"github.com/backplane-labs/backplane/internal/audit"
)

func (w *replicaWorld) auditDelivered(t *testing.T) {
	t.Helper()

	var history consolev1.SearchAuditResponse
	w.call(t, "/backplane.console.v1.AuditService/SearchAudit", &consolev1.SearchAuditRequest{PageSize: 500, Filter: platformOnly()}, &history)

	wanted := make(map[string]*consolev1.AuditRecord)
	actions := make(map[string]bool)

	for _, entry := range history.GetRecords() {
		wanted[entry.GetId()] = entry
		actions[entry.GetAction()] = true
	}

	for _, action := range []string{"session.create", "binding.save", "rule.save", "config.save", "hook.call"} {
		if !actions[action] {
			t.Fatalf("missing durable audit for %s", action)
		}
	}

	if history.GetNextPageCursor() != "" {
		t.Fatal("acceptance fixture unexpectedly exceeds one audit page")
	}

	m1Until(t, m2Wait, "durable audit delivered over OTLP from both replicas", func() (bool, string) {
		w.auditSink.mu.Lock()
		defer w.auditSink.mu.Unlock()

		for id := range w.auditSink.entries {
			event := w.auditSink.entries[id]

			entry, exists := wanted[id]
			if !exists {
				continue
			}

			if event.ID != entry.GetId() || event.Sequence != strconv.FormatUint(entry.GetSequence(), 10) || event.Action != entry.GetAction() || event.OperationID != entry.GetOperationId() || event.Outcome != entry.GetOutcome() {
				t.Fatalf("export differs from committed audit: %+v / %v", event, entry)
			}

			delete(wanted, id)
		}

		return len(wanted) == 0, fmt.Sprintf("%d audit records pending", len(wanted))
	})
}

// platformOnly is the audit feed's platform entries.
func platformOnly() *consolev1.AuditFilter {
	return &consolev1.AuditFilter{Conditions: []*consolev1.AuditCondition{{
		Target: &consolev1.AuditCondition_Field{Field: consolev1.AuditField_AUDIT_FIELD_SOURCE},
		Op:     consolev1.AuditOperator_AUDIT_OPERATOR_IS, Values: []*structpb.Value{structpb.NewStringValue("platform")},
	}}}
}

// auditCollector acknowledges OTLP only after decoding the durable entry.
type auditCollector struct {
	mu      sync.Mutex
	entries map[string]audit.Entry
	url     string
}

func newAuditCollector(t *testing.T) *auditCollector {
	t.Helper()

	sink := &auditCollector{entries: map[string]audit.Entry{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		var req collogspb.ExportLogsServiceRequest
		if err = proto.Unmarshal(body, &req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		sink.mu.Lock()
		defer sink.mu.Unlock()

		for _, resource := range req.GetResourceLogs() {
			for _, scope := range resource.GetScopeLogs() {
				for _, record := range scope.GetLogRecords() {
					var entry audit.Entry
					if err = json.Unmarshal([]byte(record.GetBody().GetStringValue()), &entry); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)

						return
					}

					sink.entries[entry.ID] = entry
				}
			}
		}

		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	sink.url = server.URL
	t.Cleanup(server.Close)

	return sink
}
