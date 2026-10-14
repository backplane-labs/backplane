// Package admin is hello's internal API, for its console plugin only: gRPC
// on the platform port behind the internal secret.
package admin

import (
	"context"

	"github.com/backplane-labs/backplane/examples/hello/internal/audit"
	"github.com/backplane-labs/backplane/examples/hello/internal/greeter"
	"github.com/backplane-labs/backplane/examples/hello/internal/store"
	helloconsolev1 "github.com/backplane-labs/backplane/examples/hello/proto/hello/console/v1"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
)

// Admin serves AdminService.
type Admin struct {
	helloconsolev1.UnimplementedAdminServiceServer
	deps.Component

	greeter *greeter.Greeter
	audit   *audit.Audit
	db      deps.Dependency[*store.DB]
	cache   deps.Optional[*store.DB]
}

// New creates the admin component.
func New(
	parent deps.Scope, g *greeter.Greeter, a *audit.Audit, db deps.Dependency[*store.DB], cache deps.Optional[*store.DB],
) *Admin {
	return &Admin{Component: deps.NewComponent(parent, "admin"), greeter: g, audit: a, db: db, cache: cache}
}

// GetStats implements AdminService.
func (a *Admin) GetStats(
	ctx context.Context, _ *helloconsolev1.GetStatsRequest,
) (*helloconsolev1.GetStatsResponse, error) {
	text, err := a.greeter.Text(ctx, "you")
	if err != nil {
		return nil, err //nolint:wrapcheck // already carries the node path
	}

	// An optional dependency may be absent (not provided yet, or failing its
	// probe under ProbeOptional): Get says so.
	cache, present := a.cache.Get()
	if present {
		cache.Inc(ctx, "stats")
	}

	return &helloconsolev1.GetStatsResponse{
		Greetings: a.db.Get().Total(), CurrentGreeting: text, Audited: a.audit.Total(), CachePresent: present,
	}, nil
}
