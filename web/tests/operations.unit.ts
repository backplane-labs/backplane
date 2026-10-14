import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';
import { Engine, SchemaSchema } from '@gopherex/schemapb';
import { scaffoldForm } from '../packages/schema-forms/src/model';
import { parsePayload, payloadCompletions, payloadDiagnostics, scaffoldJSON } from '../packages/platform-ui/src/operations-model';

const schema = create(SchemaSchema, { id: { name: 'input' }, defs: { address: { id: { name: 'address' }, fields: [{ name: 'city', required: true, kind: { case: 'string', value: {} } }] } }, fields: [
  { name: 'name', required: true, kind: { case: 'string', value: { minLen: 1n } } },
  { name: 'sequence', kind: { case: 'uint64', value: { default: 18446744073709551615n } } },
  { name: 'count', kind: { case: 'int64', value: { gte: 1n } } },
  { name: 'home', required: true, kind: { case: 'ref', value: { target: { case: 'name', value: 'address' } } } },
  { name: 'tags', kind: { case: 'list', value: { items: [{ kind: { case: 'string', value: {} } }] } } },
  { name: 'total', kind: { case: 'computed', value: { expr: 'root.count * 2' } } },
] });
const engine = Engine.compile(schema);

describe('operation payloads', () => {
  it('scaffolds required fields and defaults, nested through refs', () => {
    expect(scaffoldForm(schema, {})).toEqual({ name: '', home: { city: '' } });
    expect(scaffoldForm(schema, { name: 'x', home: { city: 'Oslo' } })).toEqual({ name: 'x', home: { city: 'Oslo' } });
    expect(JSON.parse(scaffoldJSON(engine).replace('18446744073709551615', '"max"'))).toEqual({ name: '', sequence: 'max', home: { city: '' } });
  });
  it('completes keys of the object at the cursor', () => {
    const text = '{\n  "sequence": 1,\n  "na';
    const top = payloadCompletions(schema, text, text.length)!;
    expect(top.options.map((option) => option.label)).toEqual(['name', 'sequence', 'count', 'home', 'tags']);
    expect(top.from).toBe(text.indexOf('"na') + 1);
    expect(top.options[0]!.apply).toBe('name": ');
    const nested = payloadCompletions(schema, '{"tags": [], "home": {"ci', 25)!;
    expect(nested.options.map((option) => option.label)).toEqual(['city']);
    const bare = payloadCompletions(schema, '{"home": {}, ', 13)!;
    expect(bare.options[0]!.apply).toBe('"name": ');
    expect(payloadCompletions(schema, '{"name": "x', 11)).toBeNull();
    expect(payloadCompletions(schema, '{"tags": [1, 2', 14)).toBeNull();
  });
  it('reports schema problems at the key and keeps big integers', () => {
    expect(parsePayload('{"sequence": 18446744073709551615, "count": 1.5}')).toEqual({ sequence: 18446744073709551615n, count: 1.5 });
    expect(parsePayload('[1]')).toBeUndefined();
    const text = '{\n  "name": "ok",\n  "count": 0,\n  "home": {"city": "Oslo"}\n}';
    const report = payloadDiagnostics(engine, text);
    expect(report.blocking).toBe(true);
    expect(report.diagnostics.map((item) => [item.message, text.slice(item.from, item.to)])).toEqual([[expect.stringContaining('count'), '"count"']]);
    expect(payloadDiagnostics(engine, '{"name": "ok", "count": 3, "home": {"city": "Oslo"}}').blocking).toBe(false);
    expect(payloadDiagnostics(engine, '{"name": ').diagnostics).toEqual([]);
    expect(payloadDiagnostics(engine, '[1]').diagnostics[0]?.message).toContain('object');
  });
});
