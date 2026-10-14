import type { EditorCompletion, EditorCompletionResult } from '@backplane-labs/backplane-editors';
import type * as api from '@backplane-labs/backplane-api';
import { framesOf, scopeAt, type Scope, type StepLike, type WiringIndex } from './catalog.js';
import type { Definition, WiringKind } from './document.js';
import { shapeAt, shapeLabel, shapeOf, type Shape } from './shape.js';

const rootKeys: Record<WiringKind, [string, string][]> = {
  binding: [['hook', 'hook it implements'], ['description', 'text for people'], ['steps', 'calls of activities'], ['result', 'the hook output'], ['editor', 'graph layout']],
  rule: [['event', 'event it reacts to'], ['description', 'text for people'], ['when', 'CEL filter over event, meta'], ['steps', 'calls of activities'], ['editor', 'graph layout']],
};
const stepKeys: [string, string][] = [
  ['activity', '<service>.<Activity>'], ['input', 'activity input'], ['when', 'CEL bool: run only when true'], ['after', 'steps to run after'],
  ['undo', 'compensating activity'], ['undoInput', 'undo activity input'], ['retry', 'retry policy'], ['startToClose', 'attempt timeout, e.g. 30s'],
  ['heartbeat', 'heartbeat timeout'], ['description', 'text for people'], ['forEach', 'CEL list or map: run per item'],
];
const forEachKeys: [string, string][] = [
  ['as', 'item variable (default item)'], ['concurrency', 'items at once (default 10)'], ['onError', 'fail | continue'],
  ['maxItems', 'most items (default 1000)'], ['steps', 'a sub-flow per item'], ['result', 'each item\'s result'],
];
const retryKeys: [string, string][] = [['attempts', 'total attempts'], ['initialInterval', 'e.g. 1s'], ['maxInterval', 'e.g. 30s'], ['backoff', 'multiplier ≥ 1']];
const functions = ['size', 'has', 'int', 'uint', 'double', 'string', 'bool', 'timestamp', 'duration', 'matches', 'startsWith', 'endsWith', 'contains', 'lowerAscii', 'upperAscii', 'trim', 'split', 'join', 'replace', 'format', 'map', 'filter', 'exists', 'all'];

/** The definition's keys along the indentation above a line: the path a key typed there belongs to. */
export function parentKeys(text: string, lineStart: number, indent: number): string[] {
  const keys: string[] = [];
  let want = indent, end = lineStart - 1;
  while (end > 0 && want > 0) {
    const start = text.lastIndexOf('\n', end - 1) + 1, line = text.slice(start, end);
    end = start - 1;
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) continue;
    const at = line.search(/\S/);
    if (at >= want) continue;
    const key = /^\s*(?:-\s+)?([\w$-]+)\s*:/.exec(line)?.[1];
    if (key) keys.unshift(key);
    want = at;
  }
  return keys;
}

export interface CompletionContext {
  kind: WiringKind; index: WiringIndex; definition?: Definition; text: string; position: number;
}

/** Suggestions at a position of a draft's YAML. */
export function completeYAML({ kind, index, definition, text, position }: CompletionContext): EditorCompletionResult | null {
  const lineStart = text.lastIndexOf('\n', position - 1) + 1, before = text.slice(lineStart, position);
  const indent = before.search(/\S|$/);
  const listItem = /^(\s*)-\s*([\w.]*)$/.exec(before);
  const keyOnly = /^(\s*)([\w]*)$/.exec(before);
  const keyValue = /^(\s*)(?:-\s+)?([\w]+)\s*:\s*(.*)$/.exec(before);
  const steps = stepsOf(definition);
  const siblings = (path: readonly string[]) => { const frames = framesOf(path, steps); return frames.chain.length > 1 ? frames.chain.at(-2)?.step?.steps ?? {} : steps; };
  if (listItem) {
    const path = parentKeys(text, lineStart, listItem[1]!.length);
    if (path.at(-1) === 'after') { const around = siblings(path.slice(0, -1)); return { from: position - listItem[2]!.length, options: Object.keys(around).map((name) => ({ label: name, type: 'variable', detail: around[name]?.activity })) }; }
    return null;
  }
  if (keyOnly) {
    const path = parentKeys(text, lineStart, indent);
    const options = keysFor(kind, index, path, steps, definitionSource(kind, definition));
    return options.length ? { from: position - keyOnly[2]!.length, options } : null;
  }
  if (!keyValue) return null;
  const path = [...parentKeys(text, lineStart, keyValue[1]!.length), keyValue[2]!], rest = keyValue[3]!;
  const key = path.at(-1)!, frames = framesOf(path.slice(0, -1), steps);
  const inStep = frames.chain.length > 0 && frames.rest.length === 0 && !frames.inStepsMap;
  const word = /[\w.<>-]*$/.exec(rest)![0];
  const names = (list: Map<string, { value: { description: string } }>) => [...list.entries()].map(([full, entry]) => ({ label: full, type: 'function' as const, detail: entry.value.description || undefined }));
  if (path.length === 1 && key === 'hook') return { from: position - word.length, options: names(index.hooks) };
  if (path.length === 1 && key === 'event') return { from: position - word.length, options: names(index.events) };
  if (inStep && (key === 'activity' || key === 'undo')) return { from: position - word.length, options: names(index.activities) };
  if (inStep && key === 'onError') { const partial = /\w*$/.exec(rest)![0]; return { from: position - partial.length, options: ['fail', 'continue'].map((label) => ({ label, type: 'keyword' as const })) }; }
  if (inStep && key === 'after') { const partial = /[\w]*$/.exec(rest)![0], around = siblings(path.slice(0, -1)); return { from: position - partial.length, options: Object.keys(around).map((name) => ({ label: name, type: 'variable' })) }; }
  const exprFrames = framesOf(path, steps), first = exprFrames.rest[0];
  const cel = (path[0] === 'result' && path.length >= 1) || (exprFrames.chain.length > 0 && ['input', 'undoInput', 'when', 'forEach', 'result'].includes(first ?? '')) || (path.length === 1 && key === 'when');
  if (!cel) return null;
  const source = definitionSource(kind, definition);
  if (!source) return null;
  return completeCEL(rest, position, scopeAt(index, source, steps, path, kind));
}

