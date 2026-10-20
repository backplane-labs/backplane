package config

import "time"

// Backplane is the SDK's own block, part of the service configuration:
//
//	type Config struct {
//	    config.Backplane `json:"backplane"`
//	    DB string        `json:"db"`
//	}
//
// Its environment names are fixed (BACKPLANE_*) for every service. Every
// dependency is optional for a service: an empty address means "without it".
type Backplane struct {
	Consul   Consul   `json:"consul"`
	NATS     NATS     `json:"nats"`
	Temporal Temporal `json:"temporal"`
	Hooks    Hooks    `json:"hooks"`
	Shutdown Shutdown `json:"shutdown"`
	Health   Health   `json:"health"`
	// Limits of the SDK's HTTP and gRPC servers (public and platform ports).
	Server Server `json:"server"`
	// Metric export timing and histogram aggregation.
	Telemetry Telemetry `json:"telemetry"`
	// Mutual TLS between services.
	Mesh Mesh `json:"mesh"`

	// Instance id; default <service>-<hostname>.
	Instance string `json:"instance,omitempty"`
	// Address other components reach this instance at; default POD_IP, then
	// the hostname's address.
	Advertise string `json:"advertise,omitempty"`
	// deployment.environment.name for telemetry; exporters use OTEL_*.
	Environment string `json:"environment,omitempty"`
	// Platform port: internal API, health, probes, UI bundle.
	InternalPort int64 `json:"internal_port" schemapb:"default=9400;gte=1;lte=65535"`
	// Shared secret the console relay presents; empty disables the check.
	InternalSecret Secret `json:"internal_secret,omitempty"`
	// Also accepted while the secret rotates: set the new one as
	// internal_secret and the old one here, then drop it.
	InternalSecretPrevious Secret `json:"internal_secret_previous,omitempty"`
	// Default listener of managed public routes.
	PublicPort int64 `json:"public_port" schemapb:"default=8080;gte=1;lte=65535"`
	// Log level; changed live from the console.
	LogLevel Live[string] `json:"log_level" schemapb:"default=info"`
	// Serve /debug/pprof on the platform port behind the internal secret.
	Pprof bool `json:"pprof" schemapb:"default=false"`
}

