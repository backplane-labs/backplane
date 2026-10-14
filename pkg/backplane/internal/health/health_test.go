package health_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/health"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

type group struct {
	ctx context.Context
}

func (g group) Go(fn func(context.Context) error) { go func() { _ = fn(g.ctx) }() }

func code(t *testing.T, h *health.Health, path string) int {
	t.Helper()

	rec := httptest.NewRecorder()
	h.HTTP().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))

	return rec.Code
}

func grpcStatus(t *testing.T, h *health.Health) hv1.HealthCheckResponse_ServingStatus {
	t.Helper()

	res, err := h.GRPC().Check(context.Background(), &hv1.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}

	return res.GetStatus()
}

func start(t *testing.T, h *health.Health) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var g node.Group = group{ctx}
	if err := h.Start(ctx, g); err != nil {
		t.Fatal(err)
	}
}

func TestReadyOnlyWhenServing(t *testing.T) {
	t.Parallel()

	h := health.New(testlog.Discard(), time.Hour, 0)
	start(t, h)
	time.Sleep(20 * time.Millisecond)

	if c := code(t, h, "/healthz/readiness"); c == http.StatusOK {
		t.Fatalf("ready before serving: %d", c)
	}

	if c := code(t, h, "/healthz/liveness"); c != http.StatusOK {
		t.Fatalf("liveness: %d", c)
	}

	h.Serving(context.Background(), true)

	if c := code(t, h, "/healthz/readiness"); c != http.StatusOK {
		t.Fatalf("ready after serving: %d", c)
	}

	if s := grpcStatus(t, h); s != hv1.HealthCheckResponse_SERVING {
		t.Fatalf("grpc: %v", s)
	}

	h.Serving(context.Background(), false)

	if s := grpcStatus(t, h); s == hv1.HealthCheckResponse_SERVING {
		t.Fatal("still serving after the gate closed")
	}
}

func TestAuthorProbeGatesReadiness(t *testing.T) {
	t.Parallel()

	h := health.New(testlog.Discard(), time.Hour, 0)
	db := probe.NewBool()
	h.Add(health.Ready, db)
	start(t, h)
	h.Serving(context.Background(), true)

	if c := code(t, h, "/healthz/readiness"); c == http.StatusOK {
		t.Fatal("ready with failing author probe")
	}

	db.Set(true)
	h.Serving(context.Background(), true)

	if c := code(t, h, "/healthz/readiness"); c != http.StatusOK {
		t.Fatalf("not ready with passing probe: %d", c)
	}
}

// One evaluation is bounded by the timeout, not the interval: a probe that
// hangs holds readiness down for the timeout only.
func TestCheckBoundedByTimeout(t *testing.T) {
	t.Parallel()

	h := health.New(testlog.Discard(), time.Hour, 50*time.Millisecond)
	if h.Interval() != time.Hour {
		t.Fatalf("interval %v", h.Interval())
	}

	h.Add(health.Ready, probe.Func(func(ctx context.Context) probe.Status {
		<-ctx.Done()

		return probe.StatusDown
	}))

	begin := time.Now()

	start(t, h)
	h.Serving(context.Background(), true)

	if took := time.Since(begin); took > 5*time.Second {
		t.Fatalf("evaluation took %v with a 50ms timeout", took)
	}

	if c := code(t, h, "/healthz/readiness"); c == http.StatusOK {
		t.Fatal("ready with a hanging probe")
	}
}

func TestNodeReasonReachesHTTP(t *testing.T) {
	t.Parallel()

	root := node.New("service", testlog.Discard(), nil)
	db := probe.NewBool()
	db.SetReason(false, "connection refused")
	root.Child("database", node.Dependency, false).Ready(db)

	h := health.New(testlog.Discard(), time.Hour, 0)
	h.Add(health.Ready, root.Readiness())
	start(t, h)
	h.Serving(t.Context(), true)

	rec := httptest.NewRecorder()
	h.HTTP().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz/readiness", http.NoBody))

	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "database: connection refused") {
		t.Fatalf("readiness: %d %s", rec.Code, rec.Body)
	}

	db.Set(true)
	h.Serving(t.Context(), true)

	rec = httptest.NewRecorder()
	h.HTTP().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz/readiness", http.NoBody))

	if rec.Code != http.StatusOK || rec.Body.String() != "Healthy" {
		t.Fatalf("recovery: %d %s", rec.Code, rec.Body)
	}
}
