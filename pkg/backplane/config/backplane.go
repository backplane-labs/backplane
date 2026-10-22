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
	Consul   Consul   `desc:"Service discovery, catalog registration and instance state." json:"consul"`
	NATS     NATS     `desc:"Events, reactors and dead letters (JetStream)."              json:"nats"`
	Temporal Temporal `desc:"Workflows, activities and hook calls."                       json:"temporal"`
	Hooks    Hooks    `desc:"How the service calls the platform's hooks."                 json:"hooks"`
	Shutdown Shutdown `desc:"Graceful stop budget."                                       json:"shutdown"`
	Health   Health   `desc:"Probe evaluation."                                           json:"health"`
	// Limits of the SDK's HTTP and gRPC servers (public and platform ports).
	Server Server `desc:"Limits of the HTTP and gRPC servers (public and platform ports)." json:"server"`
	// Metric export timing and histogram aggregation.
	Telemetry Telemetry `desc:"Metric export timing, histogram aggregation and trace sampling." json:"telemetry"`
	// Mutual TLS between services.
	Mesh Mesh `desc:"Mutual TLS between services." json:"mesh"`

	// Instance id; default <service>-<hostname>.
	Instance string `desc:"Instance id; empty: <service>-<hostname>." json:"instance,omitempty"`
	// Address other components reach this instance at; default POD_IP, then
	// the hostname's address.
	Advertise string `desc:"Address other components reach this instance at; empty: POD_IP, then the hostname's address." json:"advertise,omitempty"`
	// deployment.environment.name for telemetry; exporters use OTEL_*.
	Environment string `desc:"deployment.environment.name of telemetry; exporters are set with OTEL_*." json:"environment,omitempty"`
	// Platform port: internal API, health, probes, UI bundle.
	InternalPort int64 `desc:"Platform port: internal API, health, probes, UI bundle." json:"internal_port" schemapb:"default=9400;gte=1;lte=65535"`
	// Shared secret the console relay presents; empty disables the check.
	InternalSecret Secret `desc:"Shared secret the console relay presents; empty disables the check." json:"internal_secret,omitempty"`
	// Also accepted while the secret rotates: set the new one as
	// internal_secret and the old one here, then drop it.
	InternalSecretPrevious Secret `desc:"Previous internal secret, accepted during rotation: set the new one as internal_secret and the old one here, then remove it." json:"internal_secret_previous,omitempty"`
	// Default listener of managed public routes.
	PublicPort int64 `desc:"Default listener of managed public routes." json:"public_port" schemapb:"default=8080;gte=1;lte=65535"`
	// Log level; changed live from the console.
	LogLevel Live[string] `desc:"Log level: debug, info, warn or error. Applied live." json:"log_level" schemapb:"default=info"`
	// Serve /debug/pprof on the platform port behind the internal secret.
	Pprof bool `desc:"Serve /debug/pprof on the platform port behind the internal secret." json:"pprof" schemapb:"default=false"`
}

// TLS of a client connection. PEM contents, not paths: they come from
// secrets like any other value. Empty CA means the system pool.
type TLS struct {
	Enabled    bool   `desc:"Connect over TLS."                                                       json:"enabled"               schemapb:"default=false"`
	CA         string `desc:"PEM of the CA certificates to trust; empty: the system pool."            json:"ca,omitempty"`
	Cert       string `desc:"PEM of the client certificate; goes together with key."                  json:"cert,omitempty"`
	Key        Secret `desc:"PEM of the client certificate's private key."                            json:"key,omitempty"`
	ServerName string `desc:"Name the server certificate is checked against; empty: the host dialed." json:"server_name,omitempty"`
	// Development only.
	InsecureSkipVerify bool `desc:"Skip verification of the server certificate. Development only." json:"insecure_skip_verify" schemapb:"default=false"`
}

// Health evaluation of the probes.
type Health struct {
	Interval time.Duration `desc:"How often the probes are evaluated." json:"interval" schemapb:"default=5s"`
	// Per evaluation; at most interval.
	Timeout time.Duration `desc:"Bound of one evaluation; at most interval." json:"timeout" schemapb:"default=3s"`
}

