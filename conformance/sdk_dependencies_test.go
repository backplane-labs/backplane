package conformance_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Importing the SDK contracts must not compile optional backend clients.
// Go module metadata is intentionally shared with the server for now.
func TestSDKDependencyBoundary(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps",
		"../pkg/backplane", "../pkg/backplane/config", "../pkg/backplane/deps",
		"../pkg/backplane/hook", "../pkg/backplane/activity", "../pkg/backplane/event",
		"../pkg/backplane/backplanetest", "../pkg/backplane/api", "../pkg/backplane/wsproto", "../backplanepb/console/v1")

	cmd.Env = append(os.Environ(), "GOWORK=off")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SDK imports: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		for _, backend := range []string{
			"go.temporal.io/", "github.com/nexus-rpc/", "github.com/nats-io/",
			"github.com/hashicorp/consul/", "github.com/valkey-io/", "github.com/jackc/pgx/",
		} {
			if strings.HasPrefix(pkg, backend) {
				t.Errorf("SDK contracts import optional backend %s", pkg)
			}
		}
	}
}

func TestPlatformDoesNotImportRedis(t *testing.T) {
	t.Parallel()
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "../cmd/backplane")

	cmd.Env = append(os.Environ(), "GOWORK=off")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("platform imports: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, "github.com/valkey-io/") || strings.HasPrefix(pkg, "github.com/redis/") {
			t.Errorf("platform imports Redis client %s", pkg)
		}
	}
}
