// Package health is the single source of a service's health: probes are
// registered by kind, their composite status is cached in xprobe states, and
// grpc.health.v1, the HTTP probe endpoints and the Consul check all read the
// same states.
//
// Each kind is driven by an xprobe runner, which serializes evaluation: a
// periodic tick and a Serving flip never interleave, so a slow check started
// before the gate opened cannot overwrite the fresher result.
package health

import (
	"context"
	"net/http"
	"sync"
	"time"

	hv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/gopherex/xlog"
	"github.com/gopherex/xprobe/pkg/probe"
	"github.com/gopherex/xprobe/pkg/reporter"
	"github.com/gopherex/xprobe/pkg/runner"
	"github.com/gopherex/xprobe/pkg/state"
	grpcprobe "github.com/gopherex/xprobe/pkg/transport/grpc"
	httpprobe "github.com/gopherex/xprobe/pkg/transport/http"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/node"
)

// Kind of probe.
type Kind int

const (
	Live Kind = iota
	Ready
	Startup
)

const kinds = 3

func (k Kind) String() string { return [...]string{"liveness", "readiness", "startup"}[k] }

// Health owns probe states. The readiness state is the registry's "" entry,
// which grpc.health.v1 reports and Consul checks.
type Health struct {
	log      *xlog.Logger
	interval time.Duration
	timeout  time.Duration
	reg      *state.Registry
	states   [kinds]*state.State
	serving  *probe.Bool

	mu      sync.Mutex
	probes  [kinds][]probe.Probe
	runners [kinds]*runner.Runner
}

// New creates the health component: every kind is evaluated each interval,
// one evaluation bounded by timeout (at most interval; 0 means interval).
func New(log *xlog.Logger, interval, timeout time.Duration) *Health {
	if timeout <= 0 || timeout > interval {
		timeout = interval
	}

	reg := state.NewRegistry()
	health := &Health{log: log, interval: interval, timeout: timeout, reg: reg, serving: probe.NewBool()}
	health.serving.SetReason(false, "service has not started serving")
	health.states = [kinds]*state.State{state.New(), reg.Get(""), state.New()}

	for _, st := range health.states {
		st.Set(probe.StatusDown)
	}

	return health
}

// Add registers a probe of the given kind; call before Start.
func (h *Health) Add(k Kind, p probe.Probe) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.probes[k] = append(h.probes[k], p)
}

// Serving flips the SDK's own gate and re-evaluates readiness and startup
// immediately.
func (h *Health) Serving(ctx context.Context, on bool) {
	h.serving.SetReason(on, "service is not serving")
	h.check(ctx, Ready)
	h.check(ctx, Startup)
}

// Interval between evaluations.
func (h *Health) Interval() time.Duration { return h.interval }

// GRPC is the grpc.health.v1 server over the cached readiness.
func (h *Health) GRPC() hv1.HealthServer { return grpcprobe.New(h.reg) }

// HTTP serves the cached states at the xprobe default paths.
func (h *Health) HTTP() http.Handler {
	return httpprobe.Mux(
		httpprobe.NewHTTPProbe(httpprobe.DefaultLivenessPath, httpprobe.CachedHandler(h.states[Live])),
		httpprobe.NewHTTPProbe(httpprobe.DefaultReadinessPath, httpprobe.CachedHandler(h.states[Ready])),
		httpprobe.NewHTTPProbe(httpprobe.DefaultStartupPath, httpprobe.CachedHandler(h.states[Startup])),
	)
}

// Start builds the composites and re-evaluates each kind every interval; a
// check may take up to the timeout.
func (h *Health) Start(ctx context.Context, g node.Group) error {
	h.mu.Lock()
	combined := [kinds]probe.Probe{
		Live:    probe.All(h.probes[Live]...),
		Ready:   probe.All(append([]probe.Probe{h.serving}, h.probes[Ready]...)...),
		Startup: probe.All(append([]probe.Probe{h.serving}, h.probes[Startup]...)...),
	}

	for k := range Kind(kinds) {
		h.runners[k] = runner.New(combined[k], h.states[k],
			runner.WithName(k.String()),
			runner.WithInterval(h.interval),
			runner.WithTimeout(h.timeout),
			runner.WithReporter(reporter.Func(h.report)))
	}

	runners := h.runners
	h.mu.Unlock()

	for _, r := range runners {
		r.Check(ctx)
		g.Go(func(ctx context.Context) error {
			r.Run(ctx)

			return nil
		})
	}

	return nil
}

// check evaluates kind k now; a no-op before Start.
func (h *Health) check(ctx context.Context, k Kind) {
	h.mu.Lock()
	r := h.runners[k]
	h.mu.Unlock()

	if r != nil {
		r.Check(ctx)
	}
}

func (h *Health) report(_ context.Context, ev reporter.Event) {
	h.log.Info("health",
		xlog.String("probe", ev.Name), xlog.String("from", ev.Prev.String()), xlog.String("to", ev.Cur.String()),
		xlog.String("reason", ev.Reason))
}