/** Keys a mapping at path may hold. */
function keysFor(kind: WiringKind, index: WiringIndex, path: readonly string[], steps: Record<string, StepLike | undefined>, source?: { hook: string } | { event: string }): EditorCompletion[] {
  const toOptions = (entries: [string, string][]) => entries.map(([label, detail]) => ({ label, detail, type: 'property' as const, apply: `${label}: ` }));
  if (!path.length) return toOptions(rootKeys[kind]);
  const frames = framesOf(path, steps);
  if (frames.inStepsMap) return [];
  const step = frames.chain.at(-1)?.step as (StepLike & { undo?: string }) | undefined;
  if (frames.chain.length && !frames.rest.length) return toOptions(step?.forEach ? [...stepKeys, ...forEachKeys] : stepKeys);
  if (frames.chain.length && frames.rest.length === 1 && frames.rest[0] === 'retry') return toOptions(retryKeys);
  let shape: Shape | undefined;
  if (frames.chain.length && (frames.rest[0] === 'input' || frames.rest[0] === 'undoInput')) {
    const activity = frames.rest[0] === 'input' ? step?.activity : step?.undo;
    shape = shapeAt(shapeOf(activity ? index.activities.get(activity)?.value.input : undefined), frames.rest.slice(1));
  } else if (!frames.chain.length && path[0] === 'result' && source && 'hook' in source) shape = shapeAt(shapeOf(index.hooks.get(source.hook)?.value.output), path.slice(1));
  return fieldOptions(shape).map((option) => ({ ...option, apply: `${option.label}: ` }));
}

function fieldOptions(shape: Shape | undefined): EditorCompletion[] {
  return (shape?.fields ?? []).map((field) => ({ label: field.name, type: 'property', detail: `${shapeLabel(field.shape)}${field.required ? ' · required' : ''}${field.description ? ` · ${field.description}` : ''}` }));
}

/** CEL suggestions at the end of `before`: variables, or the fields of the value a select chain reads. */
export function completeCEL(before: string, position: number, scope: Scope): EditorCompletionResult | null {
  const chain = /([A-Za-z_]\w*(?:\s*\.\s*[A-Za-z_]\w*)*)\s*\.\s*(\w*)$/.exec(before);
  if (chain) {
    const [root, ...fields] = chain[1]!.split('.').map((part) => part.trim());
    const variable = scope.variables.get(root!);
    if (!variable) return null;
    const shape = shapeAt(variable.shape, fields);
    return { from: position - chain[2]!.length, options: fieldOptions(shape) };
  }
  const partial = /(\w*)$/.exec(before)![1]!;
  if (/\d/.test(partial[0] ?? '')) return null;
  return { from: position - partial.length, options: [
    ...[...scope.variables.entries()].map(([name, entry]) => ({ label: name, type: 'variable' as const, detail: `${shapeLabel(entry.shape)} · ${entry.detail}` })),
    ...functions.map((name) => ({ label: name, type: 'function' as const })),
  ] };
}

export function stepsOf(definition?: Definition): Record<string, StepLike | undefined> {
  return definition?.steps ?? {};
}

export function definitionSource(kind: WiringKind, definition?: Definition): { hook: string } | { event: string } | undefined {
  if (!definition) return undefined;
  return kind === 'binding' ? { hook: (definition as api.BindingDefinition).hook } : { event: (definition as api.RuleDefinition).event };
}
