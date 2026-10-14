import { Document, isMap, isPair, isScalar, isSeq, parseDocument, type Node, type Pair, type Scalar } from 'yaml';
import { fromJson, ScalarType, toJson, type DescField, type DescMessage, type JsonValue } from '@bufbuild/protobuf';
import * as api from '@backplane-labs/backplane-api';

/**
 * A binding or rule as the wiring editor holds it: YAML text whose data is the
 * definition's protojson (camelCase keys; values are JSON trees whose strings
 * are CEL). The text is the draft's source of truth; the graph edits it
 * through the YAML document so the author's layout and comments survive.
 */
export type WiringKind = 'binding' | 'rule';
export type Definition = api.BindingDefinition | api.RuleDefinition;

export const definitionSchema = (kind: WiringKind): DescMessage => kind === 'binding' ? api.BindingDefinitionSchema : api.RuleDefinitionSchema;

/** A problem of a draft at a range of its text (UTF-16 offsets). */
export interface DraftProblem {
  from: number; to: number; message: string; severity: 'error' | 'warning';
  /** yaml: the text does not read; shape: keys or types the definition cannot hold; server: a violation. */
  source: 'yaml' | 'shape' | 'server';
  code?: string; path?: string;
}

export interface Draft {
  kind: WiringKind; text: string; doc: Document.Parsed;
  /** Absent when the text does not read as a definition. */
  definition?: Definition;
  problems: DraftProblem[];
}

const rootOrder: Record<WiringKind, string[]> = {
  binding: ['hook', 'description', 'steps', 'result', 'editor'],
  rule: ['event', 'description', 'when', 'steps', 'editor'],
};
const stepOrder = ['description', 'forEach', 'as', 'concurrency', 'onError', 'maxItems', 'activity', 'when', 'after', 'input', 'steps', 'result', 'undo', 'undoInput', 'retry', 'startToClose', 'heartbeat'];

/** Steps by name, each with its keys in reading order and its body's steps likewise. */
function orderSteps(steps: JsonValue | undefined): JsonValue | undefined {
  if (!steps || typeof steps !== 'object' || Array.isArray(steps)) return steps;
  const map = steps as Record<string, Record<string, JsonValue>>;
  return Object.fromEntries(Object.keys(map).sort().map((name) => {
    const step = ordered(map[name]!, stepOrder);
    if (step.steps) step.steps = orderSteps(step.steps)!;
    return [name, step];
  }));
}

function ordered(json: Record<string, JsonValue>, order: readonly string[]): Record<string, JsonValue> {
  const out: Record<string, JsonValue> = {};
  for (const key of order) if (key in json) out[key] = json[key]!;
  for (const key of Object.keys(json)) if (!(key in out)) out[key] = json[key]!;
  return out;
}

/** The YAML text of a definition: keys in reading order, steps by name, the editor's layout compact. */
export function toYAML(kind: WiringKind, definition: Definition): string {
  const json = toJson(definitionSchema(kind), definition) as Record<string, JsonValue>;
  if (json.steps) json.steps = orderSteps(json.steps)!;
  const doc = new Document(ordered(json, rootOrder[kind]));
  compact(doc);
  return doc.toString({ lineWidth: 0 });
}

/** Layout points, `after` lists and retry policies read best on one line. */
function compact(doc: Document) {
  const flow = (node: unknown) => { if (isMap(node) || isSeq(node)) node.flow = true; };
  const stepsOf = (steps: unknown) => {
    if (!isMap(steps)) return;
    for (const pair of steps.items) if (isMap(pair.value)) { flow(pair.value.get('after')); flow(pair.value.get('retry')); stepsOf(pair.value.get('steps')); }
  };
  stepsOf(doc.get('steps'));
  const editor = doc.get('editor');
  if (isMap(editor)) {
    const nodes = editor.get('nodes'); if (isMap(nodes)) for (const pair of nodes.items) flow(pair.value);
    const notes = editor.get('notes'); if (isSeq(notes)) for (const note of notes.items) if (isMap(note)) flow(note.get('at'));
  }
}

