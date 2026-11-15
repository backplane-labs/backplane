package telemetry_test

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/backplane-labs/backplane/internal/testhttp"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/telemetry"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

func TestStatus(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	tel := telemetry.New(telemetry.Identity{Service: "svc"}, config.Telemetry{}, testlog.Discard())

	if configured, _, _ := tel.Status(); configured {
		t.Fatal("configured without an endpoint")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://collector:4318")

	if configured, up, err := tel.Status(); !configured || !up || err != nil {
		t.Fatalf("with an endpoint: %v %v %v", configured, up, err)
	}
}

// A server span leaves the service without the caller's address: the
// exported span (a real OTLP export) has no client.* / network.peer.*
// attributes, the rest stays.
func TestCallerAddressNeverExported(t *testing.T) {
	var (
		mu       sync.Mutex
		exported []*tracepb.Span
	)

	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Errorf("export: %v", err)

				return
			}

			body = zr
		}

		raw, _ := io.ReadAll(body)
		req := &coltracepb.ExportTraceServiceRequest{}

		if err := proto.Unmarshal(raw, req); err != nil {
			t.Errorf("export: %v", err)
		}

		mu.Lock()
		for _, rs := range req.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				exported = append(exported, ss.GetSpans()...)
			}
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(nil)
	}))
	t.Cleanup(collector.Close)

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf")

	tel := telemetry.New(telemetry.Identity{Service: "svc"},
		config.Telemetry{Traces: config.Traces{Sampler: config.SamplerAlwaysOn}}, testlog.Discard())
	if err := tel.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(otelhttp.NewHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), "served"))
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/x", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	res, err := testhttp.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = res.Body.Close()

	if err := tel.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(exported) == 0 {
		t.Fatal("nothing exported")
	}

	for _, s := range exported {
		keys := map[string]bool{}
		for _, a := range s.GetAttributes() {
			keys[a.GetKey()] = true
		}

		if keys["client.address"] || keys["network.peer.address"] || keys["network.peer.port"] || keys["client.port"] {
			t.Fatalf("span %s exports the caller's address: %v", s.GetName(), keys)
		}

		if !keys["http.request.method"] {
			t.Fatalf("span %s lost its other attributes: %v", s.GetName(), keys)
		}
	}
}

// A service that never sets a propagator itself continues its caller's trace
// across HTTP: Start installs W3C trace context and baggage, which otelhttp
// injects on the way out and extracts on the way in.
func TestTraceContinuesAcrossHTTP(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	tel := telemetry.New(telemetry.Identity{Service: "svc"},
		config.Telemetry{Traces: config.Traces{Sampler: config.SamplerParentBasedAlwaysOn}}, testlog.Discard())
	if err := tel.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = tel.Stop(context.Background()) })

	var (
		gotTrace   trace.TraceID
		gotBaggage string
		header     string
	)

	srv := httptest.NewServer(otelhttp.NewHandler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("Traceparent")
		gotTrace = trace.SpanContextFromContext(r.Context()).TraceID()
		gotBaggage = baggage.FromContext(r.Context()).Member("tenant").Value()
	}), "served"))
	t.Cleanup(srv.Close)

	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}, TraceFlags: trace.FlagsSampled, Remote: true,
	}))

	member, err := baggage.NewMember("tenant", "t-1")
	if err != nil {
		t.Fatal(err)
	}

	bag, err := baggage.New(member)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(baggage.ContextWithBaggage(ctx, bag), http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	res, err := (&http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = res.Body.Close()

	if header == "" || gotTrace != traceID || gotBaggage != "t-1" {
		t.Fatalf("traceparent %q, trace %s, baggage %q: the caller's trace did not continue", header, gotTrace, gotBaggage)
	}
}
