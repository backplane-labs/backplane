// hello is the reference service: every part of the SDK's author API, each
// where a real service would put it.
//
//   - internal/store: a contrib-style provider (config section + Provider)
//     used as a required dependency and as an optional one (ProbeOptional).
//   - internal/greeter: a component with Go, Span, Meter, a Singleton, Live
//     configuration with Watch and Validate; declares and publishes Greeted
//     (Key, ID, Header); serves HelloService.
//   - internal/audit: a reactor on hello.Greeted with a pinned Consumer and
//     delivery options, idempotent through event.DeliveryOf.
//   - internal/flows: the Greet hook (Call and workflows.CallHook), the Echo
//     activity (Handle) and the workflow-backed Welcome, workflows Declare,
//     Register and a Report workflow for administrative schedules.
//   - internal/web and internal/legacy: HTTP, GraphQL, middleware and
//     interceptors; a listener of its own announced by svc.API.
//   - internal/admin: the internal API; HELLO_UI_ASSETS supplies the UI bundle.
//
// Consul, NATS and Temporal are optional: without them hello starts with
// warnings and the local fallbacks answer.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"

	"github.com/gopherex/xprobe/pkg/probe"

	"github.com/backplane-labs/backplane/examples/hello/internal/admin"
	"github.com/backplane-labs/backplane/examples/hello/internal/audit"
	"github.com/backplane-labs/backplane/examples/hello/internal/flows"
	"github.com/backplane-labs/backplane/examples/hello/internal/greeter"
	"github.com/backplane-labs/backplane/examples/hello/internal/legacy"
	"github.com/backplane-labs/backplane/examples/hello/internal/store"
	"github.com/backplane-labs/backplane/examples/hello/internal/web"
	helloconsolev1 "github.com/backplane-labs/backplane/examples/hello/proto/hello/console/v1"
	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/api"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/standard"
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
	Audit    *audit.Audit
	Flows    *flows.Flows
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
	st.Audit = audit.New(root, g.Greeted())
	st.Flows = flows.New(root, g, st.Store)
	st.Admin = admin.New(root, g, st.Audit, st.Store, st.Cache)

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
	svc, err := backplane.Open(ctx, NewState, standard.Drivers())
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
	svc.HTTP("/hello/", web.Hello(st.Flows.Greet, st.Greeter),
		api.OpenAPIFS(web.API, "openapi.yaml"),
		api.Middleware(web.ServedBy(svc.Name())))
	svc.GraphQL("/graphql/", web.GraphQL(st.Greeter), web.Introspection)

	// A listener hello serves itself, described for the API catalog.
	svc.API(api.HTTP(legacy.Prefix,
		api.Port(st.Legacy.Port()), api.OpenAPI(legacy.API)))

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
