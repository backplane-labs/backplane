// Package tracehttp traces outbound HTTP requests without recording what a
// URL may hide: every request is a client span of the caller's trace whose
// attributes are the method, the server's host and port and the response
// status, never the path or the query (a device token, a webhook secret, a
// signed link). The trace context stays local: no traceparent, tracestate or
// baggage goes out, since the destination is outside the installation (a
// user's URL, a push service). A destination that takes part in the trace
// gets it with WithPropagator. OpenTelemetry's otelhttp records the full URL
// and cannot be told not to.
package tracehttp

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// instrumentation is the scope of the client spans.
const instrumentation = "github.com/backplane-labs/backplane/pkg/backplane/tracehttp"

// statusServerError is where a response becomes a span error (5xx).
const statusServerError = 500

// Transport traces the requests of base.
type Transport struct {
	base       http.RoundTripper
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
}

// Option configures NewTransport.
type Option func(*options)

type options struct {
	provider   trace.TracerProvider
	propagator propagation.TextMapPropagator
}

// WithTracerProvider makes the spans with provider instead of the global one.
func WithTracerProvider(provider trace.TracerProvider) Option {
	return func(o *options) { o.provider = provider }
}

// WithPropagator sends the trace context with propagator, for a
// destination that takes part in the installation's trace; by default
// nothing is sent.
func WithPropagator(propagator propagation.TextMapPropagator) Option {
	return func(o *options) { o.propagator = propagator }
}

// NewTransport is a Transport over base (http.DefaultTransport when nil).
func NewTransport(base http.RoundTripper, opts ...Option) *Transport {
	if base == nil {
		base = http.DefaultTransport
	}

	o := options{provider: otel.GetTracerProvider(), propagator: propagation.NewCompositeTextMapPropagator()}
	for _, opt := range opts {
		opt(&o)
	}

	return &Transport{base: base, tracer: o.provider.Tracer(instrumentation), propagator: o.propagator}
}

// method is req's method as a bounded attribute value: the methods
// OpenTelemetry names, any other _OTHER.
func method(name string) string {
	switch name {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace:
		return name
	default:
		return "_OTHER"
	}
}

// CloseIdleConnections closes the base's idle connections when it keeps
// any (http.Client.CloseIdleConnections reaches the base through it).
func (t *Transport) CloseIdleConnections() {
	switch base := t.base.(type) {
	case interface{ CloseIdleConnections() }:
		base.CloseIdleConnections()
	default:
	}
}

// RoundTrip sends req as a client span of its context's trace.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	host, port := req.URL.Hostname(), req.URL.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[req.URL.Scheme]
	}

	verb := method(req.Method)
	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", verb), attribute.String("server.address", host),
	}

	if p, err := strconv.Atoi(port); err == nil {
		attrs = append(attrs, attribute.Int("server.port", p))
	}

	ctx, span := t.tracer.Start(req.Context(), "HTTP "+verb,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	defer span.End()

	out := req.Clone(ctx)
	t.propagator.Inject(ctx, propagation.HeaderCarrier(out.Header))

	res, err := t.base.RoundTrip(out)
	if err != nil {
		// The error's text may name the URL: only its class is recorded.
		span.SetAttributes(attribute.String("error.type", errorType(err)))
		span.SetStatus(codes.Error, "request failed")

		return nil, fmt.Errorf("tracehttp: %w", err)
	}

	span.SetAttributes(attribute.Int("http.response.status_code", res.StatusCode))

	if res.StatusCode >= statusServerError {
		span.SetAttributes(attribute.String("error.type", strconv.Itoa(res.StatusCode)))
		span.SetStatus(codes.Error, "server error")
	}

	return res, nil
}

// errorType is a bounded class of a transport failure.
func errorType(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	return "transport"
}
