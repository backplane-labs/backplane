import type * as api from '@backplane-labs/backplane-api';

/** A manifest schema (schemapb) as the wiring editor reads it. */
export type Schema = NonNullable<api.WiringContract['hooks'][number]['input']>;
type Field = Schema['fields'][number];

/** What a value is, as far as wiring cares: the CEL view of a schema field. */
export type ShapeKind = 'object' | 'map' | 'list' | 'string' | 'int' | 'uint' | 'double' | 'bool' | 'bytes' | 'dyn';
export interface ShapeField { name: string; shape: Shape; required: boolean; description?: string }
export interface Shape { kind: ShapeKind; fields?: ShapeField[]; elem?: Shape; choices?: string[] }

export const dyn: Shape = { kind: 'dyn' };
const scalar = (kind: ShapeKind): Shape => ({ kind });

/** The shape of a schema; none (no schema declared): dyn. Recursive refs stop at dyn. */
export function shapeOf(schema: Schema | undefined): Shape {
  if (!schema) return dyn;
  return objectOf(schema, schema, new Set());
}

function objectOf(schema: Schema, root: Schema, seen: Set<Schema>): Shape {
  if (seen.has(schema)) return { kind: 'object', fields: [] };
  const inner = new Set(seen).add(schema);
  return { kind: 'object', fields: schema.fields.map((field) => ({ name: field.name, shape: fieldShape(field, root, inner), required: field.required, description: field.description })) };
}

function fieldShape(field: Field, root: Schema, seen: Set<Schema>): Shape {
  const kind = field.kind;
  switch (kind.case) {
    case 'float': case 'double': return scalar('double');
    case 'int32': case 'int64': return scalar('int');
    case 'uint32': case 'uint64': return scalar('uint');
    case 'bool': return scalar('bool');
    case 'string': case 'duration': case 'timestamp': return scalar('string');
    case 'bytes': return scalar('bytes');
    case 'choice': return { kind: 'string', choices: kind.value.options.map((option) => option.label).filter(Boolean) };
    case 'list': return { kind: 'list', elem: kind.value.items.length === 1 ? fieldShape(kind.value.items[0]!, root, seen) : dyn };
    case 'object': return kind.value.schema ? objectOf(kind.value.schema, root, seen) : { kind: 'map', elem: dyn };
    case 'map': return { kind: 'map', elem: kind.value.valueSchema ? objectOf(kind.value.valueSchema, root, seen) : kind.value.valueField ? fieldShape(kind.value.valueField, root, seen) : dyn };
    case 'ref': {
      const target = kind.value.target.case === 'name' ? root.defs[kind.value.target.value] : undefined;
      return target ? objectOf(target, root, seen) : dyn;
    }
    default: return dyn;
  }
}

/** The shape at a path of field names; undefined when the path leaves the known shape. */
export function shapeAt(shape: Shape, path: readonly string[]): Shape | undefined {
  let current: Shape | undefined = shape;
  for (const key of path) {
    if (!current) return undefined;
    if (current.kind === 'dyn') return dyn;
    if (current.kind === 'map') { current = current.elem ?? dyn; continue; }
    if (current.kind === 'list') { current = /^\d+$/.test(key) ? current.elem ?? dyn : undefined; continue; }
    current = current.fields?.find((field) => field.name === key)?.shape;
  }
  return current;
}

/** Type label of a shape, as the analysis names CEL types. */
export function shapeLabel(shape: Shape): string {
  switch (shape.kind) {
    case 'list': return `list(${shapeLabel(shape.elem ?? dyn)})`;
    case 'map': return `map(string, ${shapeLabel(shape.elem ?? dyn)})`;
    default: return shape.kind;
  }
}
