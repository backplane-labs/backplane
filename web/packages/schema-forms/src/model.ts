import { create, fromJsonString, toJsonString } from '@bufbuild/protobuf';
import { DurationSchema, TimestampSchema } from '@bufbuild/protobuf/wkt';
import {
  Engine, ValueSchema, defaultValue, fromNative, toNative, refDefKey, listItemDef,
  type Native, type NativeStruct, type Schema, type Schema_Field, type Value,
} from '@gopherex/schemapb';

export type FieldPath = readonly (string | number)[];
export const pathKey = (path: FieldPath) => JSON.stringify(path);

/** Keys are literal segments, never dotted paths or JS property expressions. */
export function valueAt(values: NativeStruct, path: FieldPath): Native | undefined {
  let value: Native | undefined = values;
  for (const key of path) {
    if (!value || typeof value !== 'object' || !Object.hasOwn(value, key)) return undefined;
    value = (value as Record<string | number, Native>)[key];
  }
  return value;
}

export function setValue(values: NativeStruct, path: FieldPath, value: Native | undefined): NativeStruct {
  if (!path.length) throw new Error('A field path is required');
  const result = structuredClone(values);
  let parent: Record<string | number, Native> = result;
  for (const [i, key] of path.slice(0, -1).entries()) {
    if (!Object.hasOwn(parent, key) || parent[key] === null || typeof parent[key] !== 'object') {
      Object.defineProperty(parent, key, { value: typeof path[i + 1] === 'number' ? [] : {}, enumerable: true, writable: true, configurable: true });
    }
    parent = parent[key] as Record<string | number, Native>;
  }
  const key = path[path.length - 1];
  if (value === undefined) {
    if (Array.isArray(parent) && typeof key === 'number') parent.splice(key, 1);
    else delete parent[key];
  } else Object.defineProperty(parent, key, { value, enumerable: true, writable: true, configurable: true });
  return result;
}

export function encodeValue(value: Native): string {
  const wire = fromNative(value);
  // Native bigint has no signedness. Preserve the upper half of uint64 in
  // the generic editor; the engine selects canonical kinds when baking.
  function unsigned(node: Value) {
    if (node.kind.case === 'int64Value' && node.kind.value > 9223372036854775807n) {
      node.kind = { case: 'uint64Value', value: node.kind.value };
    } else if (node.kind.case === 'structValue') Object.values(node.kind.value.fields).forEach(unsigned);
    else if (node.kind.case === 'listValue') node.kind.value.items.forEach(unsigned);
  }
  unsigned(wire);
  return toJsonString(ValueSchema, wire, { prettySpaces: 2 });
}

export function decodeValue(text: string): Native {
  return toNative(fromJsonString(ValueSchema, text));
}

/** Preserve invalid input as text so validation reports it, never round int64. */
export function parseScalar(field: Schema_Field, text: string): Native {
  switch (field.kind.case) {
    case 'int64': case 'uint64': case 'int32': case 'uint32':
      return /^-?\d+$/.test(text) ? BigInt(text) : text;
    case 'float': case 'double':
      return text.trim() && Number.isFinite(Number(text)) ? Number(text) : text;
    case 'duration': case 'timestamp':
      try { return fromJsonString(field.kind.case === 'duration' ? DurationSchema : TimestampSchema, JSON.stringify(text)); }
      catch { return text; }
    case 'bytes':
      try {
        if (!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(text)) return text;
        return Uint8Array.from(atob(text), (char) => char.charCodeAt(0));
      } catch { return text; }
    default: return text;
  }
}

export function scalarText(value: Native | undefined): string {
  if (value === undefined || value === null) return '';
  if (value instanceof Uint8Array) return btoa(Array.from(value, (byte) => String.fromCharCode(byte)).join(''));
  if (typeof value === 'object' && '$typeName' in value) {
    if (value.$typeName === 'google.protobuf.Duration') return JSON.parse(toJsonString(DurationSchema, value as import('@bufbuild/protobuf/wkt').Duration));
    if (value.$typeName === 'google.protobuf.Timestamp') return JSON.parse(toJsonString(TimestampSchema, value as import('@bufbuild/protobuf/wkt').Timestamp));
  }
  return typeof value === 'object' ? encodeValue(value) : String(value);
}

/**
 * Plain JSON (numbers parsed losslessly) as the field's native value:
 * durations from integer nanoseconds or text, integers as bigint, objects,
 * lists and maps through their schemas. Go's encoding/json writes a
 * time.Duration as nanoseconds, so a value read from a Go service converts
 * here before an engine or an editor sees it. Anything else stays as given.
 */
