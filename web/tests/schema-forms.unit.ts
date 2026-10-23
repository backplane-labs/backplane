import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';
import { Engine, SchemaSchema, Schema_FieldSchema, masked, structToNative } from '@gopherex/schemapb';
import { decodeValue, encodeValue, evaluateForm, parseScalar, scalarText, setValue, valueAt } from '../packages/schema-forms/src/model';

describe('schema form engine boundary', () => {
  it('resolves defaults and CEL without mutating caller input', () => {
    const schema = create(SchemaSchema, { id: { name: 'limits' }, fields: [
      { name: 'replicas', kind: { case: 'int64', value: { default: 3n, gte: 1n } } },
      { name: 'capacity', kind: { case: 'computed', value: { expr: 'root.replicas * 2' } } },
      { name: 'advanced', when: 'root.replicas > 5', required: true, kind: { case: 'string', value: {} } },
    ] });
    const input = {};
    const result = evaluateForm(Engine.compile(schema), input);
    expect(input).toEqual({});
    expect(result.resolved).toMatchObject({ replicas: 3n, capacity: 6n });
    expect(result.baked).toBeDefined();
    expect(evaluateForm(Engine.compile(schema), { replicas: 6n }).result.errors.map((e) => e.path)).toContain('advanced');
  });
  it('roundtrips uint64 max and signed values nested in structures', () => {
    const values = { sequence: 18446744073709551615n, negative: -9223372036854775808n, items: [9007199254740993n] };
    expect(decodeValue(encodeValue(values))).toEqual(values);
    const field = create(Schema_FieldSchema, { kind: { case: 'uint64', value: {} } });
    expect(parseScalar(field, '18446744073709551615')).toBe(values.sequence);
    expect(scalarText(values.sequence)).toBe('18446744073709551615');
  });
  it('retains invalid lexical edits and exact nanoseconds', () => {
    const integer = create(Schema_FieldSchema, { kind: { case: 'int64', value: {} } });
    expect(parseScalar(integer, '-')).toBe('-');
    const timestamp = create(Schema_FieldSchema, { kind: { case: 'timestamp', value: {} } });
    expect(scalarText(parseScalar(timestamp, '2026-09-29T10:00:00.123456789Z'))).toBe('2026-09-29T10:00:00.123456789Z');
  });
  it('uses structured error paths for literal keys and list indexes', () => {
    const value = setValue({}, ['a.b', 'items', 0, '__proto__'], 'literal');
    expect(valueAt(value, ['a.b', 'items', 0, '__proto__'])).toBe('literal');
    expect(valueAt(value, ['a', 'b'])).toBeUndefined();
    expect(({} as Record<string, unknown>).literal).toBeUndefined();
    expect(valueAt(setValue(value, ['a.b', 'items', 0], undefined), ['a.b', 'items'])).toEqual([]);
  });
  it('masks declared secrets without changing the editable values', () => {
    const schema = create(SchemaSchema, { id: { name: 'secret' }, fields: [
      { name: 'password', secret: true, kind: { case: 'string', value: {} } },
      { name: 'name', kind: { case: 'string', value: {} } },
    ] });
    const result = evaluateForm(Engine.compile(schema), { password: 'hidden-value', name: 'demo', unknown: 'public-extra' });
    const safe = structToNative(masked(result.baked!));
    expect(safe).toEqual({ password: '***', name: 'demo', unknown: 'public-extra' });
    expect(result.resolved.password).toBe('hidden-value');
  });
});

describe('schema table cells', () => {
  it('shows 16 bytes as a uuid and other bytes as hex', async () => {
    const { bytesText } = await import('../packages/schema-forms/src/table');
    const id = new Uint8Array(16); id[0] = 0xde; id[15] = 1;
    expect(bytesText(id)).toBe('de000000-0000-0000-0000-000000000001');
    expect(bytesText(new Uint8Array([1, 0xab]))).toBe('01ab');
  });
});