// Server limits, applied to the public and platform listeners.
type Server struct {
	ReadHeaderTimeout time.Duration `desc:"HTTP: time to read the request headers."               json:"read_header_timeout" schemapb:"default=10s"`
	IdleTimeout       time.Duration `desc:"HTTP: keep-alive connection idle this long is closed." json:"idle_timeout"        schemapb:"default=2m"`
	MaxHeaderBytes    int64         `desc:"HTTP: largest request headers, in bytes."              json:"max_header_bytes"    schemapb:"default=1048576;gte=1"`
	// gRPC: largest message received.
	GRPCMaxRecvMsgSize int64 `desc:"gRPC: largest message received, in bytes." json:"grpc_max_recv_msg_size" schemapb:"default=4194304;gte=1"`
	// gRPC: largest message sent; 0: unlimited (grpc-go's default).
	GRPCMaxSendMsgSize int64 `desc:"gRPC: largest message sent, in bytes; 0: unlimited." json:"grpc_max_send_msg_size" schemapb:"default=0;gte=0"`
	// gRPC: concurrent streams per connection; 0: unlimited.
	GRPCMaxConcurrentStreams int64 `desc:"gRPC: concurrent streams per connection; 0: unlimited." json:"grpc_max_concurrent_streams" schemapb:"default=0;gte=0;lte=4294967295"`
	// gRPC: the server pings a connection idle this long (long bidi streams
	// behind NAT and load balancers that drop quiet connections).
	GRPCKeepaliveTime time.Duration `desc:"gRPC: the server pings a connection idle this long (keeps long streams alive behind NAT and load balancers)." json:"grpc_keepalive_time" schemapb:"default=2h;gte=1s"`
	// gRPC: how long the server waits for the ping's answer before closing.
	GRPCKeepaliveTimeout time.Duration `desc:"gRPC: how long the server waits for the ping's answer before closing the connection." json:"grpc_keepalive_timeout" schemapb:"default=20s;gte=1s"`
	// gRPC: a connection without streams this long is closed (GOAWAY);
	// 0: never.
	GRPCMaxConnectionIdle time.Duration `desc:"gRPC: a connection without streams this long is closed (GOAWAY); 0: never." json:"grpc_max_connection_idle" schemapb:"default=0s;gte=0"`
	// gRPC: a connection this old is closed (GOAWAY) so clients reconnect
	// and rebalance; 0: never.
	GRPCMaxConnectionAge time.Duration `desc:"gRPC: a connection this old is closed (GOAWAY) so clients reconnect and rebalance; 0: never." json:"grpc_max_connection_age" schemapb:"default=0s;gte=0"`
	// gRPC: how long streams of a connection past its age may finish; 0:
	// as long as they take.
	GRPCMaxConnectionAgeGrace time.Duration `desc:"gRPC: how long streams of a connection past its age may finish; 0: as long as they take." json:"grpc_max_connection_age_grace" schemapb:"default=0s;gte=0"`
	// gRPC: minimum interval of client keepalive pings; some clients ping more
	// often than grpc-go's 5m default allows.
	GRPCKeepaliveMinTime time.Duration `desc:"gRPC: minimum interval of client keepalive pings; more frequent pings close the connection." json:"grpc_keepalive_min_time" schemapb:"default=30s"`
	// gRPC: accept keepalive pings on connections without streams.
	GRPCPermitWithoutStream bool `desc:"gRPC: accept keepalive pings on connections without streams." json:"grpc_permit_without_stream" schemapb:"default=true"`
}

// Backplaner is satisfied by any struct embedding Backplane.
type Backplaner interface{ BackplaneConfig() Backplane }

// BackplaneConfig returns the block; promoted to the embedding struct.
func (b Backplane) BackplaneConfig() Backplane { return b }

// Consul connection.
type Consul struct {
	Addr       string `desc:"Consul agent host:port; empty: without Consul." json:"addr,omitempty"`
	Token      Secret `desc:"ACL token."                                     json:"token,omitempty"`
	Datacenter string `desc:"Datacenter; empty: the agent's."                json:"datacenter,omitempty"`
	TLS        TLS    `desc:"TLS of the connection to the agent."            json:"tls"`
	// false when the deployment registers the service itself.
	Register bool `desc:"Register the instance in the catalog; off when the deployment registers it." json:"register" schemapb:"default=true"`
	// Tags of the catalog registration.
	Tags []string `desc:"Tags of the catalog registration." json:"tags,omitempty"`
	// Health check of the catalog registration.
	CheckInterval   time.Duration `desc:"Interval of the registration's health check."           json:"check_interval"   schemapb:"default=10s"`
	CheckTimeout    time.Duration `desc:"Timeout of the registration's health check."            json:"check_timeout"    schemapb:"default=5s"`
	DeregisterAfter time.Duration `desc:"A registration failing its check this long is removed." json:"deregister_after" schemapb:"default=1m"`
	// TTL of the session the instance state lives under.
	SessionTTL time.Duration `desc:"TTL of the session the instance state lives under." json:"session_ttl" schemapb:"default=30s"`
}

// Enabled reports whether Consul is configured.
func (c Consul) Enabled() bool { return c.Addr != "" }

