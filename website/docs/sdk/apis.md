# Service APIs and internal APIs

The SDK serves APIs on the service's own listeners and describes them in
`Manifest.apis`. Descriptions feed the console API viewer. External publication,
proxy configuration and protocol conversion belong to application code and deployment.
Registering an API never publishes it through a gateway.

## Managed listeners

```go
import (
    "github.com/backplane-labs/backplane/pkg/backplane/api"
    "github.com/backplane-labs/backplane/pkg/backplane/wsproto"
)

svc.GRPC(st.Greeter.Register, api.Reflection())
wsproto.Serve(svc, "/ws/", api.Origins{}, st.Greeter.Register)
svc.HTTP("/hello/", handler, api.OpenAPIFS(apiFiles, "openapi.yaml"))
svc.GraphQL("/graphql/", graphqlHandler, introspection)
```

These methods serve native gRPC, ws-proto, HTTP and the author's GraphQL handler.
They do not create Connect, gRPC-Web or REST/JSON conversion around gRPC.
If those protocols are needed, the author implements them or configures them in deployment.

`api.Listen(":8082")` selects a separate managed listener. `api.Host("api.example.com")`
restricts managed HTTP/GraphQL/ws-proto handler selection by Host. Empty Host accepts
any host; exact names and wildcard suffixes are supported. HTTP prefixes are normalized
to end in `/`; `/api` redirects to `/api/`. Duplicate host/prefix pairs on one listener
are rejected before startup.

`api.Middleware(...)`, `api.Interceptors(...)` and `api.StreamInterceptors(...)`
run in the service, scoped to the registration. `api.Reflection()` enables native
gRPC reflection on that listener. Shared listener limits are configured with
`BACKPLANE_SERVER_*`, including HTTP header/idle limits and the gRPC receive limit.
Request timeouts, body bounds, browser CORS and other application behavior belong to
handlers, middleware or the external proxy. There are no declarative proxy policy options.

## Calls between services: mutual TLS

An API only other services call (a domain's admin API, a sync service)
should not answer whoever reaches its port. `api.MTLS()` serves the route's
port to services of the mesh only, and `CallerOf` says which one called:

```go
// The server: only services may call, and the call knows who.
svc.GRPC(func(r grpc.ServiceRegistrar) { syncv1.RegisterSyncAdminServiceServer(r, admin) },
	api.Listen(":9090"), api.MTLS())

func (a admin) Purge(ctx context.Context, in *syncv1.PurgeRequest) (*syncv1.PurgeResponse, error) {
	caller, _ := backplane.CallerOf(ctx) // caller.Service == "feed"
	...
}

// A client, in its newState: its own certificate, the server verified as "sync".
conn, err := grpc.NewClient("sync:9090", grpc.WithTransportCredentials(root.ServiceCredentials("sync")))
// HTTP: &http.Transport{TLSClientConfig: root.ServiceTLS("sync")}
```

Nothing is configured for it beyond Consul, which the Consul driver already
uses:

- **Certificates** come from Consul's CA: each service gets a certificate
  naming it (`spiffe://<trust domain>/ns/default/dc/<dc>/svc/<service>`),
  issued and renewed by the local agent before it expires and when the CA's
  root rotates. The SDK follows them with blocking queries and reads them on
  every TLS handshake: a renewed certificate applies to the next connection
  without a restart; open connections keep theirs. Private keys never pass
  through the KV store. A development Consul (`consul agent -dev`) is a CA
  already.
- **Who may call** is Consul's intentions with the service as destination,
  followed live and decided by the SDK (an exact source before `*`):

  ```sh
  consul config write - <<'EOF'
  Kind = "service-intentions"
  Name = "sync"
  Sources = [
    { Name = "feed", Action = "allow" },
    { Name = "*",    Action = "deny" },
  ]
  EOF
  ```

  A caller no intention names gets `BACKPLANE_MESH_DEFAULT`: `allow`
  (Consul's own default without ACLs) or `deny`. A refused call is
  `PermissionDenied` (gRPC) or 403 (HTTP); a client without a certificate of
  the mesh does not get past the TLS handshake.
- **A port is mTLS or not as a whole**: every route on it says the same, or
  Run fails; so does `api.MTLS()` without the Consul driver and Consul.
  The SDK follows the CA only once something uses it, so services without
  mTLS ask Consul for nothing. Readiness waits for the certificate.

The mesh spans one trust domain, namespace and partition: those of the
service's own certificate. A caller presenting a certificate of the same CA
for another namespace or partition (Consul Enterprise) does not get past the
handshake, and intentions naming sources elsewhere do not apply; datacenters
of the trust domain are one mesh, as Consul's intentions are.

In production protect the identity with Consul ACLs: whoever can get a
certificate for `feed` (a token with `service:write` on it) is `feed`.

## Independently served APIs

Describe an API that your own server already serves:

```go
svc.API(api.HTTP("/legacy/",
    api.Port(8083),
    api.OpenAPI(document)))
```

This only adds catalog metadata and optional schema files. It does not start a
listener, check reachability, create routes, or choose a public URL. `api.Port(0)`
means the primary public port recorded in Consul; specify the port when the author
owns a separate listener. `api.GRPC`, `api.GraphQL` and `api.WSProto` provide the
corresponding declarations.

## Internal module API

```go
svc.Internal(func(r grpc.ServiceRegistrar) {
    helloconsolev1.RegisterAdminServiceServer(r, st.Admin)
})
svc.UI(bundle)
```

Internal RPCs live on the platform port, in any protobuf package except `grpc.*`
and the platform's own `backplane.*`. The console's authenticated WebSocket
relay sends each method to the service whose manifest declares it (a method two
services declare is refused) and calls it directly; UI bundles and API files are fetched directly from that port.
The public API is excluded from this relay.

`api.Interceptors` and `api.StreamInterceptors` give the internal API its own
middleware, as on `svc.GRPC`: they wrap only the services of that
`Internal` call, the first outermost, and run after the internal secret is
checked, so `backplane.OperatorOf` already answers in them.

```go
svc.Internal(func(r grpc.ServiceRegistrar) {
    helloconsolev1.RegisterAdminServiceServer(r, st.Admin)
}, api.Interceptors(audit, translateErrors))
```

### The console operator

A relayed call names the operator's console session, and
`backplane.OperatorOf(ctx)` returns it as `Operator{Session}`;
`Operator.Actor()` is `console:<session>`, the actor backplane's own audit
uses (see [application audit](../reference/audit.md#application-audit)). The
console signs in with the installation's admin token, so the session is all
there is to know of the person; the console's session menu lists each one's
address and user agent. Calls the console did not relay for a session
(backplane's own activity calls) have no operator.

The session is backplane's word, not the browser's: the relay drops every
`bp-*` key the browser sends and sets the session itself, and the platform port
admits only calls presenting the internal secret. That holds while
`BACKPLANE_INTERNAL_SECRET` is set and the platform port is unreachable to
anyone but backplane. Without a secret the port is open, and the session is
whatever the caller claims.

## API documents

HTTP APIs accept OpenAPI, gRPC/ws-proto use descriptor sets, and GraphQL uses
introspection JSON. Multi-file documents can be embedded as a filesystem.
[Publishing service API documents](service-api.md) covers packaging, limits and reference resolution.

The declared path and port describe the service listener. External routing may rewrite
paths or select another hostname; the platform does not infer that configuration.
