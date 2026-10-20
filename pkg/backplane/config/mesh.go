package config

// Mesh is mutual TLS between services (api.MTLS, Root.ServiceCredentials):
// BACKPLANE_MESH_*. Certificates and who may call whom come from the
// installed driver (Consul's CA and intentions).
type Mesh struct {
	// What a caller no intention names gets: allow (Consul's own default
	// without ACLs) or deny.
	Default string `json:"default" schemapb:"default=allow"`
}

// Mesh defaults.
const (
	MeshAllow = "allow"
	MeshDeny  = "deny"
)