/** A new definition's text: the source and one example step in comments. */
export function templateYAML(kind: WiringKind, source: string): string {
  return kind === 'binding'
    ? `hook: ${source}\nsteps:\n  # name:\n  #   activity: <service>.<Activity>\n  #   input: {field: req.field}\nresult: {}\n`
    : `event: ${source}\nsteps:\n  # name:\n  #   activity: <service>.<Activity>\n  #   input: {field: event.field}\n`;
}

const goDuration = /^(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$/;
const units: Record<string, number> = { ns: 1e-9, us: 1e-6, 'µs': 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 };
/** Go-style durations ("1m30s", "500ms") as protojson's seconds ("90s"). */
export function protoDuration(text: string): string {
  if (!goDuration.test(text)) return text;
  let seconds = 0;
  for (const [, value, unit] of text.matchAll(/(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g)) seconds += Number(value) * units[unit!]!;
  return `${Number(seconds.toFixed(9))}s`;
}

const wellKnown = new Set(['google.protobuf.Value', 'google.protobuf.Struct', 'google.protobuf.ListValue']);
const fieldByKey = (desc: DescMessage, key: string) => desc.fields.find((field) => field.jsonName === key || field.name === key);

/** Reads a draft: YAML syntax, the shape of a definition, then protojson. */
export function parseDraft(kind: WiringKind, text: string): Draft {
  const doc = parseDocument(text, { uniqueKeys: true, prettyErrors: false });
  const problems: DraftProblem[] = [];
  for (const error of doc.errors) problems.push({ from: error.pos[0], to: Math.max(error.pos[1], error.pos[0] + 1), message: error.message.split('\n')[0]!, severity: 'error', source: 'yaml' });
  for (const warning of doc.warnings) problems.push({ from: warning.pos[0], to: Math.max(warning.pos[1], warning.pos[0] + 1), message: warning.message.split('\n')[0]!, severity: 'warning', source: 'yaml' });
  if (doc.errors.length) return { kind, text, doc, problems };
  checkShape(doc.contents, definitionSchema(kind), '', problems);
  if (problems.some((problem) => problem.severity === 'error')) return { kind, text, doc, problems };
  try {
    const json = normalize(doc.toJS({ maxAliasCount: 50 }) ?? {});
    return { kind, text, doc, problems, definition: fromJson(definitionSchema(kind), json as JsonValue) as Definition };
  } catch (error) {
    problems.push({ from: 0, to: Math.min(text.length, Math.max(1, text.indexOf('\n'))), message: error instanceof Error ? error.message : String(error), severity: 'error', source: 'shape' });
    return { kind, text, doc, problems };
  }
}

/** Durations to protojson; everything else as it reads. */
function normalize(value: unknown): unknown {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return value;
  const out = value as Record<string, unknown>;
  const steps = (map: unknown) => {
    if (!map || typeof map !== 'object') return;
    for (const step of Object.values(map as Record<string, Record<string, unknown> | null>)) {
      if (!step || typeof step !== 'object') continue;
      for (const key of ['startToClose', 'start_to_close', 'heartbeat']) if (typeof step[key] === 'string') step[key] = protoDuration(step[key]);
      const retry = step.retry as Record<string, unknown> | undefined;
      if (retry && typeof retry === 'object') for (const key of ['initialInterval', 'initial_interval', 'maxInterval', 'max_interval']) if (typeof retry[key] === 'string') retry[key] = protoDuration(retry[key]);
      steps(step.steps);
    }
  };
  steps(out.steps);
  return out;
}

function checkShape(node: unknown, desc: DescMessage, path: string, problems: DraftProblem[]) {
  if (node == null || (isScalar(node) && node.value == null)) return;
  if (!isMap(node)) { problems.push({ ...nodeRange(node), message: `expected a mapping (${desc.name})`, severity: 'error', source: 'shape', path }); return; }
  for (const pair of node.items) {
    const key = isScalar(pair.key) ? String(pair.key.value) : '';
    const field = fieldByKey(desc, key);
    if (!field) {
      const known = desc.fields.map((entry) => entry.jsonName).join(', ');
      problems.push({ ...nodeRange(pair.key), message: `unknown key "${key}" (${desc.name} has ${known})`, severity: 'error', source: 'shape', path: `${path}/${escape(key)}` });
      continue;
    }
    checkField(pair.value, field, `${path}/${escape(field.jsonName)}`, problems);
  }
}

function checkField(value: unknown, field: DescField, path: string, problems: DraftProblem[]) {
  if (value == null || (isScalar(value) && value.value == null)) return;
  switch (field.fieldKind) {
    case 'message':
      if (!wellKnown.has(field.message.typeName) && field.message.typeName !== 'google.protobuf.Duration') checkShape(value, field.message, path, problems);
      else if (field.message.typeName === 'google.protobuf.Duration' && !(isScalar(value) && typeof value.value === 'string' && /^-?\d+(\.\d+)?s$/.test(protoDuration(value.value))))
        problems.push({ ...nodeRange(value), message: 'expected a duration such as 30s, 1m30s or 500ms', severity: 'error', source: 'shape', path });
      return;
    case 'map':
      if (!isMap(value)) { problems.push({ ...nodeRange(value), message: 'expected a mapping', severity: 'error', source: 'shape', path }); return; }
      if (field.mapKind === 'message') for (const pair of value.items) checkShape(pair.value, field.message, `${path}/${escape(isScalar(pair.key) ? String(pair.key.value) : '')}`, problems);
      return;
    case 'list':
      if (!isSeq(value)) problems.push({ ...nodeRange(value), message: 'expected a list, such as [a, b]', severity: 'error', source: 'shape', path });
      return;
    default:
      if (!isScalar(value)) problems.push({ ...nodeRange(value), message: `expected a ${field.fieldKind === 'enum' ? 'name' : field.scalar === ScalarType.STRING ? 'string' : 'value'}`, severity: 'error', source: 'shape', path });
  }
}

function nodeRange(node: unknown): { from: number; to: number } {
  const range = (node as Node | undefined)?.range;
  return range ? { from: range[0], to: Math.max(range[1], range[0] + 1) } : { from: 0, to: 1 };
}

export const escape = (key: string) => key.replaceAll('~', '~0').replaceAll('/', '~1');
export const unescape = (segment: string) => segment.replaceAll('~1', '/').replaceAll('~0', '~');
export const pointer = (...keys: string[]) => keys.map((key) => `/${escape(key)}`).join('');
const snake = (key: string) => key.replace(/[A-Z]/g, (letter) => `_${letter.toLowerCase()}`);

/** The node at a JSON Pointer: its pair (for a mapping entry) and value; missing: the deepest parent found. */
export function nodeAt(doc: Document, path: string): { pair?: Pair; node?: unknown; found: boolean } {
  let current: unknown = doc.contents, pair: Pair | undefined;
  for (const segment of path.split('/').slice(1).map(unescape)) {
    if (isMap(current)) {
      const next = current.items.find((item) => isScalar(item.key) && (String(item.key.value) === segment || String(item.key.value) === snake(segment)));
      if (!next) return { pair, node: current, found: false };
      pair = next; current = next.value;
    } else if (isSeq(current) && /^\d+$/.test(segment)) {
      const item = current.items[Number(segment)];
      if (item === undefined) return { pair, node: current, found: false };
      pair = undefined; current = item;
    } else return { pair, node: current, found: false };
  }
  return { pair, node: current, found: true };
}

/** Where a server violation is in the text: its expression range, else its node (a missing one: its parent's key). */
export function locate(draft: Draft, path: string, expr?: { start: number; end: number }): { from: number; to: number } {
  const { pair, node, found } = nodeAt(draft.doc, path);
  if (found && expr && isScalar(node) && typeof node.value === 'string') {
    const mapped = exprRange(draft.text, node, expr.start, expr.end);
    if (mapped) return mapped;
  }
  const target = found && node != null && !(isScalar(node) && node.value == null) ? node : pair?.key ?? node;
  const range = (target as Node | undefined)?.range;
  if (!range) { const end = draft.text.indexOf('\n'); return { from: 0, to: end > 0 ? end : Math.min(1, draft.text.length) }; }
  return { from: range[0], to: Math.max(range[1], range[0] + 1) };
}

/** UTF-16 offsets in the text of code points start..end of a string scalar's value; undefined when the scalar is not mappable. */
export function exprRange(text: string, scalar: Scalar, start: number, end: number): { from: number; to: number } | undefined {
  const range = scalar.range, value = String(scalar.value);
  if (!range) return undefined;
  const offsets = valueOffsets(text.slice(range[0], range[1]), value, scalar.type);
  if (!offsets) return undefined;
  const units = codeUnits(value);
  const at = (point: number) => { const unit = units[Math.min(point, units.length - 1)] ?? value.length; return range[0] + (offsets[unit] ?? offsets[offsets.length - 1] ?? 0); };
  const from = at(start), to = end > start ? at(end - 1) + 1 : from + 1;
  return { from, to: Math.max(to, from + 1) };
}

/** UTF-16 index of every code point of s, plus its length. */
function codeUnits(s: string): number[] {
  const out: number[] = []; let index = 0;
  for (const char of s) { out.push(index); index += char.length; }
  out.push(index);
  return out;
}

/** For each UTF-16 unit of value, its offset in the scalar's source; undefined for forms not mapped (block scalars, folded lines). */
function valueOffsets(source: string, value: string, type: Scalar['type']): number[] | undefined {
  const out: number[] = [];
  if (type === 'PLAIN') { if (source !== value) return undefined; for (let i = 0; i <= value.length; i++) out.push(i); return out; }
  if (type !== 'QUOTE_SINGLE' && type !== 'QUOTE_DOUBLE') return undefined;
  let at = 1;
  for (let i = 0; i < value.length; i++) {
    out.push(at);
    if (type === 'QUOTE_SINGLE') at += source[at] === "'" ? 2 : 1;
    else if (source[at] === '\\') {
      const next = source[at + 1], high = value.charCodeAt(i) >= 0xd800 && value.charCodeAt(i) < 0xdc00;
      // An escaped astral character is two UTF-16 units of the value from one escape.
      if (next === 'U' && high) { out.push(at); i++; }
      at += next === 'x' ? 4 : next === 'u' ? 6 : next === 'U' ? 10 : 2;
    }
    else at += 1;
    if (at > source.length) return undefined;
  }
  out.push(at);
  return out;
}

/** The JSON Pointer of the deepest node at a text offset, and whether the offset is on a key. */
export function pathAt(doc: Document, offset: number): { path: string; onKey: boolean } {
  let path = '', current: unknown = doc.contents, onKey = false;
  for (;;) {
    if (isMap(current)) {
      const pair = current.items.find((item) => within(item, offset));
      if (!pair || !isScalar(pair.key)) break;
      path += `/${escape(String(pair.key.value))}`;
      if (pair.key.range && offset <= pair.key.range[1]) { onKey = true; break; }
      current = pair.value;
    } else if (isSeq(current)) {
      const index = current.items.findIndex((item) => { const range = (item as Node | undefined)?.range; return !!range && offset >= range[0] && offset <= range[1]; });
      if (index < 0) break;
      path += `/${index}`; current = current.items[index];
    } else break;
  }
  return { path, onKey };
}

function within(pair: Pair, offset: number): boolean {
  if (!isPair(pair)) return false;
  const start = (pair.key as Node | undefined)?.range?.[0], end = (pair.value as Node | undefined)?.range?.[2] ?? (pair.key as Node | undefined)?.range?.[2];
  return start !== undefined && end !== undefined && offset >= start && offset <= end;
}

/** One change of a draft: the value at path (keys and indexes), undefined to remove it. */
export interface Change { path: readonly (string | number)[]; value: unknown }

/** The draft text with the value at path set; undefined: removed. Keeps the rest of the text as written. */
export function edit(draft: Draft, path: readonly (string | number)[], value: unknown): string {
  return editAll(draft, [{ path, value }]);
}

/**
 * The draft text with every change applied in order, on one copy of the
 * document: comments and layout of untouched parts stay. A scalar in the way
 * of a path (an empty `steps:`, an input written as one expression) becomes a
 * mapping.
 */
export function editAll(draft: Draft, changes: readonly Change[]): string {
  const doc = draft.doc.clone();
  for (const change of changes) {
    if (change.value === undefined) { if (doc.hasIn(change.path)) doc.deleteIn(change.path); continue; }
    for (let depth = 1; depth < change.path.length; depth++) {
      const prefix = change.path.slice(0, depth), node = doc.getIn(prefix, true);
      if (node !== undefined && !isMap(node) && !isSeq(node)) doc.setIn(prefix, doc.createNode({}));
    }
    doc.setIn(change.path, doc.createNode(change.value));
  }
  compact(doc);
  return doc.toString({ lineWidth: 0 });
}

const reservedNames = new Set(['req', 'event', 'meta', 'steps', 'true', 'false', 'null', 'in', 'as', 'break', 'const', 'continue', 'else', 'for', 'function', 'if', 'import', 'let', 'loop', 'package', 'namespace', 'return', 'var', 'void', 'while', 'has', 'dyn', 'int', 'uint', 'double', 'bool', 'string', 'bytes', 'list', 'map', 'type', 'null_type', 'timestamp', 'duration', 'optional', 'math', 'strings', 'base64', 'lists', 'on', 'when']);
/** A step name valid as a CEL variable: an identifier, not reserved. */
export const validStepName = (name: string) => /^[A-Za-z_][A-Za-z0-9_]*$/.test(name) && !reservedNames.has(name);

/** A free step name for an activity: its name in lowerCamelCase, numbered when taken. */
export function stepNameFor(activity: string, taken: readonly string[]): string {
  const bare = (activity.split('.').pop() ?? 'step').replace(/[^A-Za-z0-9_]/g, '');
  let base = bare ? bare[0]!.toLowerCase() + bare.slice(1) : 'step';
  if (!validStepName(base)) base = `${base}Step`;
  let name = base;
  for (let n = 2; taken.includes(name) || !validStepName(name); n++) name = `${base}${n}`;
  return name;
}

/**
 * A step's id in the graph and the editor's layout: its name, or
 * "<for-each step>/<name>" for a step of a body (nested bodies nest).
 */
export const stepPathOf = (id: string): string[] => id.split('/').flatMap((name) => ['steps', name]);

/** The step with an id, and the steps it sits among. */
export function stepAt(definition: Definition | undefined, id: string): { step?: api.Step; siblings: Record<string, api.Step> } {
  let siblings: Record<string, api.Step> = definition?.steps ?? {}, step: api.Step | undefined;
  const names = id.split('/');
  names.forEach((name, position) => {
    step = siblings[name];
    if (position < names.length - 1) siblings = step?.steps ?? {};
  });
  return { step, siblings };
}

/** Changes that add a step of an activity at a canvas position, in a for-each step's body when parent is set. */
export function addStep(definition: Definition | undefined, activity: string, at: { x: number; y: number }, parent?: string): { name: string; id: string; changes: Change[] } {
  const siblings = parent ? stepAt(definition, parent).step?.steps ?? {} : definition?.steps ?? {};
  const name = stepNameFor(activity, Object.keys(siblings)), id = parent ? `${parent}/${name}` : name;
  return { name, id, changes: [{ path: stepPathOf(id), value: { activity } }, { path: ['editor', 'nodes', id], value: { x: Math.round(at.x), y: Math.round(at.y) } }] };
}

/** Changes that remove a step: the step, its position, and its name from its siblings' `after`. */
export function removeStep(definition: Definition | undefined, id: string): Change[] {
  const { siblings } = stepAt(definition, id), name = id.split('/').at(-1)!, base = stepPathOf(id).slice(0, -2);
  const changes: Change[] = [{ path: stepPathOf(id), value: undefined }, { path: ['editor', 'nodes', id], value: undefined }];
  for (const [other, step] of Object.entries(siblings)) {
    if (other !== name && step.after.includes(name)) changes.push({ path: [...base, 'steps', other, 'after'], value: step.after.filter((entry) => entry !== name) });
  }
  return changes;
}
