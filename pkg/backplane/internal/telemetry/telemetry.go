// Package telemetry installs OpenTelemetry through xtrace's SDK bootstrap:
// resource from the build identity, exporters from the standard OTEL_*
// environment. A signal without an OTLP endpoint is not exported (xtrace
// decides per signal), so a service never retries a collector that does not
// exist.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/gopherex/xlog"
	xsdk "github.com/gopherex/xtrace/contrib/sdk"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// Identity stamped into the resource.
type Identity struct {
	Service, Version, Instance, Environment string
}

// Telemetry is a lifecycle component; start it first, it stops last.
type Telemetry struct {
	id       Identity
	metrics  config.Metrics
	traces   config.Traces
	log      *xlog.Logger
	shutdown xsdk.Shutdown

	mu       sync.Mutex
	setupErr error
}

// New creates the component; the section was validated (HistogramView,
// Sampler).
func New(id Identity, cfg config.Telemetry, log *xlog.Logger) *Telemetry {
	return &Telemetry{id: id, metrics: cfg.Metrics, traces: cfg.Traces, log: log}
}

// ErrSampler: an unknown sampler.
var ErrSampler = errors.New("unknown sampler")

// Sampler is the sampler of traces.
//
//nolint:ireturn // OpenTelemetry's sampler interface
func Sampler(t config.Traces) (sdktrace.Sampler, error) {
	switch t.Sampler {
	case config.SamplerAlwaysOn:
		return sdktrace.AlwaysSample(), nil
	case config.SamplerAlwaysOff:
		return sdktrace.NeverSample(), nil
	case config.SamplerTraceIDRatio:
		return sdktrace.TraceIDRatioBased(t.Ratio), nil
	case config.SamplerParentBasedAlwaysOn:
		return sdktrace.ParentBased(sdktrace.AlwaysSample()), nil
	case config.SamplerParentBasedAlwaysOff:
		return sdktrace.ParentBased(sdktrace.NeverSample()), nil
	case config.SamplerParentBasedTraceIDRatio:
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(t.Ratio)), nil
	}

	return nil, fmt.Errorf("%w %q", ErrSampler, t.Sampler)
}

// Start installs global providers and the propagator.
func (t *Telemetry) Start(ctx context.Context) error {
	histograms, err := HistogramView(t.metrics.Histograms)
	if err != nil {
		return err
	}

	sampler, err := Sampler(t.traces)
	if err != nil {
		return err
	}

	opts := []xsdk.Option{
		xsdk.WithService(t.id.Service),
		xsdk.WithVersion(t.id.Version),
		xsdk.WithInstanceID(t.id.Instance),
		xsdk.WithMetricReaderTiming(t.metrics.Interval, t.metrics.Timeout),
		xsdk.WithViews(histograms),
		xsdk.WithSampler(sampler),
	}
	if t.id.Environment != "" {
		opts = append(opts, xsdk.WithAttributes(attribute.String("deployment.environment.name", t.id.Environment)))
	}

	shutdown, err := xsdk.Setup(ctx, opts...)
	if err != nil {
		t.mu.Lock()
		t.setupErr = err
		t.mu.Unlock()

		t.log.Warn("telemetry setup failed, continuing without export", xlog.Err(err))

		return nil
	}

	t.shutdown = shutdown

	if err := xsdk.StartHostRuntime(); err != nil {
		t.log.Warn("runtime metrics", xlog.Err(err))
	}

	return nil
}

// Stop flushes and shuts the providers down.
func (t *Telemetry) Stop(ctx context.Context) error {
	if t.shutdown == nil {
		return nil
	}

	return t.shutdown(ctx)
}

// Status of OTLP export for the instance state: configured when an
// OTEL_EXPORTER_OTLP_*ENDPOINT is set; up unless the setup failed (export
// errors after setup are not tracked).
func (t *Telemetry) Status() (configured, up bool, err error) { //nolint:nonamedreturns // three flags read better named
	if !endpointSet() {
		return false, false, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	return true, t.setupErr == nil, t.setupErr
}

// endpointSet reports whether any OTLP endpoint is configured.
func endpointSet() bool {
	for _, kv := range os.Environ() {
		key, value, _ := strings.Cut(kv, "=")
		if value != "" && strings.HasPrefix(key, "OTEL_EXPORTER_OTLP_") && strings.HasSuffix(key, "ENDPOINT") {
			return true
		}
	}

	return false
}
