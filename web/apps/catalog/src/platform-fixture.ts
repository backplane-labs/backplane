import * as api from '@backplane-labs/backplane-api';
import { SchemaSchema } from '@backplane-labs/backplane-api/schemapb/schema_pb';
import {
  type ClientConstructor,
  type ClientRuntime,
  type ClientState,
  WsStatusError,
} from '@backplane-labs/backplane-client';
import { create, fromJson, type JsonValue, toJson } from '@bufbuild/protobuf';
import { timestampFromDate, ValueSchema } from '@bufbuild/protobuf/wkt';
import { fixtureClient } from './fixture-client';

const schema = create(SchemaSchema, {
  id: { name: 'input' },
  fields: [
    { name: 'name', title: 'Recipient', kind: { case: 'string', value: { default: 'World', minLen: 1n } } },
    { name: 'sequence', title: 'Sequence', kind: { case: 'uint64', value: { default: 18446744073709551615n } } },
  ],
});
const source = { name: 'hello', latestVersion: '1.0.0', instances: 1, healthy: 1, health: api.ServiceHealth.HEALTHY };
const traceId = '1234567890abcdef1234567890abcdef';
const bindingDefinition = () =>
  fromJson(api.BindingDefinitionSchema, {
    hook: 'hello.Greet',
    description: 'Greets through the formatter.',
    steps: { format: { activity: 'formatter.Format', input: { name: 'req.name' } } },
    result: { text: 'format.text' },
  });
/** A binding with a for-each step whose body is two steps: one greeting per person. */
const batchDefinition = () =>
  fromJson(api.BindingDefinitionSchema, {
    hook: 'hello.Batch',
    steps: {
      each: {
        forEach: 'req.people',
        as: 'person',
        concurrency: 4,
        onError: 'continue',
        steps: {
          format: { activity: 'formatter.Format', input: { name: 'person.name' } },
          record: { activity: 'formatter.Record', input: { name: 'person.name', text: 'format.text' } },
        },
        result: 'format.text',
      },
    },
  });
const ruleDefinition = () =>
  fromJson(api.RuleDefinitionSchema, {
    event: 'hello.Greeted',
    when: 'event.name != "skip"',
    steps: { record: { activity: 'formatter.Record', input: { name: 'event.name', text: 'event.text' } } },
  });
const field = (name: string, required = true) => ({ name, required, kind: { case: 'string' as const, value: {} } });
const textSchema = (...names: string[]) => create(SchemaSchema, { fields: names.map((name) => field(name)) });
/** The installation the wiring editor sees: hello calls Greet and publishes Greeted; formatter formats and records. */
const wiringCatalog = () =>
  create(api.GetWiringCatalogResponseSchema, {
    services: [
      {
        service: 'formatter',
        version: '1.0.0',
        activities: [
          { name: 'Format', input: textSchema('name'), output: textSchema('text'), description: 'Formats a greeting' },
          { name: 'Record', input: textSchema('name', 'text'), description: 'Records a greeting' },
        ],
      },
      {
        service: 'hello',
        version: '1.0.0',
        hooks: [
          { name: 'Greet', required: true, input: textSchema('name'), output: textSchema('text') },
          { name: 'Farewell', input: textSchema('name'), output: textSchema('text') },
          {
            name: 'Batch',
            input: create(SchemaSchema, {
              fields: [
                {
                  name: 'people',
                  required: true,
                  kind: {
                    case: 'list',
                    value: { items: [{ kind: { case: 'object', value: { schema: textSchema('name') } } }] },
                  },
                },
              ],
            }),
          },
        ],
        events: [{ name: 'Greeted', schema: textSchema('name', 'text') }],
      },
    ],
  });

const appTime = (minutes: number) => timestampFromDate(new Date(Date.UTC(2026, 8, 30, 12, minutes)));
/** The audit feed as backplane keeps it: a platform entry and two records an identity service marked backplane.audit. */
const auditRecords = () => [
  create(api.AuditRecordSchema, {
    id: 'app-2',
    source: api.AuditSource.APPLICATION,
    time: appTime(5),
    receivedAt: appTime(5),
    service: 'iam',
    action: 'identity.deleted',
    actor: 'user:7',
    subject: 'identity/42',
    outcome: 'succeeded',
    severity: 'INFO',
    message: 'identity deleted',
    attributes: { 'backplane.audit': true, tenant: 'acme' },
    resource: { 'service.name': 'iam' },
    traceId: '0102030405060708090a0b0c0d0e0f10',
  }),
  create(api.AuditRecordSchema, {
    id: 'app-1',
    source: api.AuditSource.APPLICATION,
    time: appTime(2),
    receivedAt: appTime(3),
    service: 'iam',
    action: 'login.failed',
    actor: 'user:9',
    subject: 'identity/7',
    outcome: 'failed',
    severity: 'WARN',
    message: 'password mismatch',
    attributes: { 'backplane.audit': true, tenant: 'globex' },
    resource: { 'service.name': 'iam' },
  }),
  create(api.AuditRecordSchema, {
    id: 'platform-1',
    source: api.AuditSource.PLATFORM,
    time: appTime(0),
    receivedAt: appTime(0),
    service: 'hello',
    action: 'config.save',
    actor: 'console:1b7e2c9a-0000-4000-8000-000000000000',
    subject: 'hello',
    outcome: 'succeeded',
    operationId: 'op-1',
    sequence: 9007199254740993n,
    attributes: { revision: 3, keys: ['greeter.suffix'] },
  }),
];
/** Errors as the error store holds them: a browser SDK capture and a Go service's exception, each its own group. */
const fingerprintOf = (service: string) =>
  service === 'shop-web' ? '0f1e2d3c4b5a69788796a5b4c3d2e1f0' : 'a1b2c3d4e5f60718293a4b5c6d7e8f90';
