package valkey_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane/backplanetest"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/valkey"
)

// sentinelImage is the Valkey of the chaos test: primary, replica and
// sentinels.
const sentinelImage = "valkey/valkey:8-alpine"

// freePort is a port nothing listens on now.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	_, raw, err := net.SplitHostPort(l.Addr().String())
	_ = l.Close()

	if err != nil {
		t.Fatal(err)
	}

	port, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatal(err)
	}

	return port
}

// docker runs a container on the host network, removed at cleanup.
func docker(t *testing.T, name string, args ...string) {
	t.Helper()

	run := append([]string{"run", "-d", "--rm", "--name", name, "--network", "host"}, args...)
	if out, err := exec.CommandContext(t.Context(), "docker", run...).CombinedOutput(); err != nil {
		t.Fatalf("docker run %s: %v %s", name, err, out)
	}

	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "docker", "rm", "-f", name).Run() })
}

// Chaos: the primary of a master set dies; the sentinels promote the
// replica and the client follows it: writes succeed on the new primary and
// what was written before is there.
func TestSentinelFailover(t *testing.T) {
	t.Parallel()

	if os.Getenv("BACKPLANE_TEST_DOCKER") == "" {
		t.Skip("BACKPLANE_TEST_DOCKER not set: the test runs valkey containers")
	}

	run := strconv.FormatInt(time.Now().UnixNano(), 36)
	primary, replica := freePort(t), freePort(t)

	docker(t, "bp-sentinel-primary-"+run, sentinelImage, "valkey-server", "--port", strconv.Itoa(primary))
	docker(t, "bp-sentinel-replica-"+run, sentinelImage, "valkey-server", "--port", strconv.Itoa(replica),
		"--replicaof", "127.0.0.1", strconv.Itoa(primary))

	var sentinels []string

	for i := range 3 {
		port := freePort(t)
		dir := t.TempDir()
		conf := fmt.Sprintf("port %d\nsentinel monitor cache 127.0.0.1 %d 2\n"+
			"sentinel down-after-milliseconds cache 1000\nsentinel failover-timeout cache 5000\n", port, primary)

		if err := os.WriteFile(filepath.Join(dir, "sentinel.conf"), []byte(conf), 0o666); err != nil {
			t.Fatal(err)
		}

		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}

		docker(t, fmt.Sprintf("bp-sentinel-%d-%s", i, run), "-v", dir+":/conf", sentinelImage,
			"valkey-sentinel", "/conf/sentinel.conf")
		sentinels = append(sentinels, fmt.Sprintf("127.0.0.1:%d", port))
	}

	h := backplanetest.New(t)
	cache := deps.NewDependency(h.Root(), valkey.New(valkey.Config{
		Sentinel: valkey.Sentinel{MasterSet: "cache", Addrs: strings.Join(sentinels, ",")},
	}))
	h.Start()

	ctx := t.Context()
	c := cache.Get()

	if err := c.Do(ctx, c.B().Set().Key("before").Value("1").Build()).Error(); err != nil {
		t.Fatal(err)
	}

	// The write reached the replica before the primary dies.
	waitFor(t, 10*time.Second, func() bool {
		out, _ := exec.CommandContext(ctx, "docker", "exec", "bp-sentinel-replica-"+run,
			"valkey-cli", "-p", strconv.Itoa(replica), "GET", "before").Output()

		return strings.TrimSpace(string(out)) == "1"
	})

	if err := exec.CommandContext(ctx, "docker", "kill", "bp-sentinel-primary-"+run).Run(); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 30*time.Second, func() bool {
		attempt, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()

		return c.Do(attempt, c.B().Set().Key("after").Value("2").Build()).Error() == nil
	})

	if got, err := c.Do(ctx, c.B().Get().Key("before").Build()).ToString(); err != nil || got != "1" {
		t.Fatalf("before the failover: %q %v", got, err)
	}

	role, err := c.Do(ctx, c.B().Role().Build()).ToArray()
	if err != nil || len(role) == 0 {
		t.Fatalf("role: %v", err)
	}

	if r, _ := role[0].ToString(); r != "master" {
		t.Fatalf("the client is on a %s, not the promoted primary", r)
	}
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()

	for deadline := time.Now().Add(limit); !cond(); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("not reached within", limit)
		}
	}
}
