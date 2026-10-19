package backplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	httpprobe "github.com/gopherex/xprobe/pkg/transport/http"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// ProbeCommand is the first argument that turns a service's binary into a
// health probe of the service running next to it:
//
//	<binary> probe [readiness|liveness|startup]
//
// It asks the platform port's /healthz/<kind> (readiness by default) on
// 127.0.0.1 and exits 0 when the probe passes, 1 otherwise: a container
// healthcheck for distroless images, which have no curl or wget.
//
//	healthcheck:
//	  test: ["CMD", "/app/bin/kratos", "probe"]
//
// The port comes from the same configuration the service reads (file and
// environment; Consul is not asked).
const ProbeCommand = "probe"

// probeTimeout bounds one probe; maxProbeAnswer bounds the failure shown.
const (
	probeTimeout   = 5 * time.Second
	maxProbeAnswer = 1 << 10
	// usageExit is the exit status of a malformed probe command.
	usageExit = 2
)

// ErrProbe: the probe did not pass.
var ErrProbe = errors.New("backplane: probe failed")

// probePaths are the probes by kind.
//
//nolint:gochecknoglobals // a constant table
var probePaths = map[string]string{
	"readiness": httpprobe.DefaultReadinessPath,
	"liveness":  httpprobe.DefaultLivenessPath,
	"startup":   httpprobe.DefaultStartupPath,
}

// probeKind is the kind args ask for: ok when args are a probe command.
func probeKind(args []string) (string, bool, error) {
	if len(args) == 0 || len(args[1:]) == 0 || args[1] != ProbeCommand {
		return "", false, nil
	}

	switch rest := args[2:]; {
	case len(rest) == 0:
		return "readiness", true, nil
	case len(rest) == 1 && probePaths[rest[0]] != "":
		return rest[0], true, nil
	default:
		return "", true, fmt.Errorf("%w: usage: %s probe [readiness|liveness|startup]", ErrProbe, args[0])
	}
}

// runProbe loads the port from the configuration and probes it; it never
// returns: the process exits with the outcome.
func runProbe[C config.Backplaner](ctx context.Context, kind string, opts []config.Option) {
	err := func() error {
		cfg, err := config.Load[C](ctx, append(opts, config.WithoutConsul())...)
		if err != nil {
			return fmt.Errorf("%w: configuration: %w", ErrProbe, err)
		}

		port := strconv.FormatInt(cfg.BackplaneConfig().InternalPort, 10)

		return Probe(ctx, net.JoinHostPort("127.0.0.1", port), kind)
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(0)
}

// Probe asks the platform port at addr (host:port) for the probe of kind
// (readiness, liveness, startup): nil when it passes.
func Probe(ctx context.Context, addr, kind string) error {
	path, known := probePaths[kind]
	if !known {
		return fmt.Errorf("%w: unknown probe %q", ErrProbe, kind)
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, http.NoBody)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProbe, err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrProbe, kind, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, maxProbeAnswer))

		return fmt.Errorf("%w: %s: %s %s", ErrProbe, kind, res.Status, strings.TrimSpace(string(body)))
	}

	return nil
}