// TLS of a client connection. PEM contents, not paths: they come from
// secrets like any other value. Empty CA means the system pool.
type TLS struct {
	Enabled    bool   `json:"enabled"               schemapb:"default=false"`
	CA         string `json:"ca,omitempty"`
	Cert       string `json:"cert,omitempty"`
	Key        Secret `json:"key,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	// Development only.
	InsecureSkipVerify bool `json:"insecure_skip_verify" schemapb:"default=false"`
}

// Health evaluation of the probes.
type Health struct {
	Interval time.Duration `json:"interval" schemapb:"default=5s"`
	// Per evaluation; at most interval.
	Timeout time.Duration `json:"timeout" schemapb:"default=3s"`
}

// Server limits, applied to the public and platform listeners.
type Server struct {
	ReadHeaderTimeout time.Duration `json:"read_header_timeout" schemapb:"default=10s"`
	IdleTimeout       time.Duration `json:"idle_timeout"        schemapb:"default=2m"`
	MaxHeaderBytes    int64         `json:"max_header_bytes"    schemapb:"default=1048576;gte=1"`
	// gRPC: largest message received.
	GRPCMaxRecvMsgSize int64 `json:"grpc_max_recv_msg_size" schemapb:"default=4194304;gte=1"`
	// gRPC: minimum interval of client keepalive pings; some clients ping more
	// often than grpc-go's 5m default allows.
	GRPCKeepaliveMinTime time.Duration `json:"grpc_keepalive_min_time" schemapb:"default=30s"`
	// gRPC: accept keepalive pings on connections without streams.
	GRPCPermitWithoutStream bool `json:"grpc_permit_without_stream" schemapb:"default=true"`
}

// Backplaner is satisfied by any struct embedding Backplane.
type Backplaner interface{ BackplaneConfig() Backplane }

// BackplaneConfig returns the block; promoted to the embedding struct.
func (b Backplane) BackplaneConfig() Backplane { return b }

// Consul connection.
type Consul struct {
	Addr       string `json:"addr,omitempty"`
	Token      Secret `json:"token,omitempty"`
	Datacenter string `json:"datacenter,omitempty"`
	TLS        TLS    `json:"tls"`
	// false when the deployment registers the service itself.
	Register bool `json:"register" schemapb:"default=true"`
	// Tags of the catalog registration.
	Tags []string `json:"tags,omitempty"`
	// Health check of the catalog registration.
	CheckInterval   time.Duration `json:"check_interval"   schemapb:"default=10s"`
	CheckTimeout    time.Duration `json:"check_timeout"    schemapb:"default=5s"`
	DeregisterAfter time.Duration `json:"deregister_after" schemapb:"default=1m"`
	// TTL of the session the instance state lives under.
	SessionTTL time.Duration `json:"session_ttl" schemapb:"default=30s"`
}

// Enabled reports whether Consul is configured.
func (c Consul) Enabled() bool { return c.Addr != "" }

// NATS connection and the streams the service owns: its events
// bp_<service> (applied when a service that declares events starts) and
// its dead letters bp_dlq_<service> (applied when its reactors start).
// Zero max_age, max_bytes or dlq_max_age mean unlimited.
type NATS struct {
	URL   Secret `json:"url,omitempty"`
	Creds Secret `json:"creds,omitempty"`
	TLS   TLS    `json:"tls"`
	// Bound of a Publish whose context has no deadline.
	PublishTimeout time.Duration `json:"publish_timeout" schemapb:"default=5s"`

	// Retention of the service's events.
	MaxAge time.Duration `json:"max_age" schemapb:"default=168h"`
	// Size of the event stream in bytes; the oldest events go first.
	MaxBytes int64 `json:"max_bytes" schemapb:"default=0;gte=0"`
	// Replicas of both streams (JetStream cluster).
	Replicas int64 `json:"replicas" schemapb:"default=1;gte=1;lte=5"`
	// Window in which a repeated publish (same ce-id) is dropped; at most
	// max_age.
	DedupWindow time.Duration `json:"dedup_window" schemapb:"default=2m"`
	// Retention of the dead letters of the service's reactors.
	DLQMaxAge time.Duration `json:"dlq_max_age" schemapb:"default=720h"`
}

// Enabled reports whether NATS is configured.
func (n NATS) Enabled() bool { return n.URL != "" }

// Temporal connection. Field is `ns`: `namespace` is a CEL reserved word.
type Temporal struct {
	Addr      string `json:"addr,omitempty"`
	Namespace string `json:"ns"             schemapb:"default=default"`
	TLS       TLS    `json:"tls"`
	// Temporal Cloud API key.
	APIKey Secret `json:"api_key,omitempty"`
	// First dial attempt; later attempts run in the background.
	DialTimeout time.Duration `json:"dial_timeout" schemapb:"default=2s"`
	// Deadline of a hook call whose context and declaration set none.
	HookTimeout time.Duration `json:"hook_timeout" schemapb:"default=30s"`
	Worker      Worker        `json:"worker"`
}

// Worker on the service's task queue: an operational setting, the same
// code runs with any of it. A zero limit is Temporal's default.
type Worker struct {
	// false: this replica runs no worker (activities and the author's
	// workflows are served by replicas that do); it still raises hooks
	// (their own worker runs on every replica) and
	// uses the client.
	Enabled bool `json:"enabled" schemapb:"default=true"`
	// Activities executing at once.
	MaxConcurrentActivities int64 `json:"max_concurrent_activities" schemapb:"default=0;gte=0"`
	// Workflow tasks executing at once.
	MaxConcurrentWorkflowTasks int64 `json:"max_concurrent_workflow_tasks" schemapb:"default=0;gte=0"`
	// Pollers of the activity and workflow task queues.
	ActivityPollers int64 `json:"activity_pollers" schemapb:"default=0;gte=0"`
	WorkflowPollers int64 `json:"workflow_pollers" schemapb:"default=0;gte=0"`
}

// Enabled reports whether Temporal is configured.
func (t Temporal) Enabled() bool { return t.Addr != "" }

// Hooks: how this service's hook calls reach backplane.
type Hooks struct {
	// Transport of hook calls: "temporal" (Nexus through the configured
	// Temporal), "direct" (gRPC to backplane's platform port, its replicas
	// found through Consul), or "auto": temporal when Temporal is
	// configured, else direct when Consul is.
	Transport string `json:"transport" schemapb:"default=auto"`
}

// Transports of hook calls.
const (
	HooksAuto     = "auto"
	HooksTemporal = "temporal"
	HooksDirect   = "direct"
)

// Resolve is the transport "auto" stands for given what is configured:
// HooksTemporal, HooksDirect or "" when neither Temporal nor Consul is.
func (h Hooks) Resolve(temporal, consul bool) string {
	switch h.Transport {
	case HooksTemporal:
		return HooksTemporal
	case HooksDirect:
		return HooksDirect
	default:
	}

	switch {
	case temporal:
		return HooksTemporal
	case consul:
		return HooksDirect
	default:
		return ""
	}
}

// Shutdown of the instance: readiness drops, Consul deregisters, then after
// Drain the listeners stop accepting and in-flight work finishes; everything
// within Timeout. Keep terminationGracePeriodSeconds above Timeout.
type Shutdown struct {
	Timeout time.Duration `json:"timeout" schemapb:"default=25s"`
	// Time for load balancers to notice the instance left before its
	// listeners close.
	Drain time.Duration `json:"drain" schemapb:"default=3s"`
	// Longest the listeners drain; then open streams are cut.
	Listeners time.Duration `json:"listeners" schemapb:"default=10s"`
	// Kept for the author's tree, dependencies and telemetry after the
	// listeners: drain + listeners + reserve <= timeout.
	Reserve time.Duration `json:"reserve" schemapb:"default=5s"`
}
