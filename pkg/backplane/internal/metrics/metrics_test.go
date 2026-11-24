package metrics_test

import (
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/metrics"
)

// reader collects what the package records into the global provider.
var reader = sdkmetric.NewManualReader()

func TestMain(m *testing.M) {
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	os.Exit(m.Run())
}

// points are the data points of name, by their attribute set rendered.
func points(t *testing.T, name string) map[string]int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}

	out := map[string]int64{}

	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}

			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range data.DataPoints {
					out[p.Attributes.Encoded(attribute.DefaultEncoder())] = p.Value
				}
			case metricdata.Gauge[int64]:
				for _, p := range data.DataPoints {
					out[p.Attributes.Encoded(attribute.DefaultEncoder())] = p.Value
				}
			case metricdata.Histogram[float64]:
				for _, p := range data.DataPoints {
					out[p.Attributes.Encoded(attribute.DefaultEncoder())] = int64(p.Count)
				}
			}
		}
	}

	return out
}

// Every recorded metric carries bounded labels only: names, outcomes and
// states, never an identifier.
func TestInstruments(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	metrics.ConfigUpdate(ctx, metrics.Applied)
	metrics.DependencyReady(ctx, "store", true)
	metrics.TransportConnected(ctx, "consul", true)

	tests := []struct {
		name, attrs string
		want        int64
	}{
		{name: "backplane.config.updates", attrs: "outcome=applied", want: 1},
		{name: "backplane.dependency.ready", attrs: "node=store", want: 1},
		{name: "backplane.transport.connected", attrs: "transport=consul", want: 1},
	}

	for _, tc := range tests {
		if got := points(t, tc.name)[tc.attrs]; got != tc.want {
			t.Errorf("%s{%s} = %d, want %d (%v)", tc.name, tc.attrs, got, tc.want, points(t, tc.name))
		}
	}
}
