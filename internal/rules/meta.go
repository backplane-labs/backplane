package rules

import (
	"context"
	"errors"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/wire"
)

// ErrNoID: the event has no ce-id — its run could not be deduplicated.
var ErrNoID = errors.New("rules: event without ce-id")

// ParseMeta reads the CloudEvents attributes of an event from its NATS
// headers (binary mode, ce-*). ce-id is required: the run's workflow id is
// made of it. A ce-time that is not RFC 3339 reads as no time (meta.time
// is empty then), as a reactor's Delivery does.
func ParseMeta(h nats.Header) (bindings.Meta, error) {
	m := bindings.Meta{
		ID: h.Get(wire.HeaderID), Source: h.Get(wire.HeaderSource), Subject: h.Get(wire.HeaderSubject),
		Type: h.Get(wire.HeaderType),
	}

	if m.ID == "" {
		return m, ErrNoID
	}

	if s := h.Get(wire.HeaderTime); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			m.Time = t
		}
	}

	return m, nil
}

// instrumentation is the scope of the rule engine's spans.
const instrumentation = "github.com/backplane-labs/backplane/internal/rules"

// TraceOf is the W3C trace context (traceparent, tracestate) of ctx's span
// as the run carries it; nil without one.
func TraceOf(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	if len(carrier) == 0 {
		return nil
	}

	return carrier
}

// consumeOptions start the span of handling one event: a delivery is an
// asynchronous continuation of its publish, so the span starts a new trace
// linked to the event's span (OpenTelemetry messaging), not a child of it.
func consumeOptions(work context.Context, h nats.Header) []trace.SpanStartOption {
	origin := trace.SpanContextFromContext(otel.GetTextMapPropagator().Extract(work, carrier(h)))
	opts := []trace.SpanStartOption{trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindConsumer)}

	if origin.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: origin}))
	}

	return opts
}

// carrier adapts NATS headers to the OpenTelemetry propagator.
type carrier nats.Header

func (c carrier) Get(key string) string { return nats.Header(c).Get(key) }

func (c carrier) Set(key, value string) { nats.Header(c).Set(key, value) }

func (c carrier) Keys() []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}

	return out
}
