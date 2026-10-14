import { useCallback, useMemo } from 'react';
import { create } from '@bufbuild/protobuf';
import * as api from '@backplane-labs/backplane-api';
import { useClient, useSnapshotWatch } from '@backplane-labs/backplane-react';
import { usePlatformQuery } from '../runtime.js';
import { dyn, shapeAt, shapeOf, type Shape } from './shape.js';

/** Manifest contracts as the wiring catalog carries them. */
export type Hook = api.WiringContract['hooks'][number];
export type Activity = api.WiringContract['activities'][number];
export type Event = api.WiringContract['events'][number];

/** One contract of the catalog by its full name ("<service>.<Name>"). */
export interface Contract<T> { full: string; service: string; name: string; value: T }

/** The wiring catalog indexed for the editor: every hook, activity and event by full name. */
export interface WiringIndex {
  services: api.WiringContract[];
  hooks: Map<string, Contract<Hook>>;
  activities: Map<string, Contract<Activity>>;
  events: Map<string, Contract<Event>>;
}

export const emptyIndex: WiringIndex = { services: [], hooks: new Map(), activities: new Map(), events: new Map() };

export function indexCatalog(services: readonly api.WiringContract[]): WiringIndex {
  const index: WiringIndex = { services: [...services], hooks: new Map(), activities: new Map(), events: new Map() };
  for (const contract of services) {
    const add = <T extends { name: string }>(into: Map<string, Contract<T>>, list: readonly T[]) => {
      for (const value of list) into.set(`${contract.service}.${value.name}`, { full: `${contract.service}.${value.name}`, service: contract.service, name: value.name, value });
    };
    add(index.hooks, contract.hooks); add(index.activities, contract.activities); add(index.events, contract.events);
  }
  return index;
}

/**
 * The catalog, reloaded whenever the installation's catalog changes (a deploy
 * changes contracts, which is also when saved wiring may break).
 */
export function useWiringCatalog() {
  const client = useClient(api.WiringServiceClient), catalog = useClient(api.CatalogServiceClient);
  const watch = useSnapshotWatch(useCallback((signal: AbortSignal) => catalog.watchCatalog(create(api.WatchCatalogRequestSchema), { signal }), [catalog]));
  const stamp = watch.value?.services.map((service) => `${service.name}@${service.latestVersion}`).join(',') ?? '';
  const state = usePlatformQuery(`wiring-catalog:${stamp}`, (signal) => client.getWiringCatalog(create(api.GetWiringCatalogRequestSchema), { signal }));
  const index = useMemo(() => state.value ? indexCatalog(state.value.services) : emptyIndex, [state.value]);
  return { index, loading: state.loading && !state.value, error: state.error };
}

/** Shapes of what expressions read, from the catalog and the definition's steps. */
export interface Scope {
  /** Variables by name: req / event and meta, steps, every step, and in a for-each body the item. */
  variables: Map<string, { shape: Shape; detail: string }>;
}

/** The part of a step the scope needs. */
export interface StepLike { activity: string; forEach?: string; as?: string; steps?: Record<string, StepLike | undefined> }

const metaShape: Shape = { kind: 'object', fields: ['id', 'source', 'subject', 'type', 'time'].map((name) => ({ name, shape: { kind: 'string' }, required: true })) };
const stateShape = (forEach: boolean): Shape => ({ kind: 'object', fields: [
  { name: 'skipped', shape: { kind: 'bool' }, required: true },
  ...(forEach ? [{ name: 'failed', shape: { kind: 'int' as const }, required: true }, { name: 'errors', shape: { kind: 'list' as const, elem: { kind: 'object' as const, fields: [{ name: 'index', shape: { kind: 'int' as const }, required: true }, { name: 'message', shape: { kind: 'string' as const }, required: true }] } }, required: true }] : []),
] });
export const itemVar = (step: StepLike) => step.as || 'item';

/** The output of a step: its activity's output, a list of them for a for-each step (of dyn for a body). */
export function outputShape(index: WiringIndex, step: StepLike | undefined): Shape {
  const out = step?.activity ? shapeOf(index.activities.get(step.activity)?.value.output) : dyn;
  if (!step?.forEach) return out;
  return { kind: 'list', elem: Object.keys(step.steps ?? {}).length ? dyn : out };
}

