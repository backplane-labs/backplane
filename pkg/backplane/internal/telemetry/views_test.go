package telemetry_test

import (
	"errors"
	"testing"

	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/telemetry"
)

// aggregations records one value in each histogram and tells what every
// exported metric became: explicit:<bounds>, exponential, or missing.
func aggregations(t *testing.T, rules []config.Histogram, histograms map[string]string) map[string]string {
	t.Helper()

	view, err := telemetry.HistogramView(rules)
	if err != nil {
		t.Fatal(err)
	}

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithView(view))
	m := mp.Meter("test")

	for name, unit := range histograms {
		h, err := m.Float64Histogram(name, metric.WithUnit(unit))
		if err != nil {
			t.Fatal(err)
		}

		h.Record(t.Context(), 0.042)
	}

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}

	out := map[string]string{}

	for _, sm := range collected.ScopeMetrics {
		for _, exported := range sm.Metrics {
			if _, dup := out[exported.Name]; dup {
				t.Fatalf("%s exported twice", exported.Name)
			}

			switch data := exported.Data.(type) {
			case metricdata.Histogram[float64]:
				out[exported.Name] = "explicit:" + bounds(data.DataPoints[0].Bounds)
			case metricdata.ExponentialHistogram[float64]:
				out[exported.Name] = "exponential"
			default:
				out[exported.Name] = "other"
			}
		}
	}

	return out
}

func bounds(b []float64) string {
	if len(b) == 0 {
		return "none"
	}

	switch b[0] {
	case 0.005:
		return "seconds"
	case 5:
		return "milliseconds"
	case 0:
		return "otel-default"
	default:
		return "custom"
	}
}

func TestBuiltinBucketsFollowTheUnit(t *testing.T) {
	t.Parallel()

	got := aggregations(t, nil, map[string]string{"a.duration": "s", "b.latency": "ms", "c.size": "By"})

	want := map[string]string{"a.duration": "explicit:seconds", "b.latency": "explicit:milliseconds", "c.size": "explicit:otel-default"}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %s, want %s", name, got[name], w)
		}
	}
}

func TestRulesFirstMatchWins(t *testing.T) {
	t.Parallel()

	rules := []config.Histogram{
		{Instrument: "templates.render.duration", Aggregation: config.AggregationExplicit, Boundaries: []float64{1, 2, 3}},
		{Instrument: "templates.*", Aggregation: config.AggregationExponential},
		{Instrument: "noisy.*", Aggregation: config.AggregationDrop},
		{Instrument: "own.*", Aggregation: config.AggregationDefault},
		{Instrument: "*", Unit: "s", Aggregation: config.AggregationExponential, MaxSize: 40},
	}

	got := aggregations(t, rules, map[string]string{
		"templates.render.duration": "s", "templates.other": "s", "noisy.duration": "s",
		"own.duration": "s", "courier.delivery.duration": "s", "bytes.sent": "By",
	})

	want := map[string]string{
		"templates.render.duration": "explicit:custom", "templates.other": "exponential",
		"own.duration": "explicit:otel-default", "courier.delivery.duration": "exponential",
		"bytes.sent": "explicit:otel-default",
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %s, want %s", name, got[name], w)
		}
	}

	if _, kept := got["noisy.duration"]; kept {
		t.Error("a dropped histogram was exported")
	}
}

func TestInvalidRules(t *testing.T) {
	t.Parallel()

	for name, r := range map[string]config.Histogram{
		"no instrument":  {Aggregation: config.AggregationExponential},
		"no boundaries":  {Instrument: "*", Aggregation: config.AggregationExplicit},
		"not increasing": {Instrument: "*", Aggregation: config.AggregationExplicit, Boundaries: []float64{1, 1}},
		"scale too fine": {Instrument: "*", Aggregation: config.AggregationExponential, MaxScale: 21},
		"size too small": {Instrument: "*", Aggregation: config.AggregationExponential, MaxSize: 1},
		"unknown":        {Instrument: "*", Aggregation: "summary"},
	} {
		if _, err := telemetry.HistogramView([]config.Histogram{r}); !errors.Is(err, telemetry.ErrHistogram) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
