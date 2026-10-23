package ops_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// Durations are written as in the rest of backplane's YAML ("30m"), or as
// protojson seconds ("1800s").
func TestReadSchedulesGoDurations(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	body := "schedules:\n" +
		"  - {service: media, name: go, workflow: Collect, timing: {interval: 1h30m}, execution_timeout: 30m}\n" +
		"  - {service: media, name: proto, workflow: Collect, timing: {interval: 5400s}, executionTimeout: 1.5s}\n"

	if err := os.WriteFile(filepath.Join(dir, "w.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string][2]time.Duration{
		"go":    {90 * time.Minute, 30 * time.Minute},
		"proto": {90 * time.Minute, 1500 * time.Millisecond},
	} {
		def, err := ops.ScheduleDefinition(dir, "media", name)
		if err != nil || def == nil {
			t.Fatalf("%s: %v", name, err)
		}

		if got := def.GetTiming().GetInterval().AsDuration(); got != want[0] {
			t.Errorf("%s: interval %v, want %v", name, got, want[0])
		}

		if got := def.GetExecutionTimeout().AsDuration(); got != want[1] {
			t.Errorf("%s: execution_timeout %v, want %v", name, got, want[1])
		}
	}
}