/** The scope of a binding (hook) or rule (event) with its steps' activities. */
export function scopeOf(index: WiringIndex, source: { hook: string } | { event: string }, steps: Record<string, StepLike | undefined>, forRuleWhen = false): Scope {
  const variables: Scope['variables'] = new Map();
  if ('hook' in source) variables.set('req', { shape: shapeOf(index.hooks.get(source.hook)?.value.input), detail: `${source.hook} input` });
  else {
    variables.set('event', { shape: shapeOf(index.events.get(source.event)?.value.schema), detail: source.event });
    variables.set('meta', { shape: metaShape, detail: 'CloudEvents attributes' });
  }
  if (forRuleWhen) return { variables };
  return withSteps(index, { variables }, steps);
}

/** scope with steps added: their outputs and their states in `steps`. */
function withSteps(index: WiringIndex, scope: Scope, steps: Record<string, StepLike | undefined>): Scope {
  const variables = new Map(scope.variables);
  const names = Object.keys(steps).sort();
  const states = variables.get('steps')?.shape.fields ?? [];
  variables.set('steps', { shape: { kind: 'object', fields: [...states.filter((field) => !names.includes(field.name)),
    ...names.map((name) => ({ name, shape: stateShape(!!steps[name]?.forEach), required: true }))] }, detail: 'step states' });
  for (const name of names) {
    const step = steps[name];
    variables.set(name, { shape: outputShape(index, step), detail: `${step?.forEach ? 'list of ' : ''}${step?.activity || 'step'} output` });
  }
  return { variables };
}

/** The shape of what a for-each step's list gives per item, when its expression is a plain select chain. */
export function itemShape(expression: string | undefined, scope: Scope): Shape {
  const chain = /^\s*([A-Za-z_]\w*(?:\s*\.\s*[A-Za-z_]\w*)*)\s*$/.exec(expression ?? '');
  if (!chain) return dyn;
  const [root, ...fields] = chain[1]!.split('.').map((part) => part.trim());
  const shape = shapeAt(scope.variables.get(root!)?.shape ?? dyn, fields);
  if (shape?.kind === 'list') return shape.elem ?? dyn;
  if (shape?.kind === 'map') return { kind: 'object', fields: [{ name: 'key', shape: { kind: 'string' }, required: true }, { name: 'value', shape: shape.elem ?? dyn, required: true }] };
  return dyn;
}

/** scope inside a for-each step: its item and the item's position. */
export function withItem(scope: Scope, step: StepLike): Scope {
  const variables = new Map(scope.variables), name = itemVar(step);
  variables.set(name, { shape: itemShape(step.forEach, scope), detail: 'the item' });
  variables.set(`${name}Index`, { shape: { kind: 'int' }, detail: 'the item\'s position' });
  return { variables };
}

/** The for-each steps a definition path is nested in, then the step it is on, and the rest of the path. */
export interface PathFrames { chain: { name: string; step?: StepLike }[]; rest: string[]; inStepsMap: boolean }
export function framesOf(path: readonly string[], steps: Record<string, StepLike | undefined>): PathFrames {
  const chain: PathFrames['chain'] = [];
  let at = 0, current: Record<string, StepLike | undefined> | undefined = steps;
  while (path[at] === 'steps') {
    const name = path[at + 1];
    if (name === undefined) return { chain, rest: [], inStepsMap: true };
    const step: StepLike | undefined = current?.[name];
    chain.push({ name, step });
    if (path[at + 2] === 'steps' && step?.forEach) { current = step.steps; at += 2; if (path[at + 1] === undefined) return { chain, rest: [], inStepsMap: true }; continue; }
    return { chain, rest: path.slice(at + 2), inStepsMap: false };
  }
  return { chain, rest: path.slice(at), inStepsMap: false };
}

/**
 * The scope an expression at a path sees: the definition's steps, and for
 * every for-each step it is in (or on, for its input, when and result) the
 * item and that body's steps.
 */
export function scopeAt(index: WiringIndex, source: { hook: string } | { event: string }, steps: Record<string, StepLike | undefined>, path: readonly string[], kind: 'binding' | 'rule'): Scope {
  if (kind === 'rule' && path.length === 1 && path[0] === 'when') return scopeOf(index, source, {}, true);
  const frames = framesOf(path, steps);
  let scope = scopeOf(index, source, steps);
  frames.chain.forEach(({ step }, position) => {
    if (!step?.forEach) return;
    const last = position === frames.chain.length - 1;
    // On the step itself, only its input, when, undo input and result see the item; its list does not.
    if (last && !['input', 'when', 'undoInput', 'result'].includes(frames.rest[0] ?? '')) return;
    scope = withItem(scope, step);
    if (step.steps && (!last || frames.rest[0] === 'result')) scope = withSteps(index, scope, step.steps);
  });
  return scope;
}
