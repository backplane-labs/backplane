package manifest_test

import (
	"errors"
	"strings"
	"testing"

	backplanev1 "github.com/backplane-labs/backplane/backplanepb/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/internal/manifest"
)

// A route matches by host and prefix together: the same prefix for two
// hosts is two routes, the same host and prefix twice is a duplicate.
func TestRouteMatchIsHostAndPrefix(t *testing.T) {
	t.Parallel()

	b := manifest.New("svc", "1.0.0")
	b.API(&backplanev1.API{Prefix: "/api/", Host: "a.example.com", Port: 80})
	b.API(&backplanev1.API{Prefix: "/api/", Host: "b.example.com", Port: 80})
	b.API(&backplanev1.API{Prefix: "/api/", Port: 80})

	if got := len(build(t, b).GetApis()); got != 3 {
		t.Fatalf("routes: %d", got)
	}

	b.API(&backplanev1.API{Prefix: "/api/", Host: "a.example.com", Port: 80})

	_, err := b.Build()
	if !errors.Is(err, manifest.ErrDuplicate) || !strings.Contains(err.Error(), "host a.example.com prefix /api/") {
		t.Fatalf("duplicate host+prefix: %v", err)
	}
}

func TestSDKVersion(t *testing.T) {
	t.Parallel()

	if manifest.SDKVersion() == "" {
		t.Fatal("empty SDK version")
	}
}
