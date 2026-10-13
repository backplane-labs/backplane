# Publish service API documentation

The service API tab renders external APIs declared in a service's API metadata. It supports OpenAPI, protobuf descriptors and GraphQL introspection, including request/response fields, schemas, enums and authored examples. It does not execute requests.

## Multi-file OpenAPI with embed.FS

Keep the contract next to its handler:

```text
internal/web/api/
  openapi.yaml
  schemas/greeting.yaml
```

The root can reference another file:

```yaml
openapi: 3.0.3
info:
  title: Greeting API
  version: 1.0.0
paths:
  /hello/:
    get:
      operationId: greet
      responses:
        '200':
          description: Greeting
          content:
            application/json:
              schema:
                $ref: './schemas/greeting.yaml'
```

`schemas/greeting.yaml`:

```yaml
type: object
required: [text]
properties:
  text:
    type: string
    description: The greeting delivered to the caller.
    example: Hello, Ann!
```

Embed and register the directory:

```go
//go:embed api
var apiFiles embed.FS

svc.HTTP("/hello/", handler,
    api.OpenAPIFS(apiFiles, "api/openapi.yaml"))
```

Use `fs.Sub` to limit the filesystem to the contract directory if appropriate. The SDK snapshots regular files, hashes paths and contents and announces a bundle reference. Files stay out of the 512 KiB manifest. Limits are 256 files and 32 MiB total; invalid entry paths and symlinks fail declaration/startup.

Relative references must remain inside the bundle. HTTP URLs, absolute paths and escaping paths are rejected. Recursive references stay references. The source preview/download is the resolved self-contained JSON, while the bundled source files remain the delivery unit.

## Protobuf and GraphQL

Managed gRPC/ws-proto registrations collect descriptors and transitive imports automatically. The viewer shows only services announced by the selected api. For an independently served gRPC endpoint, supply `api.Descriptors(fds)` with a binary descriptor set including imports. Include source info when generating it to preserve comments:

```bash
protoc --include_imports --include_source_info \
  --descriptor_set_out=api.pb path/to/service.proto
```

GraphQL accepts introspection JSON as `{"__schema": ...}` or `{"data":{"__schema": ...}}`. For a large file, use `api.GraphQLFS(files, "schema.json")` and a nil inline document. The viewer lists query/mutation/subscription fields, argument types and named definitions.

## Delivery and security

Service files are served on the internal platform listener under their content hash. Backplane checks the session, selects an instance whose version declares that hash and validates the response. The browser never fetches arbitrary external schema URLs. A bad document affects its route rather than breaking every service API page.

## Make the reference useful

Document actual status codes, auth requirements, examples, field descriptions and constraints. OpenAPI `examples` and `x-codeSamples` become useful reader-facing content. Protobuf comments and GraphQL descriptions are equally important. Test imported types and recursive references, not only the operations list.

The viewer expands small type sets automatically and provides search/expand controls for larger ones. Missing schemas are shown explicitly. See [detailed API viewer contract](../reference/platform-ui.md#publishing-external-api-documents) and the hello/formatter source examples.
