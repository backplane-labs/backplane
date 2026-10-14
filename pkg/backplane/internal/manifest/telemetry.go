package manifest

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
)

var errTelemetry = errors.New("manifest: empty telemetry resource attribute")

// Telemetry retains declaration presence, including an explicitly empty set.
func (b *Builder) Telemetry(sources *backplanev1.TelemetrySources) {
	b.with("telemetry", func(m *backplanev1.Manifest) error {
		if m.GetTelemetry() != nil {
			return fmt.Errorf("%w: telemetry declared twice", ErrDuplicate)
		}

		for _, selector := range sources.GetSelectors() {
			for key := range selector.GetResource() {
				if strings.TrimSpace(key) == "" {
					return errTelemetry
				}
			}
		}

		m.Telemetry = proto.CloneOf(sources)

		return nil
	})
}
