import { memo, useCallback, useEffect, useMemo, useState, type DragEvent } from 'react';
import { applyNodeChanges, Background, BackgroundVariant, Controls, Handle, MarkerType, MiniMap, Position, ReactFlow, ReactFlowProvider, useReactFlow, type Connection, type Edge, type Node, type NodeChange, type NodeProps } from '@xyflow/react';
import { toJson } from '@bufbuild/protobuf';
import { ValueSchema } from '@bufbuild/protobuf/wkt';
import * as api from '@backplane-labs/backplane-api';
import { Badge, EmptyState, StatusBadge, type StatusTone } from '@backplane-labs/backplane-ui';
import { CircleAlert, Clock, Filter, Flag, Play, Repeat, RotateCcw, Undo2 } from 'lucide-react';
import { usePlatformText } from '../locales.js';
import { enumLabel } from '../format.js';
import { addStep, definitionSchema, editAll, removeStep, stepAt, stepPathOf, unescape, type Change, type Definition, type Draft, type DraftProblem, type WiringKind } from './document.js';
import { stepTone } from './history.js';
import { shapeLabel, shapeOf, type Shape } from './shape.js';
import type { WiringIndex } from './catalog.js';

/** Drag data of an activity dragged from the palette. */
export const activityDragType = 'application/x-backplane-activity';

const palette = ['var(--chart-1)', 'var(--chart-2)', 'var(--chart-3)', 'var(--chart-4)', 'var(--chart-5)'];
/** The color of a service, as on the system map: stable per name. */
export function serviceColor(service: string): string {
  let hash = 0;
  for (const char of service) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return palette[hash % palette.length]!;
}
const owner = (full: string) => full.slice(0, Math.max(0, full.indexOf('.'))) || full;

/** A port: one field of a step's input or output (or the whole value). */
interface Port { id: string; label: string; type: string; expr?: string; required?: boolean; problem?: boolean }
/** What a run did to a node: one call's status, or an item count for a for-each step or a step of a body. */
interface RunMark { status: api.StepRunStatus; ms?: number; undone: boolean; items?: { ok: number; failed: number; total: number } }
interface Loop { expr: string; item: string; badges: string[] }
type StepData = {
  kind: 'step'; id: string; name: string; activity: string; color: string; inputs: Port[]; outputs: Port[]; when?: string; undo?: string;
  retry?: string; problems: number; selected: boolean; run?: RunMark; loop?: Loop;
};
type LoopData = { kind: 'loop'; id: string; name: string; color: string; loop: Loop; problems: number; selected: boolean; run?: RunMark; width: number; height: number };
type EndData = { kind: 'trigger' | 'result'; title: string; subtitle: string; color: string; ports: Port[]; selected: boolean; problems: number; when?: string };
type GraphNode = Node<StepData, 'step'> | Node<LoopData, 'loop'> | Node<EndData, 'end'>;

const TRIGGER = '$trigger', RESULT = '$result';
const WIDTH = 272, COLUMN = 340, PAD = 24, LOOP_HEAD = 64;

function ports(shape: Shape, prefix: 'in' | 'out', values?: Record<string, unknown>, problems?: (field: string) => boolean): Port[] {
  const fields = shape.kind === 'object' ? shape.fields ?? [] : [];
  const out: Port[] = fields.map((field) => ({
    id: `${prefix}:${field.name}`, label: field.name, type: shapeLabel(field.shape), required: field.required,
    expr: values ? describe(values[field.name]) : undefined, problem: problems?.(field.name),
  }));
  // Fields the value sets that the schema does not declare stay visible (the problem says why).
  if (values) for (const name of Object.keys(values)) if (!fields.some((field) => field.name === name)) out.push({ id: `${prefix}:${name}`, label: name, type: '?', expr: describe(values[name]), problem: problems?.(name) ?? true });
  return out;
}

/** A value as a port shows it: an expression as written, a literal as JSON. */
function describe(value: unknown): string | undefined {
  if (value === undefined) return undefined;
  if (typeof value === 'string') return value;
  return JSON.stringify(value);
}

