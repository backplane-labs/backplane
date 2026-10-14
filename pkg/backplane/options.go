package backplane

import (
	"os"

	"google.golang.org/grpc"

	"github.com/gopherex/xlog"

	"github.com/backplane-labs/backplane/pkg/backplane/config"
)

// Option configures Open.
type Option func(*options)

type options struct {
	presence  PresenceFactory
	events    EventTransportFactory
	workflows WorkflowTransportFactory
	discovery HookDiscoveryFactory
	id        Identity
	log       *xlog.Logger
	config    []config.Option
	slog      bool
	grpc      []grpc.ServerOption

	requireNATS     bool
	requireTemporal bool

	signals <-chan os.Signal // tests: instead of SIGINT/SIGTERM
	exit    func(code int)   // tests: instead of os.Exit
}

// Name overrides build.Service.
func Name(n string) Option { return func(o *options) { o.id.Service = n } }

// Version overrides build.Version.
func Version(v string) Option { return func(o *options) { o.id.Version = v } }

// Instance overrides the instance id (default: the block's instance, then
// <service>-<hostname>).
func Instance(id string) Option { return func(o *options) { o.id.Instance = id } }

// Advertise overrides the address registered in Consul.
func Advertise(addr string) Option { return func(o *options) { o.id.Advertise = addr } }

// Logger replaces the default logger.
func Logger(l *xlog.Logger) Option { return func(o *options) { o.log = l } }

// ConfigOptions pass options to configuration loading.
func ConfigOptions(opts ...config.Option) Option {
	return func(o *options) { o.config = append(o.config, opts...) }
}

// KeepSlog leaves the log/slog default alone. By default Run routes it into
// the service logger (the OTel SDK and other libraries log through it) and
// restores it when Run returns.
func KeepSlog() Option { return func(o *options) { o.slog = false } }

// GRPCServerOptions are passed to every public gRPC server after the
// SDK's own (telemetry, limits of config.Server, panic recovery): a later
// limit overrides the SDK's; interceptors run inside recovery and outside
// those of api.Interceptors. The platform port is not affected.
func GRPCServerOptions(opts ...grpc.ServerOption) Option {
	return func(o *options) { o.grpc = append(o.grpc, opts...) }
}

// RequireNATS makes the NATS connection part of readiness: the instance is
// not ready while it is down. Without NATS configured, Open fails.
func RequireNATS() Option { return func(o *options) { o.requireNATS = true } }

// RequireTemporal makes the Temporal connection part of readiness: the
// instance is not ready while it is down. Without Temporal configured,
// Open fails.
func RequireTemporal() Option { return func(o *options) { o.requireTemporal = true } }

// Options composes explicit driver options without global registration.
func Options(opts ...Option) Option {
	return func(o *options) {
		for _, opt := range opts {
			opt(o)
		}
	}
}
