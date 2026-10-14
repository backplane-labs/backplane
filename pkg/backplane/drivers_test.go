package backplane_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// A configured backend must not silently disappear when its driver is missing.
func TestConfiguredTransportRequiresDriver(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		yaml string
		want error
	}{
		{"nats", "nats: {url: 'nats://127.0.0.1:1'}", backplane.ErrConfig},
		{"temporal", "temporal: {addr: '127.0.0.1:1'}", backplane.ErrConfig},
		{"consul", "consul: {addr: '127.0.0.1:1'}", config.ErrRemoteDriver},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(file, []byte("backplane:\n  "+tc.yaml+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			called := false

			_, err := backplane.Open(t.Context(), func(backplane.Root[testConfig]) (*testState, error) {
				called = true
				return &testState{}, nil
			}, backplane.Name("driver-test"), backplane.ConfigOptions(config.WithoutEnv(), config.File(file)))
			if !errors.Is(err, tc.want) || called {
				t.Fatalf("missing driver: constructor called=%v, error=%v", called, err)
			}
		})
	}
}