export function nativeOf(root: Schema, field: Schema_Field, value: unknown): Native {
  switch (field.kind.case) {
    case 'duration':
      if (typeof value === 'number' && Number.isInteger(value)) value = BigInt(value);
      if (typeof value === 'bigint') return create(DurationSchema, { seconds: value / 1_000_000_000n, nanos: Number(value % 1_000_000_000n) });
      return typeof value === 'string' ? parseScalar(field, value) : plain(value);
    case 'timestamp':
      return typeof value === 'string' ? parseScalar(field, value) : plain(value);
    case 'int32': case 'int64': case 'uint32': case 'uint64':
      if (typeof value === 'number' && Number.isInteger(value)) return BigInt(value);
      return typeof value === 'string' && /^-?\d+$/.test(value) ? BigInt(value) : plain(value);
    case 'float': case 'double':
      return typeof value === 'bigint' ? Number(value) : plain(value);
    case 'object': case 'ref': case 'oneOf': {
      const nested = isRecord(value) ? fieldSchema(root, field, plain(value)) : undefined;
      return nested && isRecord(value) ? structOf(root, nested, value) : plain(value);
    }
    case 'list': {
      const list = field.kind.value;
      return Array.isArray(value) ? value.map((item, index) => { const def = listItemDef(list, index); return def ? nativeOf(root, def, item) : plain(item); }) : plain(value);
    }
    case 'map': {
      const map = field.kind.value;
      if (!isRecord(value)) return plain(value);
      const result: NativeStruct = {};
      for (const [key, item] of Object.entries(value)) {
        const next = map.valueField ? nativeOf(root, map.valueField, item) : map.valueSchema && isRecord(item) ? structOf(root, map.valueSchema, item) : plain(item);
        Object.defineProperty(result, key, { value: next, enumerable: true, writable: true, configurable: true });
      }
      return result;
    }
    default: return plain(value);
  }
}
function structOf(root: Schema, schema: Schema, value: Record<string, unknown>): NativeStruct {
  const result: NativeStruct = {};
  for (const [key, item] of Object.entries(value)) {
    const child = schema.fields.find((field) => field.name === key);
    Object.defineProperty(result, key, { value: child ? nativeOf(root, child, item) : plain(item), enumerable: true, writable: true, configurable: true });
  }
  return result;
}
const isRecord = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value);
/** JSON has no other shapes: strings, numbers, bigints, booleans, null, arrays and objects are all Native. */
const plain = (value: unknown) => value as Native;

export function fieldSchema(root: Schema, field: Schema_Field, value: Native | undefined): Schema | undefined {
  switch (field.kind.case) {
    case 'object': return field.kind.value.schema;
    case 'ref': return root.defs[refDefKey(field.kind.value)];
    case 'oneOf': {
      if (!value || typeof value !== 'object') return undefined;
      const variant = (value as NativeStruct)[field.kind.value.discriminator];
      return typeof variant === 'string' ? field.kind.value.variants[variant] : undefined;
    }
    default: return undefined;
  }
}

/**
 * The value a field starts with: its default, else an empty value of its
 * kind; an object's required children are filled the same way, so a form
 * opens with every required input present instead of a "set value" button.
 */
export function scaffoldValue(root: Schema, field: Schema_Field): Native {
  const preset = defaultValue(field);
  if (preset !== undefined) return field.kind.case === 'object' || field.kind.case === 'ref' ? scaffoldStruct(root, field, preset as NativeStruct) : preset;
  if (field.kind.case === 'object' || field.kind.case === 'ref') return scaffoldStruct(root, field, {});
  return emptyValue(field);
}
function scaffoldStruct(root: Schema, field: Schema_Field, value: NativeStruct): NativeStruct {
  const nested = fieldSchema(root, field, value);
  return nested ? scaffoldFields(root, nested, value) : value;
}
/** Fills the required fields of schema that values lack. */
export function scaffoldFields(root: Schema, schema: Schema, values: NativeStruct): NativeStruct {
  const result = { ...values };
  for (const field of schema.fields) {
    if (!Object.hasOwn(result, field.name) && field.required && field.kind.case !== 'computed') {
      Object.defineProperty(result, field.name, { value: scaffoldValue(root, field), enumerable: true, writable: true, configurable: true });
    }
  }
  return result;
}
/** The form's starting values: initial values with the required fields filled. */
export function scaffoldForm(schema: Schema, values: NativeStruct): NativeStruct { return scaffoldFields(schema, schema, values); }

export function emptyValue(field: Schema_Field): Native {
  switch (field.kind.case) {
    case 'object': case 'ref': case 'map': case 'oneOf': return {};
    case 'list': return [];
    case 'bool': return false;
    case 'int32': case 'int64': case 'uint32': case 'uint64': return 0n;
    case 'float': case 'double': return 0;
    case 'bytes': return new Uint8Array();
    case 'json': return null;
    case 'choice': return toNative(field.kind.value.options[0]?.value);
    default: return '';
  }
}

export function evaluateForm(engine: Engine, values: NativeStruct) {
  const resolved = structuredClone(values);
  const outcome = engine.bakeDetailed(resolved);
  return { ...outcome, resolved };
}
