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

Internal RPCs live on the platform port and are restricted to the service's
`<service>.console.v1` protobuf package. The console's authenticated WebSocket relay
calls them directly; UI bundles and API files are fetched directly from that port.
The public API is excluded from this relay.

## API documents

HTTP APIs accept OpenAPI, gRPC/ws-proto use descriptor sets, and GraphQL uses
introspection JSON. Multi-file documents can be embedded as a filesystem.
[Publishing service API documents](service-api.md) covers packaging, limits and reference resolution.

The declared path and port describe the service listener. External routing may rewrite
paths or select another hostname; the platform does not infer that configuration.