function valueObject(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

/** A google.protobuf.Value as plain JSON. */
function apiJson(value: api.BindingDefinition['result']): unknown { return value ? toJson(ValueSchema, value) : undefined; }

/** The value at a path of the definition's JSON, as written (expressions as strings). */
function valueAt(kind: WiringKind, definition: Definition, path: readonly string[]): unknown {
  let current: unknown = toJson(definitionSchema(kind), definition);
  for (const key of path) current = current && typeof current === 'object' ? (current as Record<string, unknown>)[key] : undefined;
  return current;
}

const millis = (at?: { seconds: bigint; nanos: number }) => at ? Number(at.seconds) * 1000 + at.nanos / 1e6 : undefined;

/** Run marks by node id: a step's call, or the item counts of a for-each step and of every step of its body. */
function runMarks(run: api.StepRun[] | undefined): Map<string, RunMark> {
  const marks = new Map<string, RunMark>(), items = new Map<string, Map<number, boolean>>();
  const count = (id: string, item: number, ok: boolean) => {
    const seen = items.get(id) ?? new Map<number, boolean>();
    seen.set(item, (seen.get(item) ?? true) && ok);
    items.set(id, seen);
  };
  for (const call of run ?? []) {
    const id = call.parent ? `${call.parent}/${call.step}` : call.step;
    if (call.undo) { const mark = marks.get(id); if (mark) mark.undone = true; continue; }
    const ok = call.status === api.StepRunStatus.COMPLETED;
    if (call.item !== undefined) {
      count(id, call.item, ok);
      if (call.parent) count(call.parent, call.item, ok);
    }
    const start = millis(call.startedTime), end = millis(call.closeTime);
    marks.set(id, { status: call.status, ms: start !== undefined && end !== undefined ? end - start : undefined, undone: false });
  }
  for (const [id, seen] of items) {
    const values = [...seen.values()], failed = values.filter((ok) => !ok).length;
    marks.set(id, { ...marks.get(id) ?? { undone: false }, status: failed ? api.StepRunStatus.FAILED : api.StepRunStatus.COMPLETED, items: { ok: values.length - failed, failed, total: values.length } });
  }
  return marks;
}

export interface WiringGraphProps {
  kind: WiringKind; definition?: Definition; draft: Draft; analysis?: api.Analysis; index: WiringIndex; mode: 'dark' | 'light';
  problems: DraftProblem[]; run?: api.StepRun[]; selected?: string; onSelect: (node: string | undefined) => void;
  /** A graph edit: the draft's new text. */
  onText: (text: string) => void;
  /** Show the mini-map. */
  minimap?: boolean;
}

export function WiringGraph(props: WiringGraphProps) {
  return <ReactFlowProvider><Graph {...props} /></ReactFlowProvider>;
}

/** Node ids and what reads resolve to: sibling steps, then enclosing frames', then items. */
interface Frame { prefix: string; names: Set<string>; loop?: { id: string; item: string }; parent?: Frame }

function resolve(frame: Frame | undefined, name: string): { id: string; handle?: string } | undefined {
  for (let at = frame; at; at = at.parent) {
    if (at.names.has(name)) return { id: at.prefix + name };
    if (at.loop && at.loop.item === name) return { id: at.loop.id, handle: 'item' };
  }
  return undefined;
}

function Graph({ kind, definition, draft, analysis, index, mode, problems, run, selected, onSelect, onText, minimap }: WiringGraphProps) {
  const text = usePlatformText(), flow = useReactFlow();
  const editable = !draft.problems.some((problem) => problem.source === 'yaml' && problem.severity === 'error');
  const apply = useCallback((changes: Change[]) => { if (editable) onText(editAll(draft, changes)); }, [draft, editable, onText]);

  const graph = useMemo(() => {
    if (!definition) return { nodes: [] as GraphNode[], edges: [] as Edge[] };
    const levels = new Map((analysis?.steps ?? []).map((step) => [step.parent ? `${step.parent}/${step.name}` : step.name, step.level]));
    const levelOf = (id: string) => levels.get(id) ?? -1;
    const problemAt = (prefix: string) => problems.filter((problem) => problem.path === prefix || problem.path?.startsWith(`${prefix}/`)).length;
    const marks = runMarks(run);
    const saved = definition.editor?.nodes ?? {};
    const nodes: GraphNode[] = [], frames = new Map<string, Frame>(), sizes = new Map<string, { w: number; h: number }>();
    const top: Frame = { prefix: '', names: new Set(Object.keys(definition.steps)) };
    frames.set('', top);

    const loopOf = (step: api.Step): Loop => ({
      expr: step.forEach, item: step.as || 'item',
      badges: [`×${step.concurrency || 10}`, ...(step.onError === 'continue' ? [text('continueOnError')] : []), ...(step.maxItems ? [`≤${step.maxItems}`] : [])],
    });

    /** Nodes of one frame's steps, laid out by level (saved positions win); a loop's children follow it. */
    const build = (steps: Record<string, api.Step>, frame: Frame, path: string, origin: { x: number; y: number }): GraphNode[] => {
      const built: { id: string; node: GraphNode; children: GraphNode[] }[] = [];
      for (const name of Object.keys(steps).sort()) {
        const step = steps[name]!, id = frame.prefix + name, stepPath = `${path}/steps/${name}`;
        if (step.forEach && Object.keys(step.steps).length) {
          const inner: Frame = { prefix: `${id}/`, names: new Set(Object.keys(step.steps)), loop: { id, item: step.as || 'item' }, parent: frame };
          frames.set(id, inner);
          const children = build(step.steps, inner, stepPath, { x: PAD, y: LOOP_HEAD });
          const direct = children.filter((child) => child.parentId === id);
          const width = Math.max(WIDTH + 2 * PAD, ...direct.map((child) => child.position.x + (sizes.get(child.id)?.w ?? WIDTH) + PAD));
          const height = Math.max(LOOP_HEAD + 80, ...direct.map((child) => child.position.y + (sizes.get(child.id)?.h ?? 120) + PAD));
          sizes.set(id, { w: width, h: height });
          const own = problemAt(stepPath) - Object.keys(step.steps).reduce((sum, child) => sum + problemAt(`${stepPath}/steps/${child}`), 0);
          const data: LoopData = { kind: 'loop', id, name, color: serviceColor(owner(Object.values(step.steps)[0]?.activity ?? name)), loop: loopOf(step),
            problems: own, selected: selected === id, run: marks.get(id), width, height };
          built.push({ id, node: { id, type: 'loop', position: { x: 0, y: 0 }, data, style: { width, height } }, children });
          continue;
        }
        const activity = index.activities.get(step.activity)?.value;
        const input = step.input?.kind.case === 'structValue' ? valueObject(apiJson(step.input)) : undefined;
        const inputs = ports(shapeOf(activity?.input), 'in', input ?? {}, (field) => problemAt(`${stepPath}/input/${field}`) > 0);
        if (step.input && step.input.kind.case !== 'structValue') inputs.unshift({ id: 'in:*', label: text('wholeInput'), type: activity?.input ? 'object' : 'dyn', expr: describe(apiJson(step.input)) });
        else if (!activity?.input) inputs.unshift({ id: 'in:*', label: text('wholeInput'), type: 'dyn' });
        const outputs = step.forEach ? [{ id: 'out:*', label: text('wholeOutput'), type: `list(${activity?.output ? 'object' : 'dyn'})` }] : ports(shapeOf(activity?.output), 'out');
        if (!step.forEach) outputs.unshift({ id: 'out:*', label: text('wholeOutput'), type: activity?.output ? 'object' : 'dyn' });
        const badges = !!(step.when || step.undo || step.retry?.attempts), loop = step.forEach ? loopOf(step) : undefined;
        sizes.set(id, { w: WIDTH, h: 58 + (inputs.length + outputs.length) * 22 + (badges ? 26 : 0) + (loop ? 26 : 0) });
        const data: StepData = { kind: 'step', id, name, activity: step.activity, color: serviceColor(owner(step.activity)), inputs, outputs, when: step.when || undefined,
          undo: step.undo || undefined, retry: step.retry?.attempts ? `×${step.retry.attempts}` : undefined, problems: problemAt(stepPath), selected: selected === id, run: marks.get(id), loop };
        built.push({ id, node: { id, type: 'step', position: { x: 0, y: 0 }, data }, children: [] });
      }
      // Levels: a column each, stacked by name; the frame's origin offsets them.
      const columns = new Map<number, typeof built>();
      for (const entry of built) { const level = Math.max(0, levelOf(entry.id)); columns.set(level, [...columns.get(level) ?? [], entry]); }
      for (const [level, entries] of columns) {
        let y = origin.y;
        for (const entry of entries) {
          const at = saved[entry.id];
          entry.node.position = at ? { x: at.x, y: at.y } : { x: origin.x + level * COLUMN, y };
          y += (sizes.get(entry.id)?.h ?? 120) + 40;
        }
      }
      if (frame.loop) for (const entry of built) { entry.node.parentId = frame.loop.id; entry.node.extent = 'parent'; }
      return built.flatMap((entry) => [entry.node, ...entry.children]);
    };

    const stepNodes = build(definition.steps, top, '', { x: COLUMN, y: 0 });
    const lastColumn = Math.max(COLUMN, ...stepNodes.filter((node) => !node.parentId).map((node) => node.position.x + (sizes.get(node.id)?.w ?? WIDTH)));
    const position = (id: string, fallback: { x: number; y: number }) => { const at = saved[id]; return at ? { x: at.x, y: at.y } : fallback; };

    if (kind === 'binding') {
      const hook = (definition as api.BindingDefinition).hook, contract = index.hooks.get(hook)?.value;
      const triggerPorts = ports(shapeOf(contract?.input), 'out'); triggerPorts.unshift({ id: 'out:*', label: 'req', type: contract?.input ? 'object' : 'dyn' });
      nodes.push({ id: TRIGGER, type: 'end', position: position(TRIGGER, { x: 0, y: 0 }), deletable: false, data: { kind: 'trigger', title: hook, subtitle: text('hookInput'), color: serviceColor(owner(hook)), ports: triggerPorts, selected: selected === TRIGGER, problems: problemAt('/hook') } });
      const result = valueObject(apiJson((definition as api.BindingDefinition).result));
      const resultPorts = ports(shapeOf(contract?.output), 'in', result ?? {}, (field) => problemAt(`/result/${field}`) > 0);
      if ((definition as api.BindingDefinition).result && !result) resultPorts.unshift({ id: 'in:*', label: text('wholeResult'), type: 'object', expr: describe(apiJson((definition as api.BindingDefinition).result)) });
      else if (!contract?.output) resultPorts.unshift({ id: 'in:*', label: text('wholeResult'), type: 'dyn' });
      nodes.push(...stepNodes);
      nodes.push({ id: RESULT, type: 'end', position: position(RESULT, { x: lastColumn + COLUMN - WIDTH, y: 0 }), deletable: false, data: { kind: 'result', title: text('resultValue'), subtitle: text('hookOutput'), color: serviceColor(owner(hook)), ports: resultPorts, selected: selected === RESULT, problems: problemAt('/result') } });
    } else {
      const event = (definition as api.RuleDefinition).event, contract = index.events.get(event)?.value;
      const triggerPorts = ports(shapeOf(contract?.schema), 'out'); triggerPorts.unshift({ id: 'out:*', label: 'event', type: contract?.schema ? 'object' : 'dyn' }); triggerPorts.push({ id: 'out:meta', label: 'meta', type: 'object' });
      nodes.push({ id: TRIGGER, type: 'end', position: position(TRIGGER, { x: 0, y: 0 }), deletable: false, data: { kind: 'trigger', title: event, subtitle: text('eventInput'), color: serviceColor(owner(event)), ports: triggerPorts, selected: selected === TRIGGER, problems: problemAt('/event') + problemAt('/when'), when: (definition as api.RuleDefinition).when || undefined } });
      nodes.push(...stepNodes);
    }

    // Edges: every read of a variable by an expression, from the port it reads to the port of the value it sets.
    const byId = new Map(nodes.map((node) => [node.id, node]));
    const has = (id: string, handle: string, side: 'in' | 'out') => {
      const data = byId.get(id)?.data as StepData | EndData | LoopData | undefined;
      if (!data) return false;
      if (data.kind === 'loop') return side === 'out' ? handle === 'out:*' || handle === 'item' : handle === 'result';
      const list = data.kind === 'step' ? side === 'in' ? data.inputs : data.outputs : data.ports;
      return list.some((port) => port.id === handle);
    };
    const edges = new Map<string, Edge>();
    const colorOf = (id: string) => (byId.get(id)?.data as { color?: string } | undefined)?.color ?? 'var(--subtle)';
    for (const reference of analysis?.references ?? []) {
      const segments = reference.path.split('/').slice(1).map(unescape);
      let at = 0, id = '';
      while (segments[at] === 'steps' && segments[at + 1] !== undefined) {
        id = id ? `${id}/${segments[at + 1]}` : segments[at + 1]!;
        if (segments[at + 2] === 'steps' && frames.has(id)) { at += 2; continue; }
        break;
      }
      const rest = id ? segments.slice(at + 2) : segments;
      const parentOf = (node: string) => frames.get(node.includes('/') ? node.slice(0, node.lastIndexOf('/')) : '') ?? top;
      let target: string, targetHandle: string, control = false, frame: Frame;
      if (id && byId.has(id)) {
        target = id;
        frame = parentOf(id);
        if (rest[0] === 'input') targetHandle = rest[1] ? `in:${rest[1]}` : 'in:*';
        else if (rest[0] === 'when') { targetHandle = 'when'; control = true; }
        else if (rest[0] === 'forEach') { targetHandle = 'each'; control = true; }
        else if (rest[0] === 'result') { targetHandle = 'result'; frame = frames.get(id) ?? frame; }
        else continue;
        // A step's own item is read inside it: nothing to draw.
        const own = byId.get(id)?.data as StepData | undefined;
        if (rest[0] !== 'forEach' && own?.loop && reference.variable === own.loop.item) continue;
      } else if (!id && rest[0] === 'result' && kind === 'binding') { target = RESULT; targetHandle = rest[1] ? `in:${rest[1]}` : 'in:*'; frame = top; }
      else continue;
      let source: string, sourceHandle: string;
      const step = reference.variable === 'steps' ? reference.fields[0] : reference.variable;
      const found = step ? resolve(frame, step) : undefined;
      if (found?.handle) { source = found.id; sourceHandle = found.handle; }
      else if (found && reference.variable === 'steps') { source = found.id; sourceHandle = 'done'; control = true; }
      else if (found) { source = found.id; sourceHandle = reference.fields[0] ? `out:${reference.fields[0]}` : 'out:*'; }
      else if (reference.variable === 'req' || reference.variable === 'event') { source = TRIGGER; sourceHandle = reference.fields[0] ? `out:${reference.fields[0]}` : 'out:*'; }
      else if (reference.variable === 'meta') { source = TRIGGER; sourceHandle = 'out:meta'; }
      else continue;
      if (source === target) continue;
      if (sourceHandle !== 'done' && sourceHandle !== 'item' && !has(source, sourceHandle, 'out')) sourceHandle = 'out:*';
      if (targetHandle.startsWith('in:') && !has(target, targetHandle, 'in')) targetHandle = has(target, 'in:*', 'in') ? 'in:*' : 'head';
      const edgeId = `${source}:${sourceHandle}->${target}:${targetHandle}`;
      if (edges.has(edgeId)) continue;
      const color = colorOf(source), mark = marks.get(source);
      edges.set(edgeId, { id: edgeId, source, sourceHandle, target, targetHandle, type: 'smoothstep', animated: mark?.status === api.StepRunStatus.STARTED, zIndex: 1,
        style: { stroke: control ? 'var(--warning)' : color, strokeWidth: 1.5, strokeDasharray: control ? '4 4' : undefined, opacity: mark && mark.status === api.StepRunStatus.NOT_RUN ? 0.35 : 1 },
        markerEnd: { type: MarkerType.ArrowClosed, color: control ? 'var(--warning)' : color, width: 14, height: 14 },
        data: { reference: [reference.variable, ...reference.fields].join('.'), path: reference.path } });
    }
    const afterEdges = (steps: Record<string, api.Step>, prefix: string) => {
      for (const [name, step] of Object.entries(steps)) {
        for (const after of step.after) if (steps[after]) {
          const edgeId = `${prefix}${after}:done->${prefix}${name}:head`;
          edges.set(edgeId, { id: edgeId, source: prefix + after, sourceHandle: 'done', target: prefix + name, targetHandle: 'head', type: 'smoothstep', zIndex: 1,
            style: { stroke: 'var(--subtle)', strokeWidth: 1.25, strokeDasharray: '2 4' }, markerEnd: { type: MarkerType.ArrowClosed, color: 'var(--subtle)', width: 12, height: 12 }, data: { after: true } });
        }
        if (step.forEach && Object.keys(step.steps).length) afterEdges(step.steps, `${prefix}${name}/`);
      }
    };
    afterEdges(definition.steps, '');
    return { nodes, edges: [...edges.values()] };
  }, [kind, definition, analysis, index, problems, run, selected, text]);

  const [nodes, setNodes] = useState<GraphNode[]>(graph.nodes);
  useEffect(() => setNodes(graph.nodes), [graph.nodes]);
  const onNodesChange = useCallback((changes: NodeChange<GraphNode>[]) => setNodes((old) => applyNodeChanges(changes, old)), []);

  /** The variable a source port stands for at a target; undefined when the target's frame does not see it. */
  const variableOf = useCallback((source: string, handle: string, target: string) => {
    const targetParent = target.includes('/') ? target.slice(0, target.lastIndexOf('/')) : '';
    if (source === TRIGGER) return handle === 'out:meta' ? 'meta' : kind === 'binding' ? 'req' : 'event';
    if (handle === 'item') {
      if (!(targetParent === source || targetParent.startsWith(`${source}/`))) return undefined;
      return stepAt(definition, source).step?.as || 'item';
    }
    const sourceParent = source.includes('/') ? source.slice(0, source.lastIndexOf('/')) : '';
    // A step is visible in its own frame and every frame nested in it.
    if (sourceParent && !(targetParent === sourceParent || targetParent.startsWith(`${sourceParent}/`))) return undefined;
    return source.split('/').at(-1);
  }, [definition, kind]);

  const connect = useCallback((connection: Connection) => {
    if (!definition || !connection.sourceHandle || !connection.targetHandle || connection.source === connection.target) return;
    if (connection.targetHandle === 'head' && connection.sourceHandle === 'done' && connection.source !== TRIGGER) {
      const sourceParent = connection.source.split('/').slice(0, -1).join('/'), targetParent = connection.target.split('/').slice(0, -1).join('/');
      const { step } = stepAt(definition, connection.target), name = connection.source.split('/').at(-1)!;
      if (sourceParent !== targetParent || !step || step.after.includes(name)) return;
      apply([{ path: [...stepPathOf(connection.target), 'after'], value: [...step.after, name] }]); return;
    }
    const variable = variableOf(connection.source, connection.sourceHandle, connection.target);
    if (!variable) return;
    const field = connection.sourceHandle.startsWith('out:') && connection.sourceHandle !== 'out:*' && connection.sourceHandle !== 'out:meta' ? connection.sourceHandle.slice(4) : undefined;
    const expr = field ? `${variable}.${field}` : variable;
    if (connection.targetHandle === 'result') { apply([{ path: [...stepPathOf(connection.target), 'result'], value: expr }]); return; }
    const target = connection.targetHandle.startsWith('in:') ? connection.targetHandle.slice(3) : undefined;
    if (!target) return;
    const base = connection.target === RESULT ? ['result'] : [...stepPathOf(connection.target), 'input'];
    apply([{ path: target === '*' ? base : [...base, target], value: expr }]);
  }, [definition, apply, variableOf]);

  const onDrop = useCallback((event: DragEvent) => {
    const activity = event.dataTransfer.getData(activityDragType); if (!activity || !editable) return;
    event.preventDefault();
    const at = flow.screenToFlowPosition({ x: event.clientX, y: event.clientY });
    // Dropped inside a for-each body: the step joins it, positioned in it.
    const inside = flow.getNodes().filter((node) => node.type === 'loop').flatMap((node) => {
      const box = flow.getInternalNode(node.id);
      if (!box) return [];
      const origin = box.internals.positionAbsolute, width = box.measured.width ?? 0, height = box.measured.height ?? 0;
      return at.x >= origin.x && at.y >= origin.y && at.x <= origin.x + width && at.y <= origin.y + height ? [{ id: node.id, origin }] : [];
    }).sort((a, b) => b.id.length - a.id.length)[0];
    const origin = inside?.origin ?? { x: 0, y: 0 };
    const added = addStep(definition, activity, { x: at.x - origin.x - WIDTH / 2, y: at.y - origin.y - 20 }, inside?.id);
    apply(added.changes); onSelect(added.id);
  }, [definition, editable, flow, apply, onSelect]);

  if (!definition) return <EmptyState className="h-full" title={text('graphNeedsYaml')} description={text('graphNeedsYamlHelp')} />;
  return <div className="relative h-full" onDragOver={(event) => { if (event.dataTransfer.types.includes(activityDragType)) { event.preventDefault(); event.dataTransfer.dropEffect = 'copy'; } }} onDrop={onDrop}>
    <ReactFlow nodes={nodes} edges={graph.edges} nodeTypes={nodeTypes} onNodesChange={onNodesChange} fitView fitViewOptions={{ padding: 0.2 }} minZoom={0.2} maxZoom={1.6}
      nodesConnectable={editable} nodesDraggable elementsSelectable deleteKeyCode={editable ? ['Delete', 'Backspace'] : null} proOptions={{ hideAttribution: true }} colorMode={mode} className="backplane-map backplane-wiring"
      isValidConnection={(connection) => connection.targetHandle === 'head' || !!(connection.sourceHandle && variableOf(connection.source, connection.sourceHandle, connection.target))}
      onConnect={connect} onNodeClick={(_, node) => onSelect(node.id)} onPaneClick={() => onSelect(undefined)}
      onNodeDragStop={(_, node) => { if (editable) apply([{ path: ['editor', 'nodes', node.id], value: { x: Math.round(node.position.x), y: Math.round(node.position.y) } }]); }}
      onNodesDelete={(deleted) => {
        // A deleted loop takes its body with it: only the outermost deleted steps are removed.
        const ids = deleted.filter((node) => node.type !== 'end').map((node) => node.id);
        const changes = ids.filter((id) => !ids.some((other) => id.startsWith(`${other}/`))).flatMap((id) => removeStep(definition, id));
        if (changes.length) { apply(changes); onSelect(undefined); }
      }}
      onEdgesDelete={(deleted) => {
        const changes: Change[] = [];
        for (const edge of deleted) {
          const data = edge.data as { after?: boolean; reference?: string; path?: string } | undefined;
          if (data?.after) {
            const { step } = stepAt(definition, edge.target), name = edge.source.split('/').at(-1)!;
            if (step) changes.push({ path: [...stepPathOf(edge.target), 'after'], value: step.after.filter((entry) => entry !== name) });
            continue;
          }
          // Only a port that is exactly the reference unsets; an expression that reads more stays for the author to edit.
          if (data?.path && data.reference) { const path = data.path.split('/').slice(1).map(unescape); if (valueAt(kind, definition, path) === data.reference) changes.push({ path, value: undefined }); }
        }
        if (changes.length) apply(changes);
      }}>
      <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="var(--border)" />
      <Controls showInteractive={false} />
      {minimap && <MiniMap pannable zoomable className="backplane-minimap" nodeColor={(node) => (node.data as { color?: string }).color ?? 'var(--subtle)'} maskColor="color-mix(in oklab, var(--background) 70%, transparent)" style={{ width: 160, height: 100 }} />}
    </ReactFlow>
    {!editable && <div className="absolute top-2 left-1/2 -translate-x-1/2 rounded-md border border-destructive/40 bg-destructive/10 px-2 py-1 text-xs text-destructive">{text('graphReadOnly')}</div>}
  </div>;
}

const PortRow = ({ port, side }: { port: Port; side: 'in' | 'out' }) => <div className={`relative flex h-[22px] items-center gap-1.5 px-2.5 text-2xs ${side === 'out' ? 'justify-end' : ''}`}>
  {side === 'in' && <Handle id={port.id} type="target" position={Position.Left} className={`size-2! min-h-0! min-w-0! border! border-card! ${port.problem ? 'bg-destructive!' : port.expr ? 'bg-primary!' : 'bg-subtle!'}`} />}
  {side === 'in' ? <>
    <span className={`shrink-0 font-mono ${port.problem ? 'text-destructive' : 'text-foreground'}`}>{port.label}{port.required && <span className="text-destructive">*</span>}</span>
    {port.expr ? <span className="min-w-0 flex-1 truncate rounded-sm bg-raised px-1 font-mono text-link" title={port.expr}>{port.expr}</span> : <span className="min-w-0 flex-1 truncate text-muted-foreground">{port.type}</span>}
  </> : <>
    <span className="min-w-0 truncate text-muted-foreground">{port.type}</span><span className="shrink-0 font-mono text-foreground">{port.label}</span>
  </>}
  {side === 'out' && <Handle id={port.id} type="source" position={Position.Right} className="size-2! min-h-0! min-w-0! border! border-card! bg-subtle!" />}
</div>;

/** A run's mark on a node: a call's status and time, or its items' count. */
function RunBadge({ run }: { run: RunMark }) {
  if (run.items) {
    const tone: StatusTone = run.items.failed ? 'danger' : 'success';
    return <StatusBadge tone={tone} className="h-5 px-1.5 text-2xs">{run.items.ok}/{run.items.total}{run.items.failed ? ` · ${run.items.failed} ✕` : ''}</StatusBadge>;
  }
  return <StatusBadge tone={stepTone[run.status]} className="h-5 px-1.5 text-2xs">{run.undone ? 'undone' : enumLabel(api.StepRunStatus, run.status)}{run.ms !== undefined ? ` · ${Math.round(run.ms)}ms` : ''}</StatusBadge>;
}

/** "each <item> in <list>" with the options of a for-each step. */
function LoopBand({ loop }: { loop: Loop }) {
  return <div className="flex min-w-0 items-center gap-1.5 border-b border-border bg-info/5 px-2.5 py-1 text-2xs">
    <Repeat className="size-3 shrink-0 text-info" />
    <span className="min-w-0 truncate font-mono" title={`${loop.item} in ${loop.expr}`}><span className="text-muted-foreground">each </span>{loop.item}<span className="text-muted-foreground"> in </span><span className="text-link">{loop.expr}</span></span>
    <span className="ml-auto flex shrink-0 gap-1">{loop.badges.map((badge) => <Badge key={badge} variant="outline" className="h-4 px-1 text-2xs">{badge}</Badge>)}</span>
  </div>;
}

const StepNode = memo(function StepNode({ data }: NodeProps<Node<StepData, 'step'>>) {
  const border = data.selected ? 'border-primary ring-1 ring-primary/40' : data.problems ? 'border-destructive/60' : 'border-border hover:border-border-strong';
  return <div className={`rounded-lg border bg-card text-card-foreground shadow-sm ${border}`} style={{ width: WIDTH }}>
    <Handle id="head" type="target" position={Position.Top} className="size-2! min-h-0! min-w-0! border-0! bg-subtle!" />
    {data.loop && <Handle id="each" type="target" position={Position.Left} style={{ top: 60 }} className="size-1.5! min-h-0! min-w-0! border-0! bg-info!" />}
    <div className="flex items-center gap-2 rounded-t-lg border-b border-border px-2.5 py-1.5" style={{ boxShadow: `inset 3px 0 0 ${data.color}` }}>
      <span className="min-w-0 flex-1"><span className="block truncate font-mono text-xs font-medium">{data.name}</span>
        <span className="block truncate font-mono text-2xs text-muted-foreground">{data.activity || '—'}</span></span>
      {data.problems > 0 && <span className="inline-flex items-center gap-0.5 text-2xs text-destructive"><CircleAlert className="size-3" />{data.problems}</span>}
      {data.run && <RunBadge run={data.run} />}
    </div>
    {data.loop && <LoopBand loop={data.loop} />}
    {(data.when || data.undo || data.retry) && <div className="flex flex-wrap items-center gap-1 border-b border-border px-2.5 py-1">
      {data.when && <Badge variant="outline" className="max-w-full gap-1 truncate font-mono text-2xs text-warning" title={data.when}><Filter className="size-3" />{data.when}</Badge>}
      {data.undo && <Badge variant="outline" className="gap-1 font-mono text-2xs" title={data.undo}><Undo2 className="size-3" />{data.undo}</Badge>}
      {data.retry && <Badge variant="outline" className="gap-1 text-2xs"><RotateCcw className="size-3" />{data.retry}</Badge>}
    </div>}
    <Handle id="when" type="target" position={Position.Left} style={{ top: 18 }} className="size-1.5! min-h-0! min-w-0! border-0! bg-warning!" />
    <div className="py-1">{data.inputs.map((port) => <PortRow key={port.id} port={port} side="in" />)}</div>
    <div className="border-t border-border py-1">{data.outputs.map((port) => <PortRow key={port.id} port={port} side="out" />)}</div>
    <Handle id="done" type="source" position={Position.Bottom} className="size-2! min-h-0! min-w-0! border-0! bg-subtle!" />
  </div>;
});

/** A for-each step whose body is steps: a frame holding them, the item as a port inside. */
const LoopNode = memo(function LoopNode({ data }: NodeProps<Node<LoopData, 'loop'>>) {
  const text = usePlatformText();
  const border = data.selected ? 'border-primary ring-1 ring-primary/40' : data.problems ? 'border-destructive/60' : 'border-info/40';
  return <div className={`rounded-xl border-2 border-dashed bg-info/[0.03] ${border}`} style={{ width: data.width, height: data.height }}>
    <Handle id="head" type="target" position={Position.Top} className="size-2! min-h-0! min-w-0! border-0! bg-subtle!" />
    <Handle id="each" type="target" position={Position.Left} style={{ top: 16 }} className="size-1.5! min-h-0! min-w-0! border-0! bg-info!" />
    <div className="flex items-center gap-2 rounded-t-xl border-b border-info/20 bg-card/80 px-3 py-1.5">
      <Repeat className="size-3.5 shrink-0 text-info" />
      <span className="font-mono text-xs font-medium">{data.name}</span>
      <span className="min-w-0 flex-1 truncate font-mono text-2xs" title={`${data.loop.item} in ${data.loop.expr}`}><span className="text-muted-foreground">each </span>{data.loop.item}<span className="text-muted-foreground"> in </span><span className="text-link">{data.loop.expr}</span></span>
      {data.loop.badges.map((badge) => <Badge key={badge} variant="outline" className="h-4 px-1 text-2xs">{badge}</Badge>)}
      {data.problems > 0 && <span className="inline-flex items-center gap-0.5 text-2xs text-destructive"><CircleAlert className="size-3" />{data.problems}</span>}
      {data.run && <RunBadge run={data.run} />}
    </div>
    <div className="relative px-3 pt-1 text-2xs text-muted-foreground">
      <span className="font-mono text-foreground">{data.loop.item}</span> · {text('itemPort')}
      <Handle id="item" type="source" position={Position.Left} style={{ top: 10, left: 2 }} className="size-2! min-h-0! min-w-0! border! border-card! bg-info!" />
    </div>
    <Handle id="result" type="target" position={Position.Right} style={{ top: 30 }} className="size-2! min-h-0! min-w-0! border! border-card! bg-primary!" />
    <Handle id="out:*" type="source" position={Position.Right} style={{ top: 16 }} className="size-2! min-h-0! min-w-0! border! border-card! bg-subtle!" />
    <Handle id="done" type="source" position={Position.Bottom} className="size-2! min-h-0! min-w-0! border-0! bg-subtle!" />
  </div>;
});

const EndNode = memo(function EndNode({ data }: NodeProps<Node<EndData, 'end'>>) {
  const border = data.selected ? 'border-primary ring-1 ring-primary/40' : data.problems ? 'border-destructive/60' : 'border-border';
  return <div className={`rounded-lg border border-dashed bg-card text-card-foreground shadow-sm ${border}`} style={{ width: WIDTH - 32 }}>
    <div className="flex items-center gap-2 rounded-t-lg border-b border-border px-2.5 py-1.5" style={{ boxShadow: `inset 3px 0 0 ${data.color}` }}>
      <span className="grid size-5 place-items-center rounded-sm bg-raised text-muted-foreground">{data.kind === 'trigger' ? <Play className="size-3" /> : <Flag className="size-3" />}</span>
      <span className="min-w-0 flex-1"><span className="block truncate font-mono text-xs font-medium">{data.title}</span><span className="block truncate text-2xs text-muted-foreground">{data.subtitle}</span></span>
      {data.problems > 0 && <span className="inline-flex items-center gap-0.5 text-2xs text-destructive"><CircleAlert className="size-3" />{data.problems}</span>}
    </div>
    {data.when && <div className="border-b border-border px-2.5 py-1"><Badge variant="outline" className="max-w-full gap-1 truncate font-mono text-2xs text-warning" title={data.when}><Clock className="size-3" />when {data.when}</Badge></div>}
    <div className="py-1">{data.ports.map((port) => <PortRow key={port.id} port={port} side={data.kind === 'trigger' ? 'out' : 'in'} />)}</div>
  </div>;
});

const nodeTypes = { step: StepNode, loop: LoopNode, end: EndNode };