// NATS connection and the streams the service owns: its events
// bp_<service> (applied when a service that declares events starts) and
// its dead letters bp_dlq_<service> (applied when its reactors start).
// Zero max_age, max_bytes or dlq_max_age mean unlimited.
type NATS struct {
	URL   Secret `desc:"Server URL, or comma-separated URLs; empty: without NATS." json:"url,omitempty"`
	Creds Secret `desc:"Content of a .creds file (user JWT and seed)."             json:"creds,omitempty"`
	TLS   TLS    `desc:"TLS of the connection."                                    json:"tls"`
	// Bound of a Publish whose context has no deadline.
	PublishTimeout time.Duration `desc:"Bound of a publish whose context has no deadline." json:"publish_timeout" schemapb:"default=5s"`

	// Retention of the service's events.
	MaxAge time.Duration `desc:"Retention of the service's events; 0: unlimited." json:"max_age" schemapb:"default=168h"`
	// Size of the event stream in bytes; the oldest events go first.
	MaxBytes int64 `desc:"Size of the event stream in bytes, the oldest events go first; 0: unlimited." json:"max_bytes" schemapb:"default=0;gte=0"`
	// Replicas of both streams (JetStream cluster).
	Replicas int64 `desc:"Replicas of the event and dead-letter streams (JetStream cluster)." json:"replicas" schemapb:"default=1;gte=1;lte=5"`
	// Window in which a repeated publish (same ce-id) is dropped; at most
	// max_age.
	DedupWindow time.Duration `desc:"Window in which a repeated publish (same ce-id) is dropped; at most max_age." json:"dedup_window" schemapb:"default=2m"`
	// Retention of the dead letters of the service's reactors.
	DLQMaxAge time.Duration `desc:"Retention of the dead letters of the service's reactors; 0: unlimited." json:"dlq_max_age" schemapb:"default=720h"`
}

// Enabled reports whether NATS is configured.
func (n NATS) Enabled() bool { return n.URL != "" }

// Temporal connection. Field is `ns`: `namespace` is a CEL reserved word.
type Temporal struct {
	Addr      string `desc:"Frontend host:port; empty: without Temporal." json:"addr,omitempty"`
	Namespace string `desc:"Temporal namespace."                          json:"ns"             schemapb:"default=default"`
	TLS       TLS    `desc:"TLS of the connection."                       json:"tls"`
	// Temporal Cloud API key.
	APIKey Secret `desc:"Temporal Cloud API key." json:"api_key,omitempty"`
	// First dial attempt; later attempts run in the background.
	DialTimeout time.Duration `desc:"Bound of the first dial; later attempts run in the background." json:"dial_timeout" schemapb:"default=2s"`
	// Deadline of a hook call whose context and declaration set none.
	HookTimeout time.Duration `desc:"Deadline of a hook call whose context and declaration set none." json:"hook_timeout" schemapb:"default=30s"`
	Worker      Worker        `desc:"Worker on the service's task queue."                             json:"worker"`
}

// Worker on the service's task queue: an operational setting, the same
// code runs with any of it. A zero limit is Temporal's default.
type Worker struct {
	// false: this replica runs no worker (activities and the author's
	// workflows are served by replicas that do); it still raises hooks
	// (their own worker runs on every replica) and
	// uses the client.
	Enabled bool `desc:"Run a worker on this replica; off: activities and workflows are served by replicas that do, hooks are still raised." json:"enabled" schemapb:"default=true"`
	// Activities executing at once.
	MaxConcurrentActivities int64 `desc:"Activities executing at once; 0: Temporal's default." json:"max_concurrent_activities" schemapb:"default=0;gte=0"`
	// Workflow tasks executing at once.
	MaxConcurrentWorkflowTasks int64 `desc:"Workflow tasks executing at once; 0: Temporal's default." json:"max_concurrent_workflow_tasks" schemapb:"default=0;gte=0"`
	// Pollers of the activity and workflow task queues.
	ActivityPollers int64 `desc:"Pollers of the activity task queue; 0: Temporal's default." json:"activity_pollers" schemapb:"default=0;gte=0"`
	WorkflowPollers int64 `desc:"Pollers of the workflow task queue; 0: Temporal's default." json:"workflow_pollers" schemapb:"default=0;gte=0"`
}

// Enabled reports whether Temporal is configured.
func (t Temporal) Enabled() bool { return t.Addr != "" }

// Hooks: how this service's hook calls reach backplane.
type Hooks struct {
	// Transport of hook calls: "temporal" (Nexus through the configured
	// Temporal), "direct" (gRPC to backplane's platform port, its replicas
	// found through Consul), or "auto": temporal when Temporal is
	// configured, else direct when Consul is.
	Transport string `desc:"Transport of hook calls: temporal (Nexus), direct (gRPC to backplane found through Consul) or auto (temporal when configured, else direct)." json:"transport" schemapb:"default=auto"`
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
	Timeout time.Duration `desc:"Whole stop budget: drain + listeners + reserve at most." json:"timeout" schemapb:"default=25s"`
	// Time for load balancers to notice the instance left before its
	// listeners close.
	Drain time.Duration `desc:"Time for load balancers to notice the instance left before its listeners close." json:"drain" schemapb:"default=3s"`
	// Longest the listeners drain; then open streams are cut.
	Listeners time.Duration `desc:"Longest the listeners drain; then open streams are cut." json:"listeners" schemapb:"default=10s"`
	// Kept for the author's tree, dependencies and telemetry after the
	// listeners: drain + listeners + reserve <= timeout.
	Reserve time.Duration `desc:"Kept for the service's components, dependencies and telemetry after the listeners." json:"reserve" schemapb:"default=5s"`
}
