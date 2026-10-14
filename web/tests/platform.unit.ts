import { describe, expect, it } from 'vitest';
import { create } from '@bufbuild/protobuf';
import { DurationSchema, TimestampSchema } from '@bufbuild/protobuf/wkt';
import { AuditField, AuditOperator, AuditRecordSchema, AuditSource, ErrorField, ErrorOrigin, GetErrorResponseSchema } from '@backplane-labs/backplane-api';
import { decodeChips, detailFacts, encodeChips, filterOf, humanActor, mergeRecords } from '../packages/platform-ui/src/audit-model';
import { addValue, parseValue } from '../packages/platform-ui/src/feed';
import { causesOf, errorFilterOf, groupFilterOf, historyOf, parseStack, reportOf, retentionText, stateOf } from '../packages/platform-ui/src/errors-model';
import { readFileSync } from 'node:fs';
import { nativeJSON } from '../packages/platform-ui/src/serialization';
import { tempoToSpans, parseTelemetryJSON, telemetryJSON, otlpId, decimalMillisToNanos } from '../packages/platform-ui/src/telemetry-model';

describe('platform data adapters', () => {
  it('deduplicates audit replay and sorts uint64 sequence values exactly', () => {
    const at = (seconds: number) => ({ seconds: BigInt(seconds), nanos: 0 });
    const a = create(AuditRecordSchema, { id: 'a', time: at(1) }), b = create(AuditRecordSchema, { id: 'b', time: at(2) }), c = create(AuditRecordSchema, { id: 'c', time: at(3) });
    expect(mergeRecords([a, b], [c, b]).map((record) => record.id)).toEqual(['c', 'b', 'a']);
  });
  it('serializes schema values to application JSON with exact integer literals', () => {
    expect(nativeJSON({ count: 18446744073709551615n, timeout: create(DurationSchema, { seconds: 1n, nanos: 1 }), at: create(TimestampSchema, { seconds: 0n, nanos: 1 }), bytes: new Uint8Array([1, 2]) })).toBe('{"count":18446744073709551615,"timeout":"1.000000001s","at":"1970-01-01T00:00:00.000000001Z","bytes":"AQI="}');
  });
  it('keeps backend JSON extensions and converts OTLP IDs and nested attributes', () => {
    const raw = '{"trace":{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"hello"}}]},"scopeSpans":[{"spans":[{"traceId":"EjRWeJCrze8SNFZ4kKvN7w==","spanId":"EjRWeJCrze8=","name":"Greet","startTimeUnixNano":1790683200123456789,"endTimeUnixNano":"1790683200123456799","attributes":[{"key":"sequence","value":{"intValue":"18446744073709551615"}}],"status":{"code":2}}]}]}]},"extension":9007199254740993}';
    expect(telemetryJSON(parseTelemetryJSON(raw))).toContain('9007199254740993');
    const [span] = tempoToSpans(raw); expect(span.traceId).toBe('1234567890abcdef1234567890abcdef'); expect(span.id).toBe('1234567890abcdef');
    expect(span.startUnixNano).toBe('1790683200123456789'); expect(span.attributes?.sequence).toBe(18446744073709551615n); expect(span.status).toBe('error'); expect(span.service).toBe('hello');
    expect(otlpId('ABCDEF0123456789', 8)).toBe('abcdef0123456789'); expect(decimalMillisToNanos('1.000001')).toBe('1000001');
    expect(decimalMillisToNanos('1e-3')).toBe('1000'); expect(decimalMillisToNanos('1.123456789')).toBe('1123456');
    expect(decimalMillisToNanos('9007199254740993')).toBe('9007199254740993000000');
    expect(decimalMillisToNanos('NaN')).toBe(''); expect(decimalMillisToNanos('-1')).toBe('');
  });
});

describe('audit filters', () => {
  it('builds conditions, one-click values and the URL form', () => {
    let chips = addValue([], 'field:service', 'iam');
    chips = addValue(chips, 'field:service', 'billing');
    chips = addValue(chips, 'attr:tenant', 'acme', true);
    expect(chips).toEqual([{ key: 'field:service', op: 'is', values: ['iam', 'billing'] }, { key: 'attr:tenant', op: 'is_not', values: ['acme'] }]);
    expect(decodeChips(encodeChips(chips))).toEqual(chips);
    expect(decodeChips('[{"key":"field:nope","op":"is","values":[]}]')).toEqual([]);
    expect(decodeChips('not json')).toEqual([]);
    const filter = filterOf(chips, 'text', { from: 1000, to: 2000 });
    expect(filter.conditions[0]!.target).toEqual({ case: 'field', value: AuditField.SERVICE });
    expect(filter.conditions[1]!.target).toEqual({ case: 'attribute', value: 'tenant' });
    expect(filter.conditions[1]!.op).toBe(AuditOperator.IS_NOT);
    expect(filter.start?.seconds).toBe(1n);
    expect(parseValue('3')).toBe(3); expect(parseValue('true')).toBe(true); expect(parseValue('acme')).toBe('acme');
  });

  it('reads actors and details as people do', () => {
    expect(humanActor('console:1b7e2c9a-0000-4000')).toBe('operator · 1b7e2c9a');
    expect(humanActor('user:7')).toBe('user:7');
    const platform = create(AuditRecordSchema, { source: AuditSource.PLATFORM, attributes: { revision: 3, keys: ['a', 'b'], workflow_id: 'wf' } });
    expect(detailFacts(platform)).toEqual(['rev 3', 'keys a, b', 'workflow wf']);
    const application = create(AuditRecordSchema, { source: AuditSource.APPLICATION, message: 'deleted', attributes: { 'backplane.audit': true, 'event.name': 'x', tenant: 'acme' } });
    expect(detailFacts(application)).toEqual(['deleted', 'tenant acme']);
  });
});

