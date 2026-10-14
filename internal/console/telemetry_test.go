package console_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/internal/console"
	"github.com/backplane-labs/backplane/internal/otlp"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

//nolint:paralleltest,tparallel // subtests share the session revoked after their requests
func TestSessionTelemetryWithIngestKeys(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)

		if r.URL.Path != "/v1/logs" || r.Header.Get(otlp.IngestHeader) != otlp.IngestProxy {
			t.Errorf("unexpected upstream request: %s %v", r.URL.Path, r.Header)
		}

		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credentials crossed into the Collector")
		}

		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(upstream.Close)

	h, err := otlp.New(otlp.Config{
		URL: upstream.URL, Keys: []config.Secret{"test-ingest-key-012345"},
		BodyBytes: 1024, Concurrent: 2, RatePerMinute: 6000, Burst: 100, MaxIPs: 16, IdleTTL: time.Minute, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(h.Close)
	e := newEnvOptions(t, nil, []console.Option{console.WithTelemetry(h), console.WithSessionTelemetry(h.SessionLogs())})

	cookie := e.mustLogin().String()
	for _, tc := range []struct {
		name, path, cookie, origin string
		want                       int
	}{
		{"anonymous", "/auth/telemetry/v1/logs", "", e.origin(), http.StatusUnauthorized},
		{"forged session", "/auth/telemetry/v1/logs", "bp_session=wrong", e.origin(), http.StatusUnauthorized},
		{"foreign origin", "/auth/telemetry/v1/logs", cookie, "https://other.example", http.StatusForbidden},
		{"public without key", "/telemetry/v1/logs", cookie, e.origin(), http.StatusUnauthorized},
		{"session logs", "/auth/telemetry/v1/logs", cookie, e.origin(), http.StatusAccepted},
		{"session metrics", "/auth/telemetry/v1/metrics", cookie, e.origin(), http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := e.request(http.MethodPost, tc.path, map[string]any{}, "Content-Type", "application/json", "Origin", tc.origin, "Cookie", tc.cookie)
			if res.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d", res.StatusCode, tc.want)
			}
		})
	}

	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}

	res := e.request(http.MethodPost, "/auth/logout", nil, "Origin", e.origin(), "Cookie", cookie)
	if res.StatusCode != http.StatusNoContent {
		t.Fatal(res.Status)
	}

	res = e.request(http.MethodPost, "/auth/telemetry/v1/logs", map[string]any{}, "Content-Type", "application/json", "Origin", e.origin(), "Cookie", cookie)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session admitted: %s", res.Status)
	}
}
