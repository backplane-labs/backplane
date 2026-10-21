package telemetry_test

import (
	"errors"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/telemetry"
)

// always_on records a span whose parent was not sampled; the parent-based
// samplers follow the parent.
func TestSampler(t *testing.T) {
	t.Parallel()

	parent := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, Remote: true, // not sampled
	}))

	for _, tc := range []struct {
		sampler string
		ratio   float64
		record  bool
	}{
		{config.SamplerAlwaysOn, 1, true},
		{config.SamplerAlwaysOff, 1, false},
		{config.SamplerTraceIDRatio, 1, true},
		{config.SamplerParentBasedAlwaysOn, 1, false},
		{config.SamplerParentBasedTraceIDRatio, 1, false},
	} {
		s, err := telemetry.Sampler(config.Traces{Sampler: tc.sampler, Ratio: tc.ratio})
		if err != nil {
			t.Fatal(err)
		}

		_, span := sdktrace.NewTracerProvider(sdktrace.WithSampler(s)).Tracer("t").Start(parent, "op")
		if span.IsRecording() != tc.record {
			t.Fatalf("%s: recording %v", tc.sampler, span.IsRecording())
		}
	}

	if _, err := telemetry.Sampler(config.Traces{Sampler: "sometimes"}); !errors.Is(err, telemetry.ErrSampler) {
		t.Fatalf("unknown: %v", err)
	}
}
