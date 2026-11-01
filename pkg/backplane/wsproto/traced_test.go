package wsproto_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	grpccodes "google.golang.org/grpc/codes"

	"github.com/gopherex/ws-proto/wsrpc"

	"github.com/backplane-labs/backplane/pkg/backplane/wsproto"
)

// A call's span continues the trace the client sent on OPEN, and is an error
// only for a server fault, as the gRPC server's: a refusal to the client
// (NotFound, InvalidArgument, ...) leaves it unset.
//
//nolint:paralleltest // sets the global tracer provider and propagator traced reads
func TestTraced(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevProvider, prevPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()

	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevProvider)
		otel.SetTextMapPropagator(prevPropagator)
	})

	const traceID, parentID = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"

	for _, tc := range []struct {
		name   string
		header string // the traceparent key as the client spells it
		err    error
		status codes.Code
		event  bool
	}{
		{"ok", "traceparent", nil, codes.Unset, false},
		{"header case", "Traceparent", nil, codes.Unset, false},
		{"refusal", "traceparent", wsrpc.Errorf(grpccodes.NotFound, "no such room"), codes.Unset, false},
		{"bad input", "traceparent", wsrpc.Errorf(grpccodes.InvalidArgument, "bad"), codes.Unset, false},
		{"internal", "traceparent", wsrpc.Errorf(grpccodes.Internal, "db down"), codes.Error, true},
		{"plain error is unknown", "traceparent", errors.New("boom"), codes.Error, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec.Reset()

			srv := wsrpc.NewServer(wsrpc.WithMiddleware(wsproto.Traced))
			srv.Register("/t.v1.S/M", func(context.Context, *wsrpc.Stream) error { return tc.err })

			srvEnd, cliEnd := wsrpc.NewPipe()
			ctx, cancel := context.WithCancel(t.Context())

			t.Cleanup(func() { cancel(); _ = srvEnd.Close(); _ = cliEnd.Close() })

			go srv.ServeConn(ctx, srvEnd)

			cc, err := wsrpc.DialConn(ctx, cliEnd)
			if err != nil {
				t.Fatal(err)
			}

			stream, err := cc.NewStream(ctx, "/t.v1.S/M", map[string]string{
				tc.header: "00-" + traceID + "-" + parentID + "-01",
			})
			if err != nil {
				t.Fatal(err)
			}

			_ = stream.CloseSend()
			if _, err := stream.RecvRaw(); err != nil && !errors.Is(err, io.EOF) && tc.err == nil {
				t.Fatal(err)
			}

			ended := rec.Ended()
			if len(ended) != 1 {
				t.Fatalf("spans: %d", len(ended))
			}

			span := ended[0]
			if span.SpanContext().TraceID().String() != traceID || span.Parent().SpanID().String() != parentID ||
				!span.Parent().IsRemote() {
				t.Fatalf("trace %s parent %s", span.SpanContext().TraceID(), span.Parent().SpanID())
			}

			if span.Status().Code != tc.status || (len(span.Events()) > 0) != tc.event {
				t.Fatalf("status %v events %v", span.Status(), span.Events())
			}
		})
	}
}
