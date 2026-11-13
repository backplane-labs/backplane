package upstream

import (
	"strconv"
	"strings"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// A full batch of browser exceptions from a few services: the encoding cost
// of one upstream request.
func BenchmarkEncode(b *testing.B) {
	stack := strings.Repeat("    at handler (https://shop.example.com/assets/app.js:1:42)\n", 40)

	batch := make([]Record, 512)
	for i := range batch {
		resource := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "service.name", Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_StringValue{StringValue: "web-" + strconv.Itoa(i%4)},
		}}}}
		batch[i] = Record{Resource: resource, Log: &logspb.LogRecord{
			Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "TypeError: x is undefined"}},
			Attributes: []*commonpb.KeyValue{{Key: "exception.stacktrace", Value: &commonpb.AnyValue{
				Value: &commonpb.AnyValue_StringValue{StringValue: stack},
			}}},
		}}
	}

	for b.Loop() {
		if _, err := encode(batch); err != nil {
			b.Fatal(err)
		}
	}
}
