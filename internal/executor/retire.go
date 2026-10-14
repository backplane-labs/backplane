package executor

import (
	"context"
	"fmt"
	"time"

	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/internal/registry"
)

// absenceVerifier rechecks authoritative discovery before destructive cleanup.
// The real registry implements it; immutable test catalogs need no I/O check.
type absenceVerifier interface {
	Absent(ctx context.Context, service string) (bool, error)
}

func present(cat registry.Catalog, name string) bool {
	return len(cat.Services[name].Instances) != 0
}

// expired tracks continuous absence, including endpoints left by an earlier
// backplane process. A health failure is not absence. A returning instance
// resets the entire grace period. Called with syncMu held.
func (x *Executor) expired(cat registry.Catalog, name string, now time.Time) bool {
	if present(cat, name) {
		delete(x.absent, name)
		return false
	}

	since, ok := x.absent[name]
	if !ok {
		since = now
		x.absent[name] = since
	}

	return now.Sub(since) >= x.absenceGrace
}

// retire removes only endpoints targeting this installation's namespace and
// queue. Versioned deletes tolerate concurrent replicas. Retained manifests
// alone do not recreate a retired endpoint. A returning service does.
func (x *Executor) retire(ctx context.Context, c client.Client, cat registry.Catalog, set hookSet) error {
	have, err := endpoints(ctx, c)
	if err != nil {
		return err
	}

	candidates := map[string]bool{}
	for name := range set {
		candidates[name] = true
	}

	for name, e := range have {
		if x.targets(e) {
			candidates[name] = true
		}
	}

	now := time.Now()

	for name := range x.absent {
		if !candidates[name] {
			delete(x.absent, name)
		}
	}

	for name := range candidates {
		if !x.expired(cat, name, now) {
			continue
		}

		absent, err := x.confirmAbsent(ctx, name)
		if err != nil {
			return err
		}

		if !absent {
			delete(x.absent, name)
			continue
		}

		if e := have[name]; e != nil && x.targets(e) {
			if err := x.removeEndpoint(ctx, c, e); err != nil {
				return err
			}
		}

		delete(set, name)
	}

	return nil
}

func (x *Executor) confirmAbsent(ctx context.Context, name string) (bool, error) {
	// A discovery update during this pass takes precedence over its old snapshot.
	if cat := x.src.Current(); cat.Index == 0 || present(cat, name) {
		return false, nil
	}

	if verifier, ok := x.src.(absenceVerifier); ok {
		absent, err := verifier.Absent(ctx, name)
		if err != nil {
			return false, fmt.Errorf("executor: confirm absence of %s: %w", name, err)
		}

		return absent, nil
	}

	return true, nil
}

func (x *Executor) removeEndpoint(ctx context.Context, c client.Client, e *nexuspb.Endpoint) error {
	_, err := c.OperatorService().DeleteNexusEndpoint(ctx, &operatorservice.DeleteNexusEndpointRequest{
		Id: e.GetId(), Version: e.GetVersion(),
	})
	switch status.Code(err) {
	case codes.OK:
		x.Log().Info("nexus endpoint retired", xlog.String("endpoint", e.GetSpec().GetName()))
		return nil
	case codes.NotFound, codes.FailedPrecondition:
		return nil // a replica already removed or changed this exact version
	default:
		return fmt.Errorf("executor: delete nexus endpoint %s: %w", e.GetSpec().GetName(), err)
	}
}
