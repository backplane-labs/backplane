// hello demonstrates listeners, service discovery, live configuration and a service-owned console module.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"

	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/backplane-labs/backplane/examples/hello/internal/admin"
	"github.com/backplane-labs/backplane/examples/hello/internal/greeter"
	"github.com/backplane-labs/backplane/examples/hello/internal/legacy"
	"github.com/backplane-labs/backplane/examples/hello/internal/store"
	"github.com/backplane-labs/backplane/examples/hello/internal/web"
	helloconsolev1 "github.com/backplane-labs/backplane/examples/hello/proto/hello/console/v1"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/api"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	consuldriver "github.com/backplane-labs/backplane/pkg/backplane/drivers/consul"
	"github.com/backplane-labs/backplane/pkg/backplane/wsproto"
)

const (
	// cacheRetry bounds the delay between attempts to provide the cache.
	cacheRetry = 10 * time.Second
)

// Config is the whole configuration: file < env (HELLO_*, BACKPLANE_* for the
// block) < Consul KV for Live fields only.
type Config struct {
	config.Backplane `json:"backplane"`

	Greeter  greeter.Config `json:"greeter"`
	Store    store.Config   `json:"store"`
	Cache    store.Config   `json:"cache"`
	Legacy   legacy.Config  `json:"legacy"`
	UIAssets string         `json:"ui_assets,omitempty"`
}

// State is everything the service holds: a tree built under the Root.
type State struct {
	// Required: start waits for it (retrying), readiness follows its probe.
	Store deps.Dependency[*store.DB]
	// Optional: the service starts without it and keeps retrying; while its
	// probe fails (ProbeOptional) Get reports it absent.
	Cache deps.Optional[*store.DB]

	Greeter  *greeter.Greeter
	Admin    *admin.Admin
	Legacy   *legacy.Server
	UIAssets string
}

// NewState wires the tree explicitly: every node gets what it needs by
// argument.
func NewState(root backplane.Root[Config]) (*State, error) {
	cfg := root.Config()

	st := &State{
		UIAssets: cfg.UIAssets,
		Store:    deps.NewDependency(root, store.New(&cfg.Store), deps.ProbeTimeout(time.Second)),
		Cache: deps.NewOptional(root, store.New(&cfg.Cache), deps.Name("cache"),
			deps.ProbeOptional(), deps.Backoff(time.Second, cacheRetry)),
	}

	g, err := greeter.New(root, &cfg.Greeter, st.Store)
	if err != nil {
		return nil, fmt.Errorf("greeter: %w", err)
	}

	st.Greeter = g
	st.Admin = admin.New(root, g, st.Store, st.Cache)

	if st.Legacy, err = legacy.New(root, &cfg.Legacy); err != nil {
		return nil, err //nolint:wrapcheck // prefixed by legacy
	}

	return st, nil
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	svc, err := backplane.Open(ctx, NewState, consuldriver.Driver())
	if err != nil {
		return err //nolint:wrapcheck // prefixed by backplane
	}

	st := svc.State()

	// External API: one implementation over native gRPC and ws-proto.
	svc.GRPC(st.Greeter.Register, api.Reflection(),
		api.Interceptors(web.Unary(svc.Log())), api.StreamInterceptors(web.Stream(svc.Log())))
	// The zero origin policy: same-origin browsers and non-browser clients.
	wsproto.Serve(svc, "/ws/", api.Origins{}, st.Greeter.Register,
		api.Interceptors(web.Unary(svc.Log())), api.Middleware(web.ServedBy(svc.Name())))

	// HTTP and GraphQL, with application middleware.
	svc.HTTP("/hello/", web.Hello(st.Greeter),
		api.Middleware(web.ServedBy(svc.Name())))
	svc.HTTP("/graphql/", web.GraphQL(st.Greeter))

	svc.Listener("legacy", st.Legacy.Port())

	// Internal API and UI bundle for the console plugin.
	svc.Internal(func(r grpc.ServiceRegistrar) { helloconsolev1.RegisterAdminServiceServer(r, st.Admin) })

	// UI delivery is optional; make dev supplies the real module build.
	if st.UIAssets != "" {
		svc.UI(os.DirFS(st.UIAssets))
	}

	// Probes beyond the required dependencies, which are readiness already.
	svc.LivenessProbe(probe.FromError(st.Greeter.Alive))
	svc.ReadinessProbe(probe.FromError(st.Greeter.Ready))

	return svc.Run(ctx) //nolint:wrapcheck // prefixed by backplane
}
