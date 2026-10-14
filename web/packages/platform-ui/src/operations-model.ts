import { parse, isLosslessNumber, type LosslessNumber } from 'lossless-json';
import { Schema_Field_Severity, refDefKey, resultBlocking, type Engine, type Native, type NativeStruct, type Schema, type Schema_Field } from '@gopherex/schemapb';
import type { EditorCompletion, EditorCompletionResult, EditorDiagnostic } from '@backplane-labs/backplane-editors';
import { evaluateForm, scaffoldForm } from '@backplane-labs/backplane-schema-forms';
import { nativeJSON } from './serialization.js';

/** The JSON a payload editor opens with: the schema's defaults and its required fields. */
export function scaffoldJSON(engine: Engine): string {
  return nativeJSON(evaluateForm(engine, scaffoldForm(engine.schema, {})).resolved, 2);
}

function kindLabel(field: Schema_Field): string {
  const kind = field.kind.case ?? 'value';
  return field.required ? `${kind}, required` : kind;
}

/** The object the cursor is in: the names of the keys on the way from the root, innermost last. */
function keyPath(text: string, position: number): string[] | undefined {
  const stack: { key?: string; list: boolean }[] = [];
  let i = 0, inString = false, token = '', lastKey: string | undefined, expectKey = false;
  while (i < position) {
    const ch = text[i]!;
    if (inString) {
      if (ch === '\\') { token += text[i + 1] ?? ''; i += 2; continue; }
      if (ch === '"') { inString = false; if (expectKey) lastKey = token; } else token += ch;
    } else if (ch === '"') { inString = true; token = ''; }
    else if (ch === '{') { stack.push({ key: lastKey, list: false }); expectKey = true; lastKey = undefined; }
    else if (ch === '[') { stack.push({ key: lastKey, list: true }); expectKey = false; lastKey = undefined; }
    else if (ch === '}' || ch === ']') { stack.pop(); expectKey = false; lastKey = stack.at(-1)?.key; }
    else if (ch === ':') expectKey = false;
    else if (ch === ',') expectKey = !stack.at(-1)?.list;
    i++;
  }
  if (!stack.length || !expectKey && !inString) return undefined;
  if (inString && !expectKey) return undefined;
  return stack.map((frame) => frame.key).filter((key): key is string => key !== undefined);
}

function schemaAt(root: Schema, path: string[]): Schema | undefined {
  let schema: Schema | undefined = root;
  for (const name of path) {
    const field: Schema_Field | undefined = schema?.fields.find((candidate) => candidate.name === name);
    if (!field) return undefined;
    schema = field.kind.case === 'object' ? field.kind.value.schema : field.kind.case === 'ref' ? root.defs[refDefKey(field.kind.value)] : undefined;
  }
  return schema;
}

/** Key completions of a JSON payload against its schema: the fields of the object the cursor is in. */
export function payloadCompletions(root: Schema, text: string, position: number): EditorCompletionResult | null {
  const path = keyPath(text, position); if (!path) return null;
  const schema = schemaAt(root, path); if (!schema) return null;
  let from = position; while (from > 0 && /[\w"]/.test(text[from - 1]!)) from--;
  const quoted = text[from] === '"', closing = text[position] === '"';
  const options: EditorCompletion[] = schema.fields.filter((field) => field.kind.case !== 'computed').map((field) => ({
    label: field.name, detail: kindLabel(field), type: 'property',
    apply: `${quoted || closing ? '' : '"'}${field.name}${closing ? '' : '": '}`,
  }));
  return { from: quoted ? from + 1 : from, options };
}

function toNativeJSON(value: unknown): Native {
  if (value === null) return null;
  if (isLosslessNumber(value)) {
    const text = (value as LosslessNumber).toString();
    return /^-?\d+$/.test(text) ? BigInt(text) : Number(text);
  }
  if (Array.isArray(value)) return value.map(toNativeJSON);
  if (typeof value === 'object') return Object.fromEntries(Object.entries(value as Record<string, unknown>).map(([key, item]) => [key, toNativeJSON(item)]));
  return value as Native;
}

/** The payload as the schema engine sees it; undefined when the text is not a JSON object. */
export function parsePayload(text: string): NativeStruct | undefined {
  try {
    const value = toNativeJSON(parse(text));
    return value && typeof value === 'object' && !Array.isArray(value) && !(value instanceof Uint8Array) ? value as NativeStruct : undefined;
  } catch { return undefined; }
}

function keyOffset(text: string, path: string): { from: number; to: number } {
  const leaf = path.split(/[.[]/).filter(Boolean).at(-1)?.replace(/]$/, '');
  const index = leaf ? text.indexOf(`"${leaf}"`) : -1;
  return index < 0 ? { from: 0, to: 0 } : { from: index, to: index + leaf!.length + 2 };
}

/** Schema diagnostics of a JSON payload; syntax errors are the editor's own. Empty when the text does not parse. */
export function payloadDiagnostics(engine: Engine, text: string): { diagnostics: EditorDiagnostic[]; blocking: boolean } {
  const values = parsePayload(text);
  if (!values) return { diagnostics: text.trim() && text.trim() !== 'null' && (() => { try { parse(text); return true; } catch { return false; } })()
    ? [{ from: 0, to: 0, severity: 'error', message: 'The payload must be a JSON object.' }] : [], blocking: false };
  const outcome = evaluateForm(engine, values);
  return {
    diagnostics: outcome.result.errors.map((error) => ({
      ...keyOffset(text, error.path), severity: error.severity === Schema_Field_Severity.WARNING ? 'warning' : 'error',
      message: error.path ? `${error.path}: ${error.message}` : error.message,
    })),
    blocking: resultBlocking(outcome.result),
  };
}
