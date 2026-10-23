package ops_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/backplane-labs/backplane/internal/ops"
)

// A directory that does not read whole is refused: entries without an
// identity, unknown fields, a schedule defined twice.
func TestReadSchedulesRefuses(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"no name":       "schedules:\n  - {service: media, workflow: Collect, timing: {interval: 60s}}\n",
		"unknown field": "schedules:\n  - {service: media, name: c, workflow: Collect, every: 60s}\n",
		"twice":         "schedules:\n  - {service: media, name: c, workflow: A}\n---\nschedules:\n  - {service: media, name: c, workflow: B}\n",
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "w.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := ops.ReadSchedules(dir); !errors.Is(err, ops.ErrScheduleFile) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
