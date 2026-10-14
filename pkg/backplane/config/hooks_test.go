package config_test

import (
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// auto picks Temporal when it is configured, else direct when Consul is;
// an explicit transport is kept whatever is configured.
func TestHooksResolve(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		transport        string
		temporal, consul bool
		want             string
	}{
		{"", true, true, config.HooksTemporal},
		{"auto", true, false, config.HooksTemporal},
		{"auto", false, true, config.HooksDirect},
		{"auto", false, false, ""},
		{"direct", true, true, config.HooksDirect},
		{"temporal", false, true, config.HooksTemporal},
		{"nonsense", false, true, config.HooksDirect},
	} {
		got := config.Hooks{Transport: tc.transport}.Resolve(tc.temporal, tc.consul)
		if got != tc.want {
			t.Errorf("%q temporal=%v consul=%v: %q, want %q", tc.transport, tc.temporal, tc.consul, got, tc.want)
		}
	}
}
