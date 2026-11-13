package tracehttp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/backplane-labs/backplane/pkg/backplane/tracehttp"
)

// spans records the client spans: the SDK's global provider and W3C
// propagator stand in, set once for the package.
var spans = tracetest.NewSpanRecorder()

func TestMain(m *testing.M) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	os.Exit(m.Run())
}

// A request is a client span of the caller's trace carrying traceparent;
// the span names the method, host, port and status, never the path or the
// query; a 5xx is an error.
func TestTransport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		failed bool
	}{
		{name: "ok", status: http.StatusOK},
		{name: "server error", status: http.StatusBadGateway, failed: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got <- r.Header.Get("Traceparent")
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(srv.Close)

			ctx, caller := otel.Tracer("test").Start(t.Context(), "caller")

			secretURL := srv.URL + "/3/device/secret-device-token?sig=secret-signature"

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, secretURL, http.NoBody)
			if err != nil {
				t.Fatal(err)
			}

			res, err := (&http.Client{Transport: tracehttp.NewTransport(nil)}).Do(req)
			if err != nil {
				t.Fatal(err)
			}

			_ = res.Body.Close()
			caller.End()

			if tp := <-got; !strings.Contains(tp, caller.SpanContext().TraceID().String()) {
				t.Fatalf("traceparent %q", tp)
			}

			span := clientSpanOf(t, caller.SpanContext().SpanID().String())
			for _, a := range span.Attributes() {
				if strings.Contains(a.Value.String(), "secret") {
					t.Fatalf("the span records %s = %s", a.Key, a.Value.String())
				}
			}

			if span.Name() != "HTTP POST" || (span.Status().Code == codes.Error) != tc.failed {
				t.Fatalf("span %s status %v", span.Name(), span.Status())
			}
		})
	}
}

// A transport failure is an error span whose attributes keep the URL out.
func TestTransportFailure(t *testing.T) {
	t.Parallel()

	ctx, caller := otel.Tracer("test").Start(context.Background(), "caller")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:1/hook/secret-token", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tracehttp.NewTransport(nil).RoundTrip(req); err == nil {
		t.Fatal("dialing a closed port succeeded")
	}

	caller.End()

	span := clientSpanOf(t, caller.SpanContext().SpanID().String())
	if span.Status().Code != codes.Error || len(span.Events()) != 0 {
		t.Fatalf("status %v, events %v", span.Status(), span.Events())
	}
}

func clientSpanOf(t *testing.T, parent string) sdktrace.ReadOnlySpan {
	t.Helper()

	for _, s := range spans.Ended() {
		if s.Parent().SpanID().String() == parent {
			return s
		}
	}

	t.Fatal("no client span under the caller")

	return nil
}

// Private providers (parallel tests, components with their own telemetry)
// and a bounded method attribute.
func TestOptionsAndMethod(t *testing.T) {
	t.Parallel()

	own := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(own))

	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Traceparent")
	}))
	t.Cleanup(srv.Close)

	ctx, caller := provider.Tracer("test").Start(t.Context(), "caller")

	req, err := http.NewRequestWithContext(ctx, "PURGE", srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Transport: tracehttp.NewTransport(nil,
		tracehttp.WithTracerProvider(provider), tracehttp.WithPropagator(propagation.TraceContext{}))}

	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = res.Body.Close()
	caller.End()

	if tp := <-got; !strings.Contains(tp, caller.SpanContext().TraceID().String()) {
		t.Fatalf("traceparent %q", tp)
	}

	var span sdktrace.ReadOnlySpan

	for _, s := range own.Ended() {
		if s.Parent().SpanID() == caller.SpanContext().SpanID() {
			span = s
		}
	}

	if span == nil || span.Name() != "HTTP _OTHER" {
		t.Fatalf("span on the private provider: %v", span)
	}

	for _, a := range span.Attributes() {
		if a.Key == "http.request.method" && a.Value.AsString() != "_OTHER" {
			t.Fatalf("method attribute %s", a.Value.AsString())
		}
	}
}
