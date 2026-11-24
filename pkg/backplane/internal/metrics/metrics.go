// Package metrics is the SDK's own instrumentation, on meter
// github.com/backplane-labs/backplane. Instruments are created once on the global
// provider, which delegates to the one telemetry installs, so they may be
// used before telemetry starts.
package metrics

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Meter name.
const Meter = "github.com/backplane-labs/backplane"

// Outcomes shared by the counters.
const (
	OK       = "ok"
	Error    = "error"
	Applied  = "applied"
	Rejected = "rejected"
)

type instruments struct {
	depReady    metric.Int64Gauge
	depProvide  metric.Int64Counter
	cfgUpdates  metric.Int64Counter
	cfgDegraded metric.Int64Gauge
	transport   metric.Int64Gauge
	panics      metric.Int64Counter
}

//nolint:gochecknoglobals // one instrument set per process
var get = sync.OnceValue(func() instruments {
	m := otel.Meter(Meter)

	// Instrument creation only fails on invalid names, fixed here.
	depReady, _ := m.Int64Gauge("backplane.dependency.ready", metric.WithDescription("1 while the dependency is provided"))
	depProvide, _ := m.Int64Counter("backplane.dependency.provide.attempts")
	cfgUpdates, _ := m.Int64Counter("backplane.config.updates")
	cfgDegraded, _ := m.Int64Gauge("backplane.config.degraded",
		metric.WithDescription("1 while the Consul layer is stale"))
	transport, _ := m.Int64Gauge("backplane.transport.connected")
	panics, _ := m.Int64Counter("backplane.panics")

	return instruments{
		depReady: depReady, depProvide: depProvide, cfgUpdates: cfgUpdates, cfgDegraded: cfgDegraded,
		transport: transport, panics: panics,
	}
})

func flag(b bool) int64 {
	if b {
		return 1
	}

	return 0
}

// DependencyReady records whether node's dependency is provided.
func DependencyReady(ctx context.Context, node string, ready bool) {
	get().depReady.Record(ctx, flag(ready), metric.WithAttributes(attribute.String("node", node)))
}

// DependencyProvide counts a provide attempt.
func DependencyProvide(ctx context.Context, node, outcome string) {
	get().depProvide.Add(ctx, 1,
		metric.WithAttributes(attribute.String("node", node), attribute.String("outcome", outcome)))
}

// ConfigUpdate counts an applied or rejected configuration update.
func ConfigUpdate(ctx context.Context, outcome string) {
	get().cfgUpdates.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// ConfigDegraded records whether the Consul layer is stale.
func ConfigDegraded(ctx context.Context, degraded bool) {
	get().cfgDegraded.Record(ctx, flag(degraded))
}

// TransportConnected records SDK transport connectivity.
func TransportConnected(ctx context.Context, transport string, connected bool) {
	get().transport.Record(ctx, flag(connected), metric.WithAttributes(attribute.String("transport", transport)))
}

// Panic counts a recovered panic.
func Panic(ctx context.Context, where string) {
	get().panics.Add(ctx, 1, metric.WithAttributes(attribute.String("where", where)))
}
