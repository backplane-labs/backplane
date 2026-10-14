package telemetry_test

import (
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/telemetry"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/testlog"
)

func TestStatus(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	tel := telemetry.New(telemetry.Identity{Service: "svc"}, testlog.Discard())

	if configured, _, _ := tel.Status(); configured {
		t.Fatal("configured without an endpoint")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://collector:4318")

	if configured, up, err := tel.Status(); !configured || !up || err != nil {
		t.Fatalf("with an endpoint: %v %v %v", configured, up, err)
	}
}