const errorOccurrences = () => [
  create(api.ErrorOccurrenceSchema, {
    ref: 'err-web',
    origin: api.ErrorOrigin.SDK,
    eventId: 'd45238c7-4ff0-4e5f-819b-f8d0f4c3c143',
    time: appTime(4),
    receivedAt: appTime(4),
    service: 'shop-web',
    environment: 'production',
    release: '1.4.2',
    type: 'TypeError',
    message: "Cannot read properties of undefined (reading 'price')",
    severity: 17,
    fingerprint: fingerprintOf('shop-web'),
    ingest: 'proxy',
    traceId: '0102030405060708090a0b0c0d0e0f10',
    spanId: '0102030405060708',
    runtimeId: 'b35e19ae-b217-4f30-ab89-0a9084b7f144',
    mechanism: 'window.error',
    unhandled: true,
    tenant: 'inst-7',
  }),
  create(api.ErrorOccurrenceSchema, {
    ref: 'err-api',
    origin: api.ErrorOrigin.OTEL_LOG,
    time: appTime(3),
    receivedAt: appTime(3),
    service: 'billing',
    environment: 'production',
    release: '2.0.0',
    type: '*billing.DeclinedError',
    message: 'charge: card declined',
    severity: 17,
    fingerprint: fingerprintOf('billing'),
    ingest: 'trusted',
  }),
];
/** Triage states set in the story, by fingerprint. */
const groupStates = new Map<string, { state: api.ErrorGroupState; note: string }>();
const errorGroups = () =>
  errorOccurrences().map((occurrence) =>
    create(api.ErrorGroupSchema, {
      fingerprint: occurrence.fingerprint,
      service: occurrence.service,
      type: occurrence.type,
      message: occurrence.message,
      firstSeen: appTime(0),
      lastSeen: occurrence.time,
      count: 7n,
      stored: 7n,
      countInRange: 1n,
      trend: [0n, 0n, 0n, occurrence.service === 'billing' ? 1n : 0n, occurrence.service === 'shop-web' ? 1n : 0n, 0n],
      sampleRef: occurrence.ref,
      origin: occurrence.origin,
      unhandled: occurrence.unhandled,
      environments: [occurrence.environment],
      releases: [occurrence.release],
      new: occurrence.service === 'billing',
      state: groupStates.get(occurrence.fingerprint)?.state ?? api.ErrorGroupState.OPEN,
      stateNote: groupStates.get(occurrence.fingerprint)?.note ?? '',
      ...(groupStates.has(occurrence.fingerprint) ? { stateAt: appTime(5), stateBy: 'console:fixture' } : {}),
    }),
  );
const wantedServices = (filter?: api.ErrorFilter) =>
  (filter?.conditions ?? [])
    .filter((condition) => condition.field === api.ErrorField.SERVICE && condition.op === api.ErrorOperator.IS)
    .flatMap((condition) => condition.values);
const errorDetail = (ref: string) => {
  const occurrence = errorOccurrences().find((entry) => entry.ref === ref);
  if (!occurrence) throw new Error(`No fixture occurrence: ${ref}`);
  const sdk = occurrence.origin === api.ErrorOrigin.SDK;
  const stack = sdk
    ? "TypeError: Cannot read properties of undefined (reading 'price')\n    at total (https://shop.example/assets/cart.js:42:17)\n    at render (https://shop.example/assets/app.js:9:3)"
    : 'goroutine 7 [running]:\nbilling.(*Charger).Charge(0xc000010)\n\t/src/billing/charge.go:88 +0x1d\nbilling.Handle()\n\t/src/billing/http.go:31 +0x10';
  return create(api.GetErrorResponseSchema, {
    occurrence,
    stacktrace: stack,
    scope: sdk ? 'app-debug.browser@0.1.0' : 'backplane',
    group: errorGroups().find((group) => group.fingerprint === occurrence.fingerprint),
    fields: {
      'service.name': occurrence.service,
      'exception.type': occurrence.type,
      'service.instance.id': sdk ? '' : 'billing-1',
    },
    resource: {
      'service.name': occurrence.service,
      'deployment.environment.name': occurrence.environment,
      'service.version': occurrence.release,
    },
    attributes: { 'exception.type': occurrence.type },
    ...(sdk
      ? {
          envelopeStatus: 'available',
          exception: { type: occurrence.type, message: occurrence.message, stacktrace: stack },
          envelope: {
            state: { sources: [{ name: 'cart', value: { items: 2, currency: 'EUR' } }] },
            history: {
              items: [
                {
                  sequence: 1,
                  kind: 'breadcrumb',
                  name: 'fetch GET /api/cart',
                  timestampUnixNano: '1790000000000000000',
                  data: { status: 'ok', value: { status: 200 } },
                },
                {
                  sequence: 2,
                  kind: 'breadcrumb',
                  name: 'navigation /checkout',
                  timestampUnixNano: '1790000001000000000',
                },
              ],
            },
          },
        }
      : {}),
  });
};
/** The record's own text for a field condition; fields without fixture data (operation, severity, trace) never match. */
const fieldText = (record: api.AuditRecord, field: api.AuditField): string | undefined => {
  switch (field) {
    case api.AuditField.SOURCE:
      return record.source === api.AuditSource.PLATFORM ? 'platform' : 'application';
    case api.AuditField.SERVICE:
      return record.service;
    case api.AuditField.ACTION:
      return record.action;
    case api.AuditField.ACTOR:
      return record.actor;
    case api.AuditField.SUBJECT:
      return record.subject;
    case api.AuditField.OUTCOME:
      return record.outcome;
    default:
      return undefined;
  }
};
/** The value a condition's target reads: an application attribute or a field of the record itself. */
const auditValue = (record: api.AuditRecord, target: api.AuditCondition['target']): JsonValue | undefined =>
  target.case === 'attribute'
    ? record.attributes?.[target.value]
    : fieldText(record, target.value ?? api.AuditField.UNSPECIFIED);
/** The fixture's conditions: attribute or field equality, and text. */
const matches = (record: api.AuditRecord, filter?: api.AuditFilter) =>
  (filter?.conditions ?? []).every((condition) => {
    const values: JsonValue[] = condition.values.map((value) => toJson(ValueSchema, value));
    const actual = auditValue(record, condition.target);
    return condition.op === api.AuditOperator.IS_NOT
      ? !values.includes(actual ?? null)
      : values.includes(actual ?? null);
  }) &&
  (!filter?.text || JSON.stringify(record).toLowerCase().includes(filter.text.toLowerCase()));
type JsonObject = { [key: string]: JsonValue };
const isJsonObject = (value: JsonValue | undefined): value is JsonObject =>
  value !== undefined && typeof value === 'object' && !Array.isArray(value);
