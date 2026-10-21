package config

import "time"

// Telemetry of the service beyond the standard OTEL_* exporters:
// BACKPLANE_TELEMETRY_*. Fixed at start.
type Telemetry struct {
	Metrics Metrics `json:"metrics"`
	Traces  Traces  `json:"traces"`
}

// Samplers of Traces.
const (
	// SamplerAlwaysOn records every span whatever the parent decided:
	// sampling is the collector's (tail sampling sees whole traces).
	SamplerAlwaysOn = "always_on"
	// SamplerAlwaysOff records nothing.
	SamplerAlwaysOff = "always_off"
	// SamplerTraceIDRatio records Ratio of traces by trace id.
	SamplerTraceIDRatio = "traceidratio"
	// SamplerParentBasedAlwaysOn follows the parent, records roots
	// (OpenTelemetry's default).
	SamplerParentBasedAlwaysOn = "parentbased_always_on"
	// SamplerParentBasedAlwaysOff follows the parent, drops roots.
	SamplerParentBasedAlwaysOff = "parentbased_always_off"
	// SamplerParentBasedTraceIDRatio follows the parent, records Ratio of
	// roots.
	SamplerParentBasedTraceIDRatio = "parentbased_traceidratio"
)

// Traces: which spans the service records. OTEL_TRACES_SAMPLER does not
// apply: the sampler is this section's.
type Traces struct {
	// always_on, always_off, traceidratio, parentbased_always_on,
	// parentbased_always_off or parentbased_traceidratio.
	Sampler string `json:"sampler" schemapb:"default=always_on"`
	// Share of traces the ratio samplers record, 0..1.
	Ratio float64 `json:"ratio" schemapb:"default=1;gte=0;lte=1"`
}

// Metrics: export timing and how histograms aggregate.
type Metrics struct {
	// How often metrics are exported; 0: OTEL_METRIC_EXPORT_INTERVAL, else 30s.
	Interval time.Duration `json:"interval" schemapb:"default=0s;gte=0"`
	// Bound of one export; 0: OTEL_METRIC_EXPORT_TIMEOUT, else 10s.
	Timeout time.Duration `json:"timeout" schemapb:"default=0s;gte=0"`
	// Histogram rules, first match wins, ahead of the built-in ones (seconds
	// and milliseconds buckets for histograms in s and ms). As an environment
	// variable: a JSON list, e.g.
	// BACKPLANE_TELEMETRY_METRICS_HISTOGRAMS='[{"instrument":"*","unit":"s","aggregation":"exponential"}]'.
	Histograms []Histogram `json:"histograms,omitempty"`
}

// Aggregations of a histogram rule.
const (
	// AggregationExplicit: fixed bucket boundaries (Boundaries).
	AggregationExplicit = "explicit"
	// AggregationExponential: base-2 exponential buckets that follow the
	// data (MaxSize, MaxScale): precise quantiles at any scale, more series
	// in storage (VictoriaMetrics keeps them as vmrange buckets).
	AggregationExponential = "exponential"
	// AggregationDrop: the histogram is not exported.
	AggregationDrop = "drop"
	// AggregationDefault: what the instrument itself asks for (its
	// boundaries advice, else OpenTelemetry's defaults); stops the rules.
	AggregationDefault = "default"
)

// Histogram is one rule: which histograms it matches and how they aggregate.
type Histogram struct {
	// Instrument name; * and ? are wildcards ("templates.*", "*").
	Instrument string `json:"instrument" schemapb:"default=*"`
	// Unit the histogram must have ("s", "ms", "By"); empty: any.
	Unit string `json:"unit,omitempty"`
	// explicit, exponential, drop or default.
	Aggregation string `json:"aggregation" schemapb:"default=explicit"`
	// explicit: bucket upper bounds, strictly increasing.
	Boundaries []float64 `json:"boundaries,omitempty"`
	// exponential: most buckets per sign (default 160); fewer means coarser
	// buckets and fewer series.
	MaxSize int32 `json:"max_size,omitempty" schemapb:"gte=0"`
	// exponential: finest scale, -10..20 (default 20).
	MaxScale int32 `json:"max_scale,omitempty" schemapb:"gte=-10;lte=20"`
	// Leave min and max out of the histogram.
	NoMinMax bool `json:"no_min_max,omitempty"`
}
