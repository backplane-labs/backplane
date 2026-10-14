package rules

import (
	"errors"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/backplane-labs/backplane/internal/bindings"
	"github.com/backplane-labs/backplane/internal/wire"
)

// ErrNoID: the event has no ce-id — its run could not be deduplicated.
var ErrNoID = errors.New("rules: event without ce-id")

// W3C trace context headers an event carries (§8).
const (
	headerTraceparent = "traceparent"
	headerTracestate  = "tracestate"
)

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

// TraceOf is the event's W3C trace context (traceparent, tracestate) as
// the run carries it; nil when the event has none.
func TraceOf(h nats.Header) map[string]string {
	var out map[string]string

	for _, k := range []string{headerTraceparent, headerTracestate} {
		if v := h.Get(k); v != "" {
			if out == nil {
				out = map[string]string{}
			}

			out[k] = v
		}
	}

	return out
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