/** JSON.parse typed at the boundary: these strings came from JSON.stringify of JSON values. */
const parseJson = (text: string): JsonValue => JSON.parse(text);
/** A fixture analysis: every read of a variable in the definition's expressions (by pattern, not CEL), bodies included. */
function analyze(definition: JsonValue) {
  const references: { path: string; variable: string; fields: string[]; expr: { start: number; end: number } }[] = [];
  const steps: {
    name: string;
    parent: string;
    level: number;
    data: string[];
    after: string[];
    when: string[];
    itemType: string;
  }[] = [];
  const walk = (value: JsonValue | undefined, path: string, names: Set<string>) => {
    if (typeof value === 'string')
      for (const match of value.matchAll(/[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*/g)) {
        const [variable, ...fields] = match[0].split('.');
        if (variable === undefined) continue;
        if (
          variable === 'req' ||
          variable === 'event' ||
          variable === 'meta' ||
          variable === 'steps' ||
          names.has(variable)
        )
          references.push({
            path,
            variable,
            fields,
            expr: { start: match.index, end: match.index + match[0].length },
          });
      }
    else if (isJsonObject(value)) for (const [key, item] of Object.entries(value)) walk(item, `${path}/${key}`, names);
  };
  let missing = false;
  const stepsOf = (value: JsonValue | undefined): Record<string, JsonObject> => {
    const map: Record<string, JsonObject> = {};
    if (isJsonObject(value)) for (const [name, item] of Object.entries(value)) if (isJsonObject(item)) map[name] = item;
    return map;
  };
  const strings = (value: JsonValue | undefined): string[] =>
    Array.isArray(value) && value.every((item) => typeof item === 'string') ? value : [];
  const frame = (map: Record<string, JsonObject>, prefix: string, parent: string, outer: Set<string>) => {
    const names = new Set([...outer, ...Object.keys(map)]);
    for (const [name, step] of Object.entries(map).sort(([a], [b]) => a.localeCompare(b))) {
      const path = `${prefix}/steps/${name}`,
        nested = stepsOf(step.steps),
        body = Object.keys(nested).length > 0;
      const as = typeof step.as === 'string' ? step.as : undefined;
      const inner = step.forEach ? new Set([...names, as ?? 'item']) : names;
      if (!step.activity && !body) missing = true;
      // Levels by reads of siblings: enough for the fixture's layout.
      const data = Object.keys(map).filter(
        (other) => other !== name && JSON.stringify(step.input ?? '').includes(`${other}.`),
      );
      steps.push({
        name,
        parent,
        level: data.length ? 1 : 0,
        data,
        after: strings(step.after),
        when: [],
        itemType: step.forEach ? 'object' : '',
      });
      if (step.forEach) walk(step.forEach, `${path}/forEach`, names);
      walk(step.input, `${path}/input`, inner);
      if (step.when) walk(step.when, `${path}/when`, inner);
      if (body) {
        frame(nested, path, parent ? `${parent}/${name}` : name, inner);
        walk(step.result, `${path}/result`, new Set([...inner, ...Object.keys(nested)]));
      }
    }
  };
  const root = isJsonObject(definition) ? definition : {};
  frame(stepsOf(root.steps), '', '', new Set());
  walk(root.result, '/result', new Set(Object.keys(stepsOf(root.steps))));
  const violations = missing
    ? [{ path: '/steps', code: 'UNKNOWN_ACTIVITY', message: 'Every step needs an activity' }]
    : [];
  return { violations, references, steps };
}
function untilAbort(signal?: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (!signal || signal.aborted) resolve();
    else signal.addEventListener('abort', () => resolve(), { once: true });
  });
}

