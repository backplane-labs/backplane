package backplane_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

type pool struct{}

// hungCollector takes every OTLP export and answers none until the test
// ends; it counts the exports.
type hungCollector struct {
	exports atomic.Int32
	release chan struct{}
}

func (h *hungCollector) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	h.exports.Add(1)

	select {
	case <-r.Context().Done():
	case <-h.release:
	}
}

// TestStopWithHungCollector: a collector that never answers costs the
// stop at most Shutdown.Reserve, inside Shutdown.Timeout, and the stop
// succeeds: lost telemetry is no stop failure. Not parallel: t.Setenv.
func TestStopWithHungCollector(t *testing.T) {
	collector := &hungCollector{release: make(chan struct{})}
	srv := httptest.NewServer(collector)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(collector.release) }) // before Close: it waits for the handlers

	p := setup(t, "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	t.Setenv("BACKPLANE_SHUTDOWN_TIMEOUT", "4s")
	t.Setenv("BACKPLANE_SHUTDOWN_LISTENERS", "1s")
	t.Setenv("BACKPLANE_SHUTDOWN_RESERVE", "1s")

	svc, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
		return &testState{}, nil
	}, baseOptions("svc-hung-otlp")...)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := run(ctx, svc)

	eventually(t, "ready", ready(p))

	start := time.Now()

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("stop with a hung collector: %v", err)
	}

	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("stop took %v, past Shutdown.Timeout", elapsed)
	}

	if collector.exports.Load() == 0 {
		t.Fatal("nothing tried to export: the test proves nothing")
	}
}

// TestLivenessFollowsTerminalDependency: a dependency that fails its probe
// takes readiness down and keeps liveness up; once the probe reports
// deps.ErrTerminal liveness goes down too, so the orchestrator restarts the
// process. Not parallel: t.Setenv, the SDK block reads the process
// environment.
func TestLivenessFollowsTerminalDependency(t *testing.T) {
	p := setup(t, "")
	t.Setenv("BACKPLANE_HEALTH_INTERVAL", "50ms")
	t.Setenv("BACKPLANE_HEALTH_TIMEOUT", "50ms")

	var state atomic.Int32 // 0 healthy, 1 disconnected, 2 closed

	svc, err := backplane.Open(t.Context(), func(root backplane.Root[testConfig]) (*testState, error) {
		deps.NewDependency(root, deps.Func(func(context.Context) (*pool, error) { return &pool{}, nil },
			deps.WithProbe(func(context.Context, *pool) error {
				switch state.Load() {
				case 1:
					return errDown
				case 2:
					return fmt.Errorf("client closed: %w", deps.ErrTerminal)
				}

				return nil
			})), deps.Name("store"))

		return &testState{}, nil
	}, baseOptions("svc-live")...)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := run(ctx, svc)

	eventually(t, "ready", ready(p))

	status := func(kind string) func() int {
		return func() int { code, _ := get(p.platformURL() + "/healthz/" + kind); return code }
	}
	readiness, liveness := status("readiness"), status("liveness")

	if liveness() != http.StatusOK {
		t.Fatalf("liveness of a healthy service: %d", liveness())
	}

	state.Store(1)
	eventually(t, "not ready", func() bool { return readiness() == http.StatusServiceUnavailable })

	if liveness() != http.StatusOK {
		t.Fatal("a transient failure took liveness down")
	}

	state.Store(2)
	eventually(t, "not live", func() bool { return liveness() == http.StatusServiceUnavailable })

	cancel()

	if err := wait(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
}
