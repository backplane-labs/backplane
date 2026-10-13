package conformance_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/consul/api"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	"github.com/gopherex/backplane/examples/demo"
)

// TestPlatformModes checks the real console over authenticated WebSocket with
// optional backends absent, independently enabled, and configured but down.
//
//nolint:paralleltest // processes share the backplane service registry
func TestPlatformModes(t *testing.T) {
	consulAddr, dsn := os.Getenv("BACKPLANE_TEST_CONSUL"), os.Getenv("BACKPLANE_TEST_PG")
	if consulAddr == "" || dsn == "" {
		t.Skip("BACKPLANE_TEST_CONSUL and BACKPLANE_TEST_PG required")
	}

	bin := m1Build(t, t.TempDir(), "backplane", "../cmd/backplane", "0.0.0-modes")

	consul, err := api.NewClient(&api.Config{Address: consulAddr})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _, _ = consul.KV().Delete("backplane/services/backplane/manifests/0.0.0-modes", nil) })

	for _, tc := range []struct {
		name, nats, temporal string
		down                 bool
	}{
		{name: "minimal"},
		{name: "events", nats: os.Getenv("BACKPLANE_TEST_NATS")},
		{name: "workflows", temporal: os.Getenv("BACKPLANE_TEST_TEMPORAL")},
		{name: "full", nats: os.Getenv("BACKPLANE_TEST_NATS"), temporal: os.Getenv("BACKPLANE_TEST_TEMPORAL")},
		{name: "unavailable", nats: "nats://127.0.0.1:1", temporal: "127.0.0.1:1", down: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (tc.name == "events" || tc.name == "full") && tc.nats == "" {
				t.Skip("BACKPLANE_TEST_NATS required")
			}

			if (tc.name == "workflows" || tc.name == "full") && tc.temporal == "" {
				t.Skip("BACKPLANE_TEST_TEMPORAL required")
			}

			scratch := m1Database(t, dsn)
			token := m1Random(t)
			port := freePort(t)

			var healthy atomic.Bool
			healthy.Store(true)

			health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if !healthy.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			defer health.Close()

			env := []string{
				"BACKPLANE_CONSUL_ADDR=" + consulAddr, "BACKPLANE_PG_DSN=" + scratch, "BACKPLANE_VALKEY_ADDR=" + testValkey(t),
				"BACKPLANE_INSTANCE=modes-" + tc.name, "BACKPLANE_ADMIN_TOKEN=" + token, "BACKPLANE_ADVERTISE=" + advertise(t),
				"BACKPLANE_INTERNAL_PORT=" + freePort(t), "BACKPLANE_PUBLIC_PORT=" + freePort(t), "BACKPLANE_CONSOLE_LISTEN=:" + port,
				"BACKPLANE_AUDIT_LISTEN=:" + freePort(t), "BACKPLANE_CONSOLE_INSECURE_COOKIE=true",
				"BACKPLANE_NATS_URL=" + tc.nats, "BACKPLANE_TEMPORAL_ADDR=" + tc.temporal,
				"BACKPLANE_INFRASTRUCTURE_COLLECTOR_URL=" + health.URL, "BACKPLANE_OTLP_URL=" + health.URL,
				"BACKPLANE_SHUTDOWN_DRAIN=100ms", "BACKPLANE_CONSUL_CHECK_INTERVAL=1s",
			}
			proc := m1Start(t, "backplane-"+tc.name, bin, env...)
			m1Until(t, m1Wait, "console ready without optional backend readiness", func() (bool, string) { return m1Ready(proc, env) })

			cc, err := demo.Connect(t.Context(), "http://localhost:"+port, token)
			if err != nil {
				t.Fatal(err)
			}
			defer cc.Close()

			var caps consolev1.GetCapabilitiesResponse
			if err = m1Call(t.Context(), cc, consolev1.PlatformService_GetCapabilities_FullMethodName, &consolev1.GetCapabilitiesRequest{}, &caps); err != nil {
				t.Fatal(err)
			}

			enabled := map[string]bool{}
			for _, capability := range caps.GetCapabilities() {
				enabled[capability.GetName()] = capability.GetEnabled()
			}

			if enabled["events"] != (tc.nats != "") || enabled["schedules"] != (tc.temporal != "") || enabled["rules"] != (tc.nats != "" && tc.temporal != "") || !enabled["audit"] {
				t.Fatalf("capabilities: %v", &caps)
			}

			check := func(collectorOK bool) {
				t.Helper()
				m1Until(t, m1Wait, "cached infrastructure health", func() (bool, string) {
					var health consolev1.GetInfrastructureResponse
					if err := m1Call(t.Context(), cc, consolev1.PlatformService_GetInfrastructure_FullMethodName, &consolev1.GetInfrastructureRequest{}, &health); err != nil {
						return false, err.Error()
					}

					seen := map[string]bool{}
					for _, entry := range health.GetDependencies() {
						seen[entry.GetName()] = true

						expected := true
						if entry.GetName() == "collector" {
							expected = collectorOK
						}

						if tc.down && (entry.GetName() == "nats" || entry.GetName() == "temporal") {
							expected = false
						}

						if entry.GetCheckedAt() == nil || entry.GetOk() != expected {
							return false, entry.String()
						}
					}

					return seen["postgres"] && seen["consul"] && seen["valkey"] && seen["collector"] && seen["nats"] == (tc.nats != "") && seen["temporal"] == (tc.temporal != ""), fmt.Sprint(seen)
				})
			}
			check(true)
			healthy.Store(false)
			check(false)
			healthy.Store(true)
			check(true)

			var schedules consolev1.ListSchedulesResponse

			err = m1Call(t.Context(), cc, consolev1.ScheduleService_ListSchedules_FullMethodName, &consolev1.ListSchedulesRequest{}, &schedules)
			if tc.temporal == "" && m1Code(err) != codes.FailedPrecondition {
				t.Fatalf("disabled schedule status: %v", err)
			}

			var history consolev1.SearchAuditResponse
			if err = m1Call(t.Context(), cc, consolev1.AuditService_SearchAudit_FullMethodName, &consolev1.SearchAuditRequest{}, &history); err != nil || len(history.GetRecords()) == 0 {
				t.Fatalf("durable audit unavailable: %v %v", &history, err)
			}

			db, err := pgx.Connect(t.Context(), scratch)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close(t.Context())

			var queued int
			if err = db.QueryRow(t.Context(), "SELECT count(*) FROM backplane.audit_outbox").Scan(&queued); err != nil || queued != 0 {
				t.Fatalf("disabled export accumulated outbox: %d %v", queued, err)
			}
		})
	}
}
