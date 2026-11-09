package backplane_test

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

type pool struct{}

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