/** Explicit catalog-only transport. Missing methods fail; it never contacts a platform. */
export class PlatformFixture implements ClientRuntime {
  private state: ClientState = {
    connection: 'connected',
    session: { id: 'fixture', created_at: '', expires_at: '', idle_timeout_seconds: 3600 },
    connectionId: 1,
  };
  private listeners = new Set<() => void>();
  failWrites = false;
  gap = false;
  watchOpens = 0;
  cursors: string[] = [];
  writes = 0;
  lastInput = '';
  active = 0;
  revision = create(api.RevisionSchema, {
    service: 'hello',
    revision: 1n,
    values: { 'greeter.suffix': '"!"' },
    author: 'fixture',
    comment: 'Initial',
  });
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };
  reconnect() {
    this.state = { ...this.state, connectionId: this.state.connectionId + 1 };
    this.listeners.forEach((listener) => {
      listener();
    });
  }
  logout() {
    this.state = { connection: 'anonymous', session: null, connectionId: this.state.connectionId };
    this.listeners.forEach((listener) => {
      listener();
    });
  }
  client<T extends object>(_constructor: ClientConstructor<T>): T {
    const fixture = this;
    // As a Go service reports it: durations as integer nanoseconds at any depth, secrets masked.
    const effective = {
      greeter: {
        suffix: '!',
        limits: { max: 5, window: 30_000_000_000 },
        port: 8080,
        timeout: 5_000_000_000,
        token: '***',
      },
    };
    const configSchema = create(SchemaSchema, {
      fields: [
        {
          name: 'greeter',
          kind: {
            case: 'object',
            value: {
              schema: {
                fields: [
                  { name: 'suffix', kind: { case: 'json', value: {} } },
                  {
                    name: 'limits',
                    title: 'Limits',
                    kind: {
                      case: 'object',
                      value: {
                        schema: {
                          fields: [
                            { name: 'max', title: 'Max', kind: { case: 'int64', value: { default: 5n } } },
                            { name: 'window', title: 'Window', kind: { case: 'duration', value: {} } },
                          ],
                        },
                      },
                    },
                  },
                  {
                    name: 'port',
                    title: 'Port',
                    description: 'Listener of the greeter.',
                    kind: { case: 'int64', value: { default: 8080n } },
                  },
                  { name: 'timeout', title: 'Timeout', kind: { case: 'duration', value: {} } },
                  { name: 'token', title: 'Token', secret: true, kind: { case: 'string', value: {} } },
                ],
              },
            },
          },
        },
      ],
    });
    const config = () =>
      create(api.ServiceConfigSchema, {
        service: 'hello',
        schema: configSchema,
        live: ['greeter.suffix', 'greeter.limits'],
        current: fixture.revision,
        instances: [
          {
            id: 'hello-1',
            version: '1.0.0',
            appliedRevision: fixture.revision.revision,
            config: new TextEncoder().encode(JSON.stringify(effective)),
            live: [
              { path: 'greeter.suffix', value: fixture.revision.values['greeter.suffix'] },
              { path: 'greeter.limits', value: JSON.stringify(effective.greeter.limits) },
            ],
          },
        ],
      });
    const snapshot = async function* <R>(value: R, signal?: AbortSignal) {
      fixture.active++;
      try {
        yield value;
        await untilAbort(signal);
      } finally {
        fixture.active--;
      }
    };
    const beforeWrite = () => {
      fixture.writes++;
      if (fixture.failWrites) throw new WsStatusError(14, 'Fixture outage');
    };
    // Runs the body synchronously (stories assert fixture.writes right after a click) and still
    // returns a promise with async rejection semantics, like the generated client's methods.
    const reply = <R>(run: () => R): Promise<R> => {
      try {
        return Promise.resolve(run());
      } catch (error) {
        return Promise.reject(error);
      }
    };
    const methods = {
      getService() {
        return reply(() => {
          return create(api.GetServiceResponseSchema, {
            summary: source,
            latest: {
              service: 'hello',
              version: '1.0.0',
              hooks: [{ name: 'Greet', input: schema }],
              activities: [{ name: 'Format', input: schema }],
              events: [{ name: 'Greeted', schema }],
              workflows: [{ name: 'Welcome', input: schema }],
            },
            instances: [
              {
                id: 'hello-1',
                registered: true,
                healthy: false,
                state: {
                  id: 'hello-1',
                  version: '1.0.0',
                  publicPort: 8080,
                  listeners: { callbacks: 8090 },
                  nodes: [
                    { path: 'greeter', kind: 1 },
                    { path: 'greeter/database', kind: 2, ready: false, error: 'Waiting for database' },
                  ],
                  config: new TextEncoder().encode('{"token":"***"}'),
                  sources: { 'greeter.suffix': 4 },
                },
              },
            ],
          });
        });
      },
      watchCatalog(_request: api.WatchCatalogRequest, options: api.CallOptions) {
        return snapshot(create(api.WatchCatalogResponseSchema, { services: [source] }), options.signal);
      },
      watchConfig(_request: api.WatchConfigRequest, options: api.CallOptions) {
        return snapshot(create(api.WatchConfigResponseSchema, { config: config() }), options.signal);
      },
      listRevisions() {
        return reply(() => {
          return create(api.ListRevisionsResponseSchema, { revisions: [fixture.revision] });
        });
      },
      validateOverride(request: api.ValidateOverrideRequest) {
        return reply(() => {
          return create(api.ValidateOverrideResponseSchema, {
            violations:
              request.values['greeter.suffix'] === '"invalid"'
                ? [{ path: 'greeter.suffix', code: 'INVALID_VALUE', message: 'Invalid suffix' }]
                : [],
          });
        });
      },
      saveRevision(request: api.SaveRevisionRequest) {
        return reply(() => {
          beforeWrite();
          fixture.revision = create(api.RevisionSchema, {
            service: request.service,
            revision: fixture.revision.revision + 1n,
            values: request.values,
            comment: request.comment,
          });
          return create(api.SaveRevisionResponseSchema, { revision: fixture.revision });
        });
      },
      rollback() {
        return reply(() => {
          beforeWrite();
          return create(api.RollbackResponseSchema, { revision: fixture.revision });
        });
      },
      searchErrors(request: api.SearchErrorsRequest) {
        return reply(() => {
          const want = wantedServices(request.filter),
            prints = (request.filter?.conditions ?? [])
              .filter((condition) => condition.field === api.ErrorField.FINGERPRINT)
              .flatMap((condition) => condition.values);
          return create(api.SearchErrorsResponseSchema, {
            occurrences: errorOccurrences().filter(
              (occurrence) =>
                (!want.length || want.includes(occurrence.service)) &&
                (!prints.length || prints.includes(occurrence.fingerprint)),
            ),
          });
        });
      },
      searchErrorGroups(request: api.SearchErrorGroupsRequest) {
        return reply(() => {
          const want = wantedServices(request.filter),
            states = request.states.length ? request.states : [api.ErrorGroupState.OPEN];
          return create(api.SearchErrorGroupsResponseSchema, {
            stepMillis: 60_000n,
            groups: errorGroups().filter(
              (group) => (!want.length || want.includes(group.service)) && states.includes(group.state),
            ),
          });
        });
      },
      setErrorGroupState(request: api.SetErrorGroupStateRequest) {
        return reply(() => {
          beforeWrite();
          groupStates.set(request.fingerprint, { state: request.state, note: request.note });
          return create(api.SetErrorGroupStateResponseSchema, {
            group: errorGroups().find((group) => group.fingerprint === request.fingerprint),
          });
        });
      },
      getErrorGroup(request: api.GetErrorGroupRequest) {
        return reply(() => {
          const group = errorGroups().find(
            (entry) => entry.fingerprint === request.fingerprint && entry.service === request.service,
          );
          if (!group) throw new WsStatusError(5, 'error group not found');
          return create(api.GetErrorGroupResponseSchema, {
            group,
            stepMillis: 60_000n,
            buckets: [0, 1, 2, 3, 4, 5].map((minute) => ({ start: appTime(minute), count: group.trend[minute] ?? 0n })),
          });
        });
      },
      errorInfo() {
        return reply(() => {
          return create(api.ErrorInfoResponseSchema, {
            retention: { seconds: 7_776_000n },
            burstPerMinute: 60,
            burstPerDay: 2000,
            maxRecordBytes: 524_288n,
            relatedAvailable: true,
          });
        });
      },
      errorHistogram() {
        return reply(() => {
          return create(api.ErrorHistogramResponseSchema, {
            stepMillis: 60_000n,
            total: 2n,
            buckets: [0, 1, 2, 3, 4, 5].map((minute) => ({
              start: appTime(minute),
              count: minute === 3 || minute === 4 ? 1n : 0n,
            })),
          });
        });
      },
      errorFacets(request: api.ErrorFacetsRequest) {
        return reply(() => {
          return create(api.ErrorFacetsResponseSchema, {
            facets: request.fields.map((field) => ({
              field,
              values:
                field === api.ErrorField.SERVICE
                  ? [
                      { value: 'shop-web', count: 1n, lastSeen: appTime(4) },
                      { value: 'billing', count: 1n, lastSeen: appTime(3) },
                    ]
                  : field === api.ErrorField.TENANT
                    ? [{ value: 'inst-7', count: 1n, lastSeen: appTime(4) }]
                    : [],
            })),
          });
        });
      },
      getError(request: api.GetErrorRequest) {
        return reply(() => {
          return errorDetail(request.ref);
        });
      },
      relatedLogs() {
        return reply(() => {
          return create(api.RelatedLogsResponseSchema, {
            status: 'available',
            logs: [{ time: appTime(4), fields: { _msg: 'rendering cart', service: 'shop-web', severity: 'INFO' } }],
          });
        });
      },
      searchAudit(request: api.SearchAuditRequest) {
        return reply(() => {
          return create(api.SearchAuditResponseSchema, {
            records: auditRecords().filter((record) => matches(record, request.filter)),
          });
        });
      },
      auditHistogram(request: api.AuditHistogramRequest) {
        return reply(() => {
          const records = auditRecords().filter((record) => matches(record, request.filter));
          return create(api.AuditHistogramResponseSchema, {
            stepSeconds: 60n,
            buckets: [0, 1, 2, 3, 4, 5].map((minute) => {
              const inside = records.filter(
                (record) => Number(record.time?.seconds ?? 0) - Number(appTime(0).seconds) === minute * 60,
              );
              return {
                start: appTime(minute),
                platform: BigInt(inside.filter((record) => record.source === api.AuditSource.PLATFORM).length),
                application: BigInt(inside.filter((record) => record.source === api.AuditSource.APPLICATION).length),
                failed: BigInt(inside.filter((record) => record.outcome === 'failed').length),
              };
            }),
          });
        });
      },
      auditFields() {
        return reply(() => {
          return create(api.AuditFieldsResponseSchema, {
            fields: [
              { attribute: 'tenant', count: 2n },
              { attribute: 'revision', count: 1n },
              { attribute: 'backplane.audit', count: 2n },
            ],
          });
        });
      },
      auditFacets(request: api.AuditFacetsRequest) {
        return reply(() => {
          const records = auditRecords().filter((record) => matches(record, request.filter));
          return create(api.AuditFacetsResponseSchema, {
            facets: request.targets.map((target) => {
              const values = records
                .map((record) => auditValue(record, target.target))
                .filter((value) => value !== undefined && value !== '');
              const counts = new Map<string, number>();
              for (const value of values)
                counts.set(JSON.stringify(value), (counts.get(JSON.stringify(value)) ?? 0) + 1);
              return {
                target,
                total: BigInt(values.length),
                values: [...counts].map(([value, count]) => ({
                  value: fromJson(ValueSchema, parseJson(value)),
                  count: BigInt(count),
                })),
              };
            }),
          });
        });
      },
      getObsCapabilities() {
        return reply(() => {
          return create(api.GetObsCapabilitiesResponseSchema, {
            signals: [
              { signal: api.ObsSignal.LOGS, languages: [api.ObsLanguage.LOGSQL] },
              { signal: api.ObsSignal.METRICS, languages: [api.ObsLanguage.METRICSQL, api.ObsLanguage.PROMQL] },
              {
                signal: api.ObsSignal.TRACES,
                languages: [api.ObsLanguage.LOGSQL, api.ObsLanguage.TRACEQL],
                traceLookup: true,
              },
            ],
            maxLimit: 1000,
            maxPoints: 10000,
          });
        });
      },
      listObsSources() {
        return reply(() => {
          return create(api.ListObsSourcesResponseSchema, {
            sources: [{ resource: { 'service.name': 'hello' } }, { resource: { 'service.name': 'kratos' } }],
          });
        });
      },
      listObsFields() {
        return reply(() => {
          return create(api.ListObsFieldsResponseSchema, { fields: ['service.name', 'trace_id'] });
        });
      },
      queryObs(request: api.QueryObsRequest) {
        return reply(() => {
          if (request.signal === api.ObsSignal.METRICS)
            return create(api.QueryObsResponseSchema, {
              result: {
                case: 'timeSeries',
                value: {
                  series: [
                    {
                      labels: { service: 'hello' },
                      samples: [{ timestampSeconds: '1790683200.123456789', value: '18446744073709551615' }],
                    },
                  ],
                },
              },
            });
          if (request.signal === api.ObsSignal.TRACES)
            return create(api.QueryObsResponseSchema, {
              result: {
                case: 'traces',
                value: {
                  traces: [
                    {
                      traceId,
                      rootName: 'Greet',
                      rootService: 'hello',
                      startUnixNano: '1790683200123456789',
                      durationMillis: '1.000001',
                    },
                  ],
                },
              },
            });
          return create(api.QueryObsResponseSchema, {
            info: { partial: true, warnings: ['Fixture partial response'] },
            result: {
              case: 'rows',
              value: {
                rows: [
                  {
                    fields: {
                      _time: '2026-09-29T12:00:00.123456789Z',
                      _msg: 'Hello from storage',
                      trace_id: traceId,
                      sequence: '18446744073709551615',
                    },
                  },
                ],
              },
            },
          });
        });
      },
      getTrace() {
        return reply(() => {
          return create(api.GetTraceResponseSchema, {
            traceId,
            tempoJson: new TextEncoder().encode(
              '{"trace":{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"hello"}}]},"scopeSpans":[{"spans":[{"traceId":"EjRWeJCrze8SNFZ4kKvN7w==","spanId":"EjRWeJCrze8=","name":"Greet","startTimeUnixNano":1790683200123456789,"endTimeUnixNano":1790683200124456790}]}]}]},"extension":9007199254740993}',
            ),
          });
        });
      },
      callHook(request: api.CallHookRequest) {
        return reply(() => {
          beforeWrite();
          fixture.lastInput = request.input;
          return create(api.CallHookResponseSchema, {
            result: { output: request.input, workflowId: 'hook/hello/Greet/fixture', runId: 'run-1' },
          });
        });
      },
      runActivity(request: api.RunActivityRequest) {
        return reply(() => {
          beforeWrite();
          fixture.lastInput = request.input;
          return create(api.RunActivityResponseSchema, { result: { output: request.input } });
        });
      },
      publishTestEvent(request: api.PublishTestEventRequest) {
        return reply(() => {
          beforeWrite();
          fixture.lastInput = request.payload;
          return create(api.PublishTestEventResponseSchema, { id: 'event-1', seq: 9007199254740993n });
        });
      },
      startWorkflow(request: api.StartWorkflowRequest) {
        return reply(() => {
          beforeWrite();
          fixture.lastInput = request.input;
          if (request.workflowId === 'welcome-1') throw new WsStatusError(6, 'Already running');
          return create(api.StartWorkflowResponseSchema, {
            workflowId: request.workflowId || 'workflow-1',
            runId: 'run-1',
          });
        });
      },
      getBinding(request: api.GetBindingRequest) {
        return reply(() => {
          return create(api.GetBindingResponseSchema, {
            version: {
              hook: request.hook,
              version: 1n,
              author: 'fixture',
              definition:
                request.hook === 'hello.Batch' ? batchDefinition() : { ...bindingDefinition(), hook: request.hook },
            },
          });
        });
      },
      validateBinding(request: api.ValidateBindingRequest) {
        return reply(() => {
          return create(api.ValidateBindingResponseSchema, {
            violations: Object.keys(request.definition?.steps ?? {}).length
              ? []
              : [{ path: '/steps', message: 'At least one step is required' }],
          });
        });
      },
      getWiringCatalog() {
        return reply(() => {
          return wiringCatalog();
        });
      },
      analyzeBinding(request: api.AnalyzeBindingRequest) {
        return reply(() => {
          return create(api.AnalyzeBindingResponseSchema, {
            analysis: analyze(
              toJson(api.BindingDefinitionSchema, request.definition ?? create(api.BindingDefinitionSchema)),
            ),
          });
        });
      },
      analyzeRule(request: api.AnalyzeRuleRequest) {
        return reply(() => {
          return create(api.AnalyzeRuleResponseSchema, {
            analysis: analyze(toJson(api.RuleDefinitionSchema, request.definition ?? create(api.RuleDefinitionSchema))),
          });
        });
      },
      renameStep(request: api.RenameStepRequest) {
        return reply(() => {
          const rename = (value: JsonValue): JsonValue =>
            typeof value === 'string'
              ? value.replace(new RegExp(`\\b${request.from}\\b`, 'g'), request.to)
              : value && typeof value === 'object'
                ? Array.isArray(value)
                  ? value.map(rename)
                  : Object.fromEntries(
                      Object.entries(value).map(([key, item]) => [
                        key === request.from ? request.to : key,
                        rename(item),
                      ]),
                    )
                : value;
          if (request.definition.case === 'binding')
            return create(api.RenameStepResponseSchema, {
              definition: {
                case: 'binding',
                value: fromJson(
                  api.BindingDefinitionSchema,
                  rename(toJson(api.BindingDefinitionSchema, request.definition.value)),
                ),
              },
            });
          if (request.definition.case === 'rule')
            return create(api.RenameStepResponseSchema, {
              definition: {
                case: 'rule',
                value: fromJson(
                  api.RuleDefinitionSchema,
                  rename(toJson(api.RuleDefinitionSchema, request.definition.value)),
                ),
              },
            });
          return create(api.RenameStepResponseSchema);
        });
      },
      saveBinding(request: api.SaveBindingRequest) {
        return reply(() => {
          beforeWrite();
          return create(api.SaveBindingResponseSchema, {
            version: { version: 2n, hook: request.definition?.hook, definition: request.definition },
          });
        });
      },
      getRule(request: api.GetRuleRequest) {
        return reply(() => {
          return create(
            api.GetRuleResponseSchema,
            request.id
              ? {
                  rule: {
                    id: request.id,
                    state: api.RuleState.ACTIVE,
                    current: {
                      ruleId: request.id,
                      version: 1n,
                      name: 'Greeted rule',
                      author: 'fixture',
                      definition: ruleDefinition(),
                    },
                  },
                  version: {
                    ruleId: request.id,
                    version: 1n,
                    name: 'Greeted rule',
                    author: 'fixture',
                    definition: ruleDefinition(),
                  },
                }
              : {},
          );
        });
      },
      watchBindings(_request: api.WatchBindingsRequest, options: api.CallOptions) {
        return snapshot(
          create(api.WatchBindingsResponseSchema, {
            bindings: [
              {
                hook: 'hello.Greet',
                service: 'hello',
                declared: true,
                required: true,
                state: api.BindingState.BOUND,
                current: { hook: 'hello.Greet', version: 1n, definition: bindingDefinition() },
              },
              {
                hook: 'hello.Batch',
                service: 'hello',
                declared: true,
                state: api.BindingState.BOUND,
                current: { hook: 'hello.Batch', version: 1n, definition: batchDefinition() },
              },
              {
                hook: 'hello.Farewell',
                service: 'hello',
                declared: true,
                description: 'Says goodbye',
                state: api.BindingState.UNBOUND,
              },
            ],
          }),
          options.signal,
        );
      },
      watchRules(_request: api.WatchRulesRequest, options: api.CallOptions) {
        return snapshot(
          create(api.WatchRulesResponseSchema, {
            rules: [
              {
                id: 'rule-1',
                state: api.RuleState.ACTIVE,
                current: { ruleId: 'rule-1', version: 1n, name: 'Greeted rule', definition: ruleDefinition() },
              },
            ],
          }),
          options.signal,
        );
      },
      getStream() {
        return reply(() => {
          return create(api.GetStreamResponseSchema, {
            service: 'hello',
            events: {
              name: 'BP_EVENTS_hello',
              exists: true,
              messages: 9007199254740993n,
              bytes: 2048n,
              consumers: 2,
              replicas: 1,
            },
            deadLetters: { name: 'BP_DLQ_hello', exists: true, messages: 1n, bytes: 128n, consumers: 0, replicas: 1 },
            deadLetterCounts: [{ consumer: 'audit', count: 1n }],
          });
        });
      },
      cancelBindingRun() {
        return reply(() => {
          beforeWrite();
          return create(api.CancelBindingRunResponseSchema);
        });
      },
      cancelRuleRun() {
        return reply(() => {
          beforeWrite();
          return create(api.CancelRuleRunResponseSchema);
        });
      },
      getRuleRun() {
        return reply(() => {
          return create(api.GetRuleRunResponseSchema, {
            run: { run: { workflowId: 'rule/rule-1/1', runId: 'run-3', status: api.RunStatus.COMPLETED } },
            version: 1n,
            steps: [{ step: 'record', activity: 'formatter.Record', status: api.StepRunStatus.COMPLETED, attempt: 1 }],
          });
        });
      },
      revokeSession() {
        return reply(() => {
          beforeWrite();
          return create(api.RevokeSessionResponseSchema);
        });
      },
      listBindings() {
        return reply(() => {
          return create(api.ListBindingsResponseSchema, {
            bindings: [
              {
                hook: 'hello.Greet',
                service: 'hello',
                declared: true,
                required: true,
                state: api.BindingState.BOUND,
                current: { hook: 'hello.Greet', version: 1n, definition: bindingDefinition() },
              },
              {
                hook: 'hello.Batch',
                service: 'hello',
                declared: true,
                state: api.BindingState.BOUND,
                current: { hook: 'hello.Batch', version: 1n, definition: batchDefinition() },
              },
              {
                hook: 'hello.Farewell',
                service: 'hello',
                declared: true,
                description: 'Says goodbye',
                state: api.BindingState.UNBOUND,
              },
            ],
          });
        });
      },
      listRules() {
        return reply(() => {
          return create(api.ListRulesResponseSchema, {
            rules: [
              {
                id: 'rule-1',
                state: api.RuleState.ACTIVE,
                current: { ruleId: 'rule-1', version: 1n, name: 'Greeted rule', definition: ruleDefinition() },
              },
            ],
          });
        });
      },
      listBindingVersions() {
        return reply(() => {
          return create(api.ListBindingVersionsResponseSchema, {
            versions: [
              {
                hook: 'hello.Greet',
                version: 1n,
                author: 'fixture',
                comment: 'Initial',
                definition: bindingDefinition(),
              },
            ],
          });
        });
      },
      listRuleVersions() {
        return reply(() => {
          return create(api.ListRuleVersionsResponseSchema, {
            versions: [
              { ruleId: 'rule-1', version: 1n, name: 'Greeted rule', author: 'fixture', definition: ruleDefinition() },
            ],
          });
        });
      },
      listBindingRuns() {
        return reply(() => {
          return create(api.ListBindingRunsResponseSchema, {
            runs: [
              {
                workflowId: 'binding/hello.Greet/1',
                runId: 'run-2',
                status: api.RunStatus.COMPLETED,
                historyLength: 11n,
              },
            ],
          });
        });
      },
      listRuleRuns() {
        return reply(() => {
          return create(api.ListRuleRunsResponseSchema);
        });
      },
      getBindingRun() {
        return reply(() => {
          return create(api.GetBindingRunResponseSchema, {
            run: { run: { workflowId: 'binding/hello.Greet/1', runId: 'run-2', status: api.RunStatus.COMPLETED } },
            hook: 'hello.Greet',
            version: 1n,
            steps: [{ step: 'format', activity: 'formatter.Format', status: api.StepRunStatus.COMPLETED, attempt: 1 }],
          });
        });
      },
      testBinding(request: api.TestBindingRequest) {
        return reply(() => {
          beforeWrite();
          fixture.lastInput = request.input;
          return create(api.TestBindingResponseSchema, { result: { output: request.input } });
        });
      },
      testRule(request: api.TestRuleRequest) {
        return reply(() => {
          beforeWrite();
          fixture.lastInput = request.event;
          return create(api.TestRuleResponseSchema, { matched: true });
        });
      },
      deleteBinding() {
        return reply(() => {
          beforeWrite();
          return create(api.DeleteBindingResponseSchema);
        });
      },
      rollbackBinding() {
        return reply(() => {
          beforeWrite();
          return create(api.RollbackBindingResponseSchema);
        });
      },
      rollbackRule() {
        return reply(() => {
          beforeWrite();
          return create(api.RollbackRuleResponseSchema);
        });
      },
      deleteRule() {
        return reply(() => {
          beforeWrite();
          return create(api.DeleteRuleResponseSchema);
        });
      },
      pauseRule() {
        return reply(() => {
          beforeWrite();
          return create(api.PauseRuleResponseSchema);
        });
      },
      resumeRule() {
        return reply(() => {
          beforeWrite();
          return create(api.ResumeRuleResponseSchema);
        });
      },
      listWorkflows() {
        return reply(() => {
          return create(api.ListWorkflowsResponseSchema, {
            workflows: [
              {
                service: 'hello',
                name: 'Welcome',
                kind: api.WorkflowKind.WORKFLOW,
                description: 'Greets a new user',
                input: schema,
                taskQueue: 'hello',
                pollers: 0,
              },
            ],
          });
        });
      },
      getObsSelectors() {
        return reply(() => {
          return create(api.GetObsSelectorsResponseSchema, {
            sources: { selectors: [{ resource: { 'service.name': 'hello' } }] },
          });
        });
      },
      listObsFieldValues() {
        return reply(() => {
          return create(api.ListObsFieldValuesResponseSchema, { values: ['hello', 'kratos'] });
        });
      },
      listSessions() {
        return reply(() => {
          return create(api.ListSessionsResponseSchema, {
            sessions: [{ id: 'fixture', current: true, address: '127.0.0.1' }],
          });
        });
      },
      validateRule() {
        return reply(() => {
          return create(api.ValidateRuleResponseSchema);
        });
      },
      saveRule(request: api.SaveRuleRequest) {
        return reply(() => {
          beforeWrite();
          return create(api.SaveRuleResponseSchema, {
            version: {
              ruleId: request.id || 'rule-1',
              version: 2n,
              name: request.name,
              definition: request.definition,
            },
          });
        });
      },
      listRuns(request: api.ListRunsRequest) {
        return reply(() => {
          // Page one: the console run; page two: the schedule's run; hook calls apart.
          if (request.hooks)
            return create(api.ListRunsResponseSchema, {
              runs: [
                {
                  workflowId: 'hook/hello/Greet/k1',
                  runId: 'run-h',
                  workflowType: 'backplane.CallHook.v1',
                  taskQueue: 'hello.hooks',
                  status: api.RunStatus.COMPLETED,
                  historyLength: 11n,
                },
              ],
            });
          const runs = [
            create(api.RunSchema, {
              workflowId: 'welcome-1',
              runId: 'run-1',
              workflowType: 'Welcome',
              status: api.RunStatus.FAILED,
              historyLength: 9007199254740993n,
            }),
            create(api.RunSchema, {
              workflowId: 'hello/Welcome-2026-09-30T11:00:00Z',
              runId: 'run-9',
              workflowType: 'Welcome',
              status: api.RunStatus.COMPLETED,
              historyLength: 12n,
            }),
          ];
          if (request.workflowIdPrefix)
            return create(api.ListRunsResponseSchema, {
              runs: runs.filter((run) => run.workflowId.startsWith(request.workflowIdPrefix)),
            });
          return request.pageToken.length
            ? create(api.ListRunsResponseSchema, { runs: runs.slice(1) })
            : create(api.ListRunsResponseSchema, { runs: runs.slice(0, 1), nextPageToken: new Uint8Array([1]) });
        });
      },
      getRun(request: api.GetRunRequest) {
        return reply(() => {
          if (request.workflowId !== 'welcome-1')
            return create(api.GetRunResponseSchema, {
              run: {
                workflowId: request.workflowId,
                runId: request.runId || 'run-0',
                workflowType: 'Parent',
                status: api.RunStatus.CONTINUED_AS_NEW,
              },
              continuedRunId: 'run-0b',
              result: '"done"',
            });
          return create(api.GetRunResponseSchema, {
            run: {
              workflowId: 'welcome-1',
              runId: 'run-1',
              workflowType: 'Welcome',
              taskQueue: 'hello',
              status: api.RunStatus.RUNNING,
              memo: { source: '"console:fixture"', 'backplane.service': '"hello"' },
              parentWorkflowId: 'parent-1',
              parentRunId: 'run-0',
            },
            input: '{"sequence":18446744073709551615}',
            failure: 'Activity failed',
            failureType: 'DatabaseError',
            pendingActivities: [
              {
                activityId: '5',
                activityType: 'Compose',
                state: 'SCHEDULED',
                attempt: 2,
                maximumAttempts: 3,
                lastFailure: 'DatabaseError',
                lastWorker: 'hello-1@fixture',
              },
            ],
            history: [
              {
                id: 1n,
                type: 'WorkflowExecutionStarted',
                summary: 'Welcome on hello',
                payload: '{"sequence":18446744073709551615}',
              },
              {
                id: 5n,
                type: 'StartChildWorkflowExecutionInitiated',
                summary: 'Flow child-1 on hello',
                workflowId: 'child-1',
              },
              { id: 9007199254740993n, type: 'ActivityTaskFailed', failure: 'DatabaseError' },
            ],
          });
        });
      },
      cancelRun() {
        return reply(() => {
          beforeWrite();
          return create(api.CancelRunResponseSchema);
        });
      },
      terminateRun() {
        return reply(() => {
          beforeWrite();
          return create(api.TerminateRunResponseSchema);
        });
      },
      signalRun(request: api.SignalRunRequest) {
        return reply(() => {
          beforeWrite();
          fixture.lastInput = `signal:${request.signal}:${request.input}`;
          return create(api.SignalRunResponseSchema);
        });
      },
      listSchedules() {
        return reply(() => {
          const at = (seconds: bigint) => ({ seconds, nanos: 0 });
          return create(api.ListSchedulesResponseSchema, {
            schedules: [
              {
                service: 'hello',
                name: 'Nightly',
                id: 'hello/Nightly',
                declared: { name: 'Nightly', workflow: 'Welcome', spec: { case: 'cron', value: '0 3 * * *' } },
              },
              {
                service: 'hello',
                name: 'Collect',
                id: 'hello/Collect',
                managedBy: 'schedules/hello.yaml',
                state: {
                  paused: false,
                  actionCount: 12n,
                  workflowType: 'Welcome',
                  owner: 'hello',
                  created: at(1790679600n),
                  nextActions: [at(1790686800n)],
                },
              },
              {
                service: 'hello',
                name: 'Welcome',
                id: 'hello/Welcome',
                declared: { name: 'Welcome', workflow: 'Welcome', spec: { case: 'every', value: { seconds: 3600n } } },
                state: {
                  paused: true,
                  note: 'maintenance (console:fixture)',
                  actionCount: 9007199254740993n,
                  missedCatchupWindow: 2n,
                  overlapSkipped: 1n,
                  workflowType: 'Welcome',
                  owner: 'hello',
                  created: at(1790679600n),
                  nextActions: [at(1790686800n), at(1790690400n)],
                  recentActions: [
                    {
                      workflowId: 'hello/Welcome-2026-09-30T11:00:00Z',
                      runId: 'run-9',
                      actualTime: at(1790679600n),
                      scheduleTime: at(1790679600n),
                    },
                  ],
                },
              },
            ],
          });
        });
      },
      pauseSchedule(request: api.PauseScheduleRequest) {
        return reply(() => {
          beforeWrite();
          fixture.lastInput = request.note;
          return create(api.PauseScheduleResponseSchema);
        });
      },
      unpauseSchedule(request: api.UnpauseScheduleRequest) {
        return reply(() => {
          beforeWrite();
          fixture.lastInput = request.note;
          return create(api.UnpauseScheduleResponseSchema);
        });
      },
      triggerSchedule() {
        return reply(() => {
          beforeWrite();
          return create(api.TriggerScheduleResponseSchema);
        });
      },
      listEvents() {
        return reply(() => {
          return create(api.ListEventsResponseSchema, {
            events: [
              {
                event: 'hello.Greeted',
                service: 'hello',
                name: 'Greeted',
                declared: true,
                messages: 9007199254740993n,
                lastSeq: 9007199254740993n,
                subscribers: [
                  {
                    kind: api.SubscriberKind.REACTOR,
                    service: 'hello',
                    consumer: 'audit',
                    durable: 'hello-audit',
                    deadLetters: 1n,
                    state: { numPending: 2n },
                  },
                ],
              },
            ],
          });
        });
      },
      peekMessages() {
        return reply(() => {
          return create(api.PeekMessagesResponseSchema, {
            messages: [{ seq: 9007199254740993n, event: 'hello.Greeted', data: '{"name":"World"}' }],
          });
        });
      },
      listDeadLetters() {
        return reply(() => {
          return create(api.ListDeadLettersResponseSchema, {
            deadLetters: [
              {
                consumer: 'audit',
                error: 'DatabaseError',
                delivered: 5,
                message: { seq: 9007199254740993n, event: 'hello.Greeted' },
              },
            ],
            counts: [{ consumer: 'audit', count: 1n }],
          });
        });
      },
      redriveDeadLetters(request: api.RedriveDeadLettersRequest) {
        return reply(() => {
          beforeWrite();
          fixture.lastInput = request.seqs.map(String).join(',');
          return create(api.RedriveDeadLettersResponseSchema, { redriven: 1n });
        });
      },
      purgeDeadLetters(request: api.PurgeDeadLettersRequest) {
        return reply(() => {
          beforeWrite();
          fixture.lastInput = request.seqs.map(String).join(',');
          return create(api.PurgeDeadLettersResponseSchema, { purged: 1n });
        });
      },
    };
    return fixtureClient(_constructor, methods);
  }
}
