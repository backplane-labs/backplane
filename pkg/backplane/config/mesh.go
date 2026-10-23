package config

// Mesh is mutual TLS between services (api.MTLS, Root.ServiceCredentials,
// the platform port): BACKPLANE_MESH_*. Certificates and who may call whom
// come from the installed driver (Consul's CA and intentions).
type Mesh struct {
	// What a caller no intention names gets: allow (Consul's own default
	// without ACLs) or deny.
	Default string `desc:"What a caller no intention names gets: allow or deny." json:"default" schemapb:"default=allow"`
	// Platform is mutual TLS on the platform port and on the calls to the
	// platform ports of others (activities, hooks, the console relay):
	// plain, accept (served, not used: the step of a rollout) or mtls.
	Platform string `desc:"Mutual TLS on the platform port: plain; accept (served, calls stay plain: roll it out everywhere first); mtls (calls use it, plain serves only probes)." json:"platform" schemapb:"default=plain"`
}

// Mesh defaults.
const (
	MeshAllow = "allow"
	MeshDeny  = "deny"
)

// Platform port modes (Mesh.Platform).
const (
	// PlatformPlain: no TLS, as without a mesh.
	PlatformPlain = "plain"
	// PlatformAccept: mutual TLS is served next to plain connections; the
	// service still calls others plain. Every service of the installation
	// gets here before any goes to mtls.
	PlatformAccept = "accept"
	// PlatformMTLS: the service calls others over mutual TLS; plain
	// connections reach only the probes.
	PlatformMTLS = "mtls"
)
