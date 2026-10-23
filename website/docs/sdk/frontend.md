# Frontend packages

All public packages use the `@backplane-labs` scope and GitHub Packages. They share a coordinated version. Install only the layers your application needs, keeping context-bearing runtime versions compatible with the host.

| Package | Responsibility |
| --- | --- |
| `@backplane-labs/backplane-api` | Generated protobuf messages and ws-proto clients for the complete console API |
| `@backplane-labs/backplane-client` | Cookie login, connection state, RPC transport and protected asset reads |
| `@backplane-labs/backplane-react` | Providers, `useClient`, connection and snapshot-watch hooks |
| `@backplane-labs/backplane-theme` | Semantic tokens, light/dark theme and Grafana theme bridge |
| `@backplane-labs/backplane-ui` | Primitive controls, tables, trees, layout and common compositions |
| `@backplane-labs/backplane-schema-forms` | Schema-driven editing with schemapb defaults, validation and masking (`SchemaForm`), and schema-driven tables (`SchemaTable`) |
| `@backplane-labs/backplane-editors` | Lazy code/structured editors and language integrations |
| `@backplane-labs/backplane-charts` | Charts, graphs and exact-value representations |
| `@backplane-labs/backplane-observability-ui` | Reusable logs, metrics and trace views |
| `@backplane-labs/backplane-platform-ui` | Catalog, configuration, wiring, runs, API reference and investigation compositions |
| `@backplane-labs/backplane-plugin-sdk` | Module routes, context, navigation, host contract and error reporting |
| `@backplane-labs/backplane-plugin-build` | Vite/Module Federation build contract and shared dependency policy |
| `@backplane-labs/backplane-errors` | Browser exception capture, state/breadcrumbs and OTLP delivery |

## Install from GitHub Packages

Configure the registry as in [quickstart](../quickstart.md), then install matching versions:

```bash
yarn add @backplane-labs/backplane-api@0.1.1 \
  @backplane-labs/backplane-client@0.1.1 \
  @backplane-labs/backplane-react@0.1.1
```

The [module template](../reference/module-template.md) contains the complete dependency and build setup for an embedded author UI. It is preferable to reconstructing the Module Federation configuration by hand.

## One client per host

```ts
import { create } from '@bufbuild/protobuf';
import { BackplaneClient } from '@backplane-labs/backplane-client';
import { CatalogServiceClient, ListServicesRequestSchema } from '@backplane-labs/backplane-api';

const runtime = new BackplaneClient({ baseURL: `${location.origin}/backplane/` });
await runtime.start();
// A login screen calls runtime.login(operatorToken) when anonymous.
const catalog = runtime.client(CatalogServiceClient);
const result = await catalog.listServices(create(ListServicesRequestSchema));
// The host calls runtime.dispose() on teardown.
```

The host wraps its content in `BackplaneProvider client={runtime}`. A page calls `useClient(CatalogServiceClient)`; it must not create another connection or dispose the host's client. Abort requests when a page unmounts or its selection changes.

`useSnapshotWatch` is for full-snapshot catalog/configuration/wiring streams. Reconnection replaces the snapshot; it is not durable event replay. Audit and Errors use paginated queries and refresh. Mutations are never automatically replayed after an uncertain network outcome.

## Tables from a schema

`SchemaTable` lays out the rows of a list method from the schemapb `Schema` of
one item: a column per field, cells formatted by kind — a choice by its label
(a proto enum by its value name), 64-bit integers without rounding, 16 bytes
as a uuid and other bytes as hex, timestamps as dates, secrets hidden. Choose
and order the columns, add derived ones, put actions in each row; the viewer
can still hide, reorder, pin, sort and filter them. `useCursorPages` follows
a method paginated by cursor (`next_cursor` / `has_more`) and appends pages:

```tsx
const pages = useCursorPages(async (cursor, signal) => {
  const res = await client.listDevices({ cursor, limit: 50 }, { signal });
  return { items: res.devices.map((d) => toNative(d)), nextCursor: res.nextCursor, hasMore: res.hasMore };
}, [userId]);

<SchemaTable schema={deviceSchema} rows={pages.rows} label="Devices"
  columns={['name', 'status', { id: 'os', label: 'OS', value: (row) => String(row.os ?? '') }, 'seen_at']}
  actions={(row) => <Button onClick={() => revoke(row.id)}>Revoke</Button>}
  more={{ hasMore: pages.hasMore, loading: pages.loading, onMore: pages.more }} />
```

Rows are native schemapb values, as `SchemaForm` takes them. Without
`columns` every field with a cell of its own is shown in schema order;
structured fields (messages, lists, maps) are shown through a derived column.

## Styling and translations

Import `@backplane-labs/backplane-theme/style.css` and `@backplane-labs/backplane-ui/style.css` once in the owning shell. Embedded modules inherit them. Use semantic CSS variables and shared components, including `SelectControl` for ordinary dropdowns. Keep English strings in a module namespace such as `module.hello`.

Grafana types and assets stay behind adapters. The kit does not promise compatibility with arbitrary Grafana application components. Heavy editors and charts should be lazy-loaded; a small page should not eagerly import an entire workbench.

See [UI kit](../reference/ui-kit.md), [component matrix](../reference/components.md), [forms](../reference/schema-forms.md), [editors](../reference/editors.md) and [charts](../reference/charts.md) for concrete component APIs and examples.
