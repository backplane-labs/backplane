package manifest_test

import (
	"testing"

	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
)

// A route matches by host and prefix together: the same prefix for two
// hosts is two routes, the same host and prefix twice is a duplicate.
func TestSDKVersion(t *testing.T) {
	t.Parallel()

	if manifest.SDKVersion() == "" {
		t.Fatal("empty SDK version")
	}
}
