package telemetry

import (
	"errors"
	"fmt"
	"slices"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// SecondsBoundaries are the buckets of a duration in seconds: OpenTelemetry's
// semantic conventions for request durations, 5 ms to 10 s.
//
//nolint:gochecknoglobals // a constant table
var SecondsBoundaries = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

// MillisecondsBoundaries are SecondsBoundaries in milliseconds.
//
//nolint:gochecknoglobals // a constant table
var MillisecondsBoundaries = []float64{5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000}

// Builtin are the rules after the configured ones: a duration gets buckets
// of its unit instead of OpenTelemetry's defaults (0 to 10000, made for
// milliseconds, useless for seconds).
func Builtin() []config.Histogram {
	return []config.Histogram{
		{Instrument: "*", Unit: "s", Aggregation: config.AggregationExplicit, Boundaries: SecondsBoundaries},
		{Instrument: "*", Unit: "ms", Aggregation: config.AggregationExplicit, Boundaries: MillisecondsBoundaries},
	}
}

// Exponential histograms' defaults (OpenTelemetry's).
const (
	defaultMaxSize  = 160
	defaultMaxScale = 20
	minMaxScale     = -10
	minMaxSize      = 2
)

// ErrHistogram: a histogram rule is invalid.
var ErrHistogram = errors.New("telemetry: histogram rule")

// HistogramView is one view applying rules, then Builtin, to every
// histogram: the first rule matching its name (and unit) decides its
// aggregation. One view, not one per rule: OpenTelemetry exports a stream
// per matching view, so overlapping rules would duplicate the histogram.
func HistogramView(rules []config.Histogram) (sdkmetric.View, error) {
	all := append(slices.Clone(rules), Builtin()...)
	views := make([]sdkmetric.View, 0, len(all))

	for i, r := range all {
		stream, err := streamOf(r)
		if err != nil {
			return nil, fmt.Errorf("%w %d (%s): %w", ErrHistogram, i, r.Instrument, err)
		}

		views = append(views, sdkmetric.NewView(
			sdkmetric.Instrument{Name: r.Instrument, Unit: r.Unit, Kind: sdkmetric.InstrumentKindHistogram}, stream))
	}

	return func(inst sdkmetric.Instrument) (sdkmetric.Stream, bool) {
		for _, v := range views {
			if stream, ok := v(inst); ok {
				return stream, true
			}
		}

		return sdkmetric.Stream{}, false
	}, nil
}

// streamOf is the stream a rule gives a histogram it matches.
func streamOf(r config.Histogram) (sdkmetric.Stream, error) {
	if r.Instrument == "" {
		return sdkmetric.Stream{}, errors.New("instrument is required (* for every one)") //nolint:err113 // wrapped
	}

	switch r.Aggregation {
	case config.AggregationExplicit:
		if len(r.Boundaries) == 0 {
			return sdkmetric.Stream{}, errors.New("explicit needs boundaries") //nolint:err113 // wrapped
		}

		for i := 1; i < len(r.Boundaries); i++ {
			if r.Boundaries[i] <= r.Boundaries[i-1] {
				return sdkmetric.Stream{}, errors.New("boundaries must strictly increase") //nolint:err113 // wrapped
			}
		}

		return sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
			Boundaries: slices.Clone(r.Boundaries), NoMinMax: r.NoMinMax,
		}}, nil
	case config.AggregationExponential:
		size, scale := r.MaxSize, r.MaxScale
		if size == 0 {
			size = defaultMaxSize
		}

		if scale == 0 {
			scale = defaultMaxScale
		}

		if size < minMaxSize || scale < minMaxScale || scale > defaultMaxScale {
			//nolint:err113 // wrapped by HistogramView
			return sdkmetric.Stream{}, fmt.Errorf("exponential needs max_size >= %d and max_scale in [%d, %d]",
				minMaxSize, minMaxScale, defaultMaxScale)
		}

		return sdkmetric.Stream{Aggregation: sdkmetric.AggregationBase2ExponentialHistogram{
			MaxSize: size, MaxScale: scale, NoMinMax: r.NoMinMax,
		}}, nil
	case config.AggregationDrop:
		return sdkmetric.Stream{Aggregation: sdkmetric.AggregationDrop{}}, nil
	case config.AggregationDefault:
		return sdkmetric.Stream{}, nil
	default:
		//nolint:err113 // wrapped by HistogramView
		return sdkmetric.Stream{}, fmt.Errorf("aggregation %q: want explicit, exponential, drop or default", r.Aggregation)
	}
}
