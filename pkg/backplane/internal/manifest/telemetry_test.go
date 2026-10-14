package manifest_test

import (
	"errors"
	"testing"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
)

func TestTelemetryPresence(t *testing.T) {
	t.Parallel()

	builder := manifest.New("hello", "1.0.0")
	if build(t, builder).GetTelemetry() != nil {
		t.Fatal("omitted declaration became empty")
	}

	builder.Telemetry(&backplanev1.TelemetrySources{})

	if build(t, builder).GetTelemetry() == nil {
		t.Fatal("empty declaration lost presence")
	}

	builder.Telemetry(&backplanev1.TelemetrySources{})

	if _, err := builder.Build(); !errors.Is(err, manifest.ErrDuplicate) {
		t.Fatal(err)
	}
}

func TestTelemetryClone(t *testing.T) {
	t.Parallel()

	builder := manifest.New("hello", "1.0.0")
	declaration := &backplanev1.TelemetrySources{Selectors: []*backplanev1.TelemetrySourceSelector{{Resource: map[string]string{"service.name": "external"}}}}
	builder.Telemetry(declaration)
	declaration.Selectors[0].Resource["service.name"] = "mutated"

	if build(t, builder).GetTelemetry().GetSelectors()[0].GetResource()["service.name"] != "external" {
		t.Fatal("declaration aliases author data")
	}
}
