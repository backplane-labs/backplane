// Package tracehttp traces outbound HTTP requests without recording what a
// URL may hide: every request is a client span of the caller's trace whose
// attributes are the method, the server's host and port and the response
// status, never the path or the query (a device token, a webhook secret, a
// signed link), and the W3C trace context goes out in its headers.
// OpenTelemetry's otelhttp records the full URL and cannot be told not to.
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
type Transport struct{ base http.RoundTripper }

// NewTransport is a Transport over base (http.DefaultTransport when nil).
func NewTransport(base http.RoundTripper) *Transport {
	if base == nil {
		base = http.DefaultTransport
	}

	return &Transport{base: base}
}

// RoundTrip sends req as a client span of its context's trace.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	host, port := req.URL.Hostname(), req.URL.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[req.URL.Scheme]
	}

	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", req.Method), attribute.String("server.address", host),
	}

	if p, err := strconv.Atoi(port); err == nil {
		attrs = append(attrs, attribute.Int("server.port", p))
	}

	ctx, span := otel.Tracer(instrumentation).Start(req.Context(), "HTTP "+req.Method,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	defer span.End()

	out := req.Clone(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(out.Header))

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
