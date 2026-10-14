package deps_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

var errProbe = errors.New("probe failed")

// A provide attempt that hangs is cut by ProvideTimeout and retried.
func TestProvideTimeout(t *testing.T) {
	t.Parallel()

	s := newService(t)

	var attempts atomic.Int32

	p := deps.Func(func(ctx context.Context) (*Pool, error) {
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("no deadline")
		}

		if attempts.Add(1) < 3 {
			<-ctx.Done()

			return nil, ctx.Err()
		}

		return &Pool{id: 1}, nil
	})

	dep := deps.NewDependency(s.root, p, deps.ProvideTimeout(20*time.Millisecond), deps.Backoff(time.Millisecond, time.Millisecond))

	begin := time.Now()

	s.start(t)

	if !dep.Ready() || attempts.Load() != 3 {
		t.Fatalf("ready %v after %d attempts", dep.Ready(), attempts.Load())
	}

	if took := time.Since(begin); took > eventually {
		t.Fatalf("start took %v", took)
	}
}

// ProbeTimeout bounds a required dependency's probe inside readiness.
func TestProbeTimeout(t *testing.T) {
	t.Parallel()

	s := newService(t)

	p := deps.Func(func(context.Context) (*Pool, error) { return &Pool{}, nil },
		deps.WithProbe(func(ctx context.Context, _ *Pool) error { <-ctx.Done(); return ctx.Err() }))
	deps.NewDependency(s.root, p, deps.ProbeTimeout(20*time.Millisecond))

	s.start(t)

	begin := time.Now()

	if s.isReady(context.Background()) {
		t.Fatal("ready with a hanging probe")
	}

	if took := time.Since(begin); took > eventually {
		t.Fatalf("probe took %v", took)
	}
}

// With ProbeOptional a provided optional dependency is reported absent
// while its probe fails, and back when it passes; it never holds readiness.
func TestProbeOptional(t *testing.T) {
	t.Parallel()

	s := newService(t)
	s.svc.SetProbeInterval(5 * time.Millisecond)

	var healthy atomic.Bool

	healthy.Store(true)

	p := deps.Func(func(context.Context) (*Pool, error) { return &Pool{id: 7}, nil },
		deps.WithProbe(func(context.Context, *Pool) error {
			if !healthy.Load() {
				return errProbe
			}

			return nil
		}))

	opt := deps.NewOptional(s.root, p, deps.ProbeOptional(), deps.ProbeTimeout(time.Second), deps.Name("cache"))
	plain := deps.NewOptional(s.root, p, deps.Name("plain"))

	s.start(t)

	present := func(o deps.Optional[*Pool]) func() bool {
		return func() bool { v, ok := o.Get(); return ok && v.id == 7 }
	}

	waitFor(t, "provided", present(opt))
	waitFor(t, "plain provided", present(plain))

	healthy.Store(false)
	waitFor(t, "hidden while failing", func() bool { _, ok := opt.Get(); return !ok })

	if !errors.Is(opt.Err(), errProbe) {
		t.Fatalf("Err while failing: %v", opt.Err())
	}

	if _, ok := plain.Get(); !ok {
		t.Fatal("an optional without ProbeOptional is never probed")
	}

	if !s.isReady(t.Context()) {
		t.Fatal("a failing optional holds readiness")
	}

	healthy.Store(true)
	waitFor(t, "back when passing", present(opt))

	if opt.Err() != nil {
		t.Fatalf("Err when passing: %v", opt.Err())
	}
}

func TestTimeoutOptionsValidated(t *testing.T) {
	t.Parallel()

	expectPanic(t, "ProvideTimeout", func() { deps.ProvideTimeout(0) })
	expectPanic(t, "ProbeTimeout", func() { deps.ProbeTimeout(-time.Second) })
}

// Provide attempts and readiness are recorded as metrics.
//
//nolint:paralleltest // installs the global meter provider
func TestDependencyMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	s := newService(t)

	var calls atomic.Int32

	p := deps.Func(func(context.Context) (*Pool, error) {
		if calls.Add(1) == 1 {
			return nil, errBoom
		}

		return &Pool{}, nil
	})
	deps.NewDependency(s.root, p, deps.Name("metered"), deps.Backoff(time.Millisecond, time.Millisecond))

	s.start(t)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}

	outcomes := map[string]int64{}

	var ready int64 = -1

	for _, sm := range collected.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					if attr, _ := dp.Attributes.Value("node"); attr.AsString() == "metered" {
						outcome, _ := dp.Attributes.Value(attribute.Key("outcome"))
						outcomes[outcome.AsString()] += dp.Value
					}
				}
			case metricdata.Gauge[int64]:
				for _, dp := range data.DataPoints {
					if attr, _ := dp.Attributes.Value("node"); attr.AsString() == "metered" {
						ready = dp.Value
					}
				}
			}
		}
	}

	if outcomes["ok"] != 1 || outcomes["error"] != 1 || ready != 1 {
		t.Fatalf("outcomes %v, ready %d", outcomes, ready)
	}
}

// condition finds the node at path and reports its condition.
func condition(t *testing.T, s *service, path string) node.Condition {
	t.Helper()

	var (
		cond  node.Condition
		found bool
	)

	s.svc.Walk(func(n *node.Node) {
		if n.Path() == path {
			cond, found = n.Condition()
		}
	})

	if !found {
		t.Fatalf("no condition at %q", path)
	}

	return cond
}

// A dependency reports its condition for the instance state: not ready
// before it is provided, ready after, not ready while its probe fails.
func TestCondition(t *testing.T) {
	t.Parallel()

	s := newService(t)

	var healthy atomic.Bool

	healthy.Store(true)

	p := deps.Func(func(context.Context) (*Pool, error) { return &Pool{}, nil },
		deps.WithProbe(func(context.Context, *Pool) error {
			if !healthy.Load() {
				return errProbe
			}

			return nil
		}))
	deps.NewDependency(s.root, p, deps.Name("db"))

	if c := condition(t, s, "db"); c.Ready || !errors.Is(c.Err, deps.ErrNotReady) {
		t.Fatalf("before start: %+v", c)
	}

	s.start(t)

	if c := condition(t, s, "db"); !c.Ready || c.Err != nil {
		t.Fatalf("provided: %+v", c)
	}

	healthy.Store(false)
	s.isReady(t.Context())

	if c := condition(t, s, "db"); c.Ready || !errors.Is(c.Err, errProbe) {
		t.Fatalf("probe failing: %+v", c)
	}
}
