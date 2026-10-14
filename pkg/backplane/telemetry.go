package backplane

import (
	"maps"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

// TelemetrySources declares optional view presets using exact OTel resource
// attribute matches. Selectors are OR alternatives; each map's entries are AND.
// Calling with no selectors explicitly disables presets. Not calling retains
// convention matching by service.name. No admission/visibility rules are created.
func (c *core) TelemetrySources(selectors ...map[string]string) {
	c.declare("telemetry", func() {
		sources := &backplanev1.TelemetrySources{}
		for _, attributes := range selectors {
			sources.Selectors = append(sources.Selectors, &backplanev1.TelemetrySourceSelector{Resource: maps.Clone(attributes)})
		}

		c.env.Manifest.Telemetry(sources)
	})
}