describe('errors', () => {
  it('parses V8, Firefox and Go stacks into frames', () => {
    expect(parseStack('TypeError: x is undefined\n    at render (https://app/main.js:10:5)\n    at https://app/vendor.js:2:3')).toEqual([
      { id: '0', function: 'render', file: 'https://app/main.js', line: 10, column: 5 }, { id: '1', function: '<anonymous>', file: 'https://app/vendor.js', line: 2, column: 3 }]);
    expect(parseStack('render@https://app/main.js:10:5\n@https://app/vendor.js:2:3')).toMatchObject([{ function: 'render', line: 10 }, { function: '<anonymous>', line: 2 }]);
    expect(parseStack('goroutine 1 [running]:\nmain.charge(0x1)\n\t/src/pay.go:12 +0x1d\nmain.main()\n\t/src/main.go:5 +0x10')).toEqual([
      { id: '0', function: 'main.charge', file: '/src/pay.go', line: 12 }, { id: '1', function: 'main.main', file: '/src/main.go', line: 5 }]);
  });

  it('turns chips into an error filter and reads the envelope', () => {
    const filter = errorFilterOf([{ key: 'service', op: 'is', values: ['web'] }, { key: 'origin', op: 'is_not', values: ['otel-log'] }, { key: 'nope', op: 'is', values: ['x'] }], 'boom', { from: 0, to: 60_000 });
    expect(filter.conditions).toHaveLength(2);
    expect(filter.conditions[1]!.values).toEqual(['otel-log']);
    const envelope = JSON.parse(readFileSync('tests/fixtures/errors/exception-envelope.json', 'utf8'));
    expect(historyOf(envelope).map((item) => [item.kind, item.name])).toEqual([['breadcrumb', 'command.started'], ['state', 'editor']]);
    expect(stateOf(envelope).map((source) => source.name)).toEqual(['editor', 'inline']);
    const cause = causesOf({ exception: envelope.exception, stacktrace: '', occurrence: undefined } as never);
    expect(cause).toMatchObject({ type: 'FixtureError', message: 'Synthetic fixture exception', frames: [{ function: 'fixture', file: 'synthetic.js' }] });
  });

  it('narrows a filter to a group, says the retention and writes a report', () => {
    const filter = errorFilterOf([{ key: 'service', op: 'is', values: ['web'] }, { key: 'environment', op: 'is', values: ['prod'] }], '', { from: 0, to: 60_000 });
    const narrowed = groupFilterOf(filter, { fingerprint: 'ab'.repeat(16), service: 'api' });
    expect(narrowed.conditions.map((condition) => [condition.field, condition.values])).toEqual([[ErrorField.ENVIRONMENT, ['prod']], [ErrorField.FINGERPRINT, ['ab'.repeat(16)]], [ErrorField.SERVICE, ['api']]]);
    expect(retentionText(undefined)).toBeUndefined(); expect(retentionText({ seconds: 7_776_000n, nanos: 0 } as never)).toBe('90 d'); expect(retentionText({ seconds: 7200n, nanos: 0 } as never)).toBe('2 h');
    const detail = create(GetErrorResponseSchema, { occurrence: { ref: 'r1', origin: ErrorOrigin.OTEL_LOG, type: 'PaymentError', message: 'declined', service: 'api', environment: 'prod', fingerprint: 'ff', unhandled: true, time: { seconds: 60n, nanos: 0 } },
      stacktrace: 'PaymentError: declined\n\tat charge', group: { fingerprint: 'ff', service: 'api', count: 9n, firstSeen: { seconds: 0n, nanos: 0 }, lastSeen: { seconds: 60n, nanos: 0 } }, warnings: ['truncated'] });
    const report = reportOf(detail);
    expect(report).toContain('# PaymentError: declined'); expect(report).toContain('- service: api (prod)'); expect(report).toContain('unhandled');
    expect(report).toContain('- group: 9 occurrences since 1970-01-01T00:00:00.000Z'); expect(report).toContain('- warnings: truncated'); expect(report.endsWith('```')).toBe(true);
  });
});
