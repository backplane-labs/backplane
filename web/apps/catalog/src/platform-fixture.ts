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

const source = { name: 'hello', latestVersion: '1.0.0', instances: 1, healthy: 1, health: api.ServiceHealth.HEALTHY };
const traceId = '1234567890abcdef1234567890abcdef';
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
const parseJson = (text: string): JsonValue => JSON.parse(text);
/** A fixture analysis: every read of a variable in the definition's expressions (by pattern, not CEL), bodies included. */
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
      revokeSession() {
        return reply(() => {
          beforeWrite();
          return create(api.RevokeSessionResponseSchema);
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
    };
    return fixtureClient(_constructor, methods);
  }
}
