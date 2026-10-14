import { memo, useMemo, useState } from 'react';
import { create } from '@bufbuild/protobuf';
import { Background, BackgroundVariant, Controls, Handle, MarkerType, Position, ReactFlow, type Edge, type Node, type NodeProps } from '@xyflow/react';
import * as api from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { Badge, EmptyState, Panel, Skeleton, StatusDot, type StatusTone } from '@backplane-labs/backplane-ui';
import { Box, Cable, Radio, Zap } from 'lucide-react';
import { usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import type { WiringTarget } from './wiring/editor.js';

export type WireKind = 'binding' | 'rule' | 'subscription';
/** One connection between two services, as declared by bindings, rules and manifests. */
export interface Wire { kind: WireKind; from: string; to: string; label: string; detail: string; target?: WiringTarget }

const healthTone: Record<api.ServiceHealth, StatusTone> = {
  [api.ServiceHealth.UNSPECIFIED]: 'neutral', [api.ServiceHealth.HEALTHY]: 'success', [api.ServiceHealth.DEGRADED]: 'warning', [api.ServiceHealth.DOWN]: 'danger',
};
const owner = (qualified: string) => qualified.slice(0, Math.max(0, qualified.indexOf('.'))) || qualified;
const wireColor: Record<WireKind, string> = { binding: 'var(--chart-1)', rule: 'var(--chart-5)', subscription: 'var(--chart-2)' };

/** Every service-to-service connection of the installation. */
export function collectWires(manifests: api.GetServiceResponse[], bindings: api.HookBinding[], rules: api.Rule[]): Wire[] {
  const wires: Wire[] = [];
  for (const binding of bindings) if (!binding.current?.deleted) for (const [name, step] of Object.entries(binding.current?.definition?.steps ?? {}).sort(([a], [b]) => a.localeCompare(b))) {
    wires.push({ kind: 'binding', from: binding.service || owner(binding.hook), to: owner(step.activity), label: binding.hook, detail: `${name} → ${step.activity}`, target: { kind: 'binding', hook: binding.hook } });
  }
  for (const rule of rules) {
    const definition = rule.current?.definition; if (!definition || rule.current?.deleted) continue;
    for (const step of Object.values(definition.steps)) wires.push({ kind: 'rule', from: owner(definition.event), to: owner(step.activity), label: rule.current?.name || rule.id, detail: `${definition.event} → ${step.activity}${rule.paused ? ' (paused)' : ''}`, target: { kind: 'rule', id: rule.id } });
  }
  for (const service of manifests) for (const subscription of service.latest?.subscriptions ?? []) {
    wires.push({ kind: 'subscription', from: owner(subscription.event), to: service.latest!.service, label: subscription.event, detail: `${subscription.event} → ${subscription.consumer}` });
  }
  return wires;
}

/** Layered left-to-right layout: a service sits one column right of its furthest upstream. */
function layout(names: string[], wires: Wire[]) {
  const rank = new Map(names.map((name) => [name, 0]));
  for (let pass = 0; pass < names.length; pass++) {
    let changed = false;
    for (const wire of wires) {
      if (wire.from === wire.to || !rank.has(wire.from) || !rank.has(wire.to)) continue;
      const next = rank.get(wire.from)! + 1;
      if (next > rank.get(wire.to)! && next < names.length) { rank.set(wire.to, next); changed = true; }
    }
    if (!changed) break;
  }
  const columns = new Map<number, string[]>();
  for (const name of names) columns.set(rank.get(name)!, [...columns.get(rank.get(name)!) ?? [], name]);
  const positions = new Map<string, { x: number; y: number }>();
  for (const [column, members] of columns) members.sort().forEach((name, index) => positions.set(name, { x: column * 340, y: index * 170 - (members.length - 1) * 85 }));
  return positions;
}

const kinds: readonly WireKind[] = ['binding', 'rule', 'subscription'];
type ServiceNodeData = { name: string; tone: StatusTone; instances: string; counts: Record<WireKind, number>; selected: boolean };
const ServiceNode = memo(function ServiceNode({ data }: NodeProps<Node<ServiceNodeData>>) {
  return <div className={`w-56 rounded-lg border bg-card px-3 py-2.5 text-card-foreground shadow-sm transition-colors ${data.selected ? 'border-primary' : 'border-border hover:border-border-strong'}`}>
    {kinds.map((kind, index) => <Handle key={`in-${kind}`} id={`in-${kind}`} type="target" position={Position.Left} style={{ top: `${30 + index * 20}%` }} className="size-1.5! min-h-0! min-w-0! border-0!" />)}
    <div className="flex items-center gap-2"><span className="grid size-7 place-items-center rounded-md border border-border bg-raised text-link"><Box className="size-3.5" /></span>
      <span className="min-w-0 flex-1 truncate text-sm font-medium">{data.name}</span><StatusDot tone={data.tone} /></div>
    <div className="mt-2 flex items-center gap-2 text-2xs text-muted-foreground"><span className="font-mono">{data.instances}</span>
      <span className="ml-auto inline-flex items-center gap-1"><Cable className="size-3" />{data.counts.binding}</span>
      <span className="inline-flex items-center gap-1"><Zap className="size-3" />{data.counts.rule}</span>
      <span className="inline-flex items-center gap-1"><Radio className="size-3" />{data.counts.subscription}</span></div>
    {kinds.map((kind, index) => <Handle key={`out-${kind}`} id={`out-${kind}`} type="source" position={Position.Right} style={{ top: `${30 + index * 20}%`, background: wireColor[kind] }} className="size-1.5! min-h-0! min-w-0! border-0!" />)}
  </div>;
});
const nodeTypes = { service: ServiceNode };

/** Services and how they are wired: bindings, rules and event subscriptions. */
export function SystemMap({ services, mode, onOpenService, onOpenWiring, height }: { services: api.ServiceSummary[]; mode: 'dark' | 'light'; onOpenService?: (service: string) => void; onOpenWiring?: (target: WiringTarget) => void; height?: number }) {
  const catalog = useClient(api.CatalogServiceClient), bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText();
  const names = services.map((service) => service.name).join(',');
  const state = usePlatformQuery(`map:${names}`, async (signal) => {
    const [manifests, bound, ruled] = await Promise.all([
      Promise.allSettled(services.map((service) => catalog.getService(create(api.GetServiceRequestSchema, { name: service.name }), { signal }))),
      bindings.listBindings(create(api.ListBindingsRequestSchema), { signal }), rules.listRules(create(api.ListRulesRequestSchema), { signal }),
    ]);
    return collectWires(manifests.flatMap((result) => result.status === 'fulfilled' ? [result.value] : []), bound.bindings, ruled.rules);
  });
  const [hidden, setHidden] = useState<Set<WireKind>>(new Set()), [selected, setSelected] = useState<string>();
  const wires = (state.value ?? []).filter((wire) => !hidden.has(wire.kind));
  const graph = useMemo(() => {
    const known = services.map((service) => service.name);
    const extra = [...new Set(wires.flatMap((wire) => [wire.from, wire.to]))].filter((name) => !known.includes(name));
    const all = [...known, ...extra], positions = layout(all, wires);
    const count = (name: string, kind: WireKind) => wires.filter((wire) => wire.kind === kind && (wire.from === name || wire.to === name)).length;
    // Self-connections (a service consuming its own event) stay in the list; an edge to itself adds no information.
    const nodes: Node<ServiceNodeData>[] = all.map((name) => { const summary = services.find((service) => service.name === name); return {
      id: name, type: 'service', position: positions.get(name)!, data: {
        name, tone: summary ? healthTone[summary.health] : 'neutral', instances: summary ? `${summary.healthy}/${summary.instances}` : '—',
        counts: { binding: count(name, 'binding'), rule: count(name, 'rule'), subscription: count(name, 'subscription') }, selected: name === selected,
      } }; });
    const grouped = new Map<string, Wire[]>();
    for (const wire of wires) if (wire.from !== wire.to) grouped.set(`${wire.kind}:${wire.from}:${wire.to}`, [...grouped.get(`${wire.kind}:${wire.from}:${wire.to}`) ?? [], wire]);
    const edges: Edge[] = [...grouped.entries()].map(([id, group]) => { const first = group[0]!, active = !selected || first.from === selected || first.to === selected; return {
      id, source: first.from, target: first.to, sourceHandle: `out-${first.kind}`, targetHandle: `in-${first.kind}`, label: group.length > 1 ? `${group.length}` : first.label, animated: first.kind === 'subscription',
      style: { stroke: wireColor[first.kind], strokeWidth: 1.5, strokeDasharray: first.kind === 'rule' ? '5 4' : undefined, opacity: active ? 1 : 0.15 },
      markerEnd: { type: MarkerType.ArrowClosed, color: wireColor[first.kind], width: 16, height: 16 },
      labelStyle: { fill: 'var(--muted-foreground)', fontSize: 10, fontFamily: 'var(--font-mono)' }, labelBgStyle: { fill: 'var(--card)' }, labelBgPadding: [4, 2] as [number, number],
    }; });
    return { nodes, edges };
  }, [services, wires, selected]);
  const focus = selected ? wires.filter((wire) => wire.from === selected || wire.to === selected) : wires;
  if (!state.value && state.loading) return <Skeleton style={{ height: height ?? 480 }} />;
  return <div className="grid h-full min-h-0 gap-3 xl:grid-rows-[minmax(0,1fr)] xl:grid-cols-[minmax(0,1fr)_320px]">
    <div className="relative min-h-[420px] overflow-hidden rounded-lg border border-border bg-card" style={height ? { height } : undefined}>
      {!services.length ? <EmptyState className="h-full" title={text('noServicesMap')} /> :
        <ReactFlow nodes={graph.nodes} edges={graph.edges} nodeTypes={nodeTypes} fitView fitViewOptions={{ padding: 0.25 }} minZoom={0.3} maxZoom={1.5}
          nodesConnectable={false} proOptions={{ hideAttribution: true }} colorMode={mode} className="backplane-map"
          onNodeClick={(_, node) => setSelected(node.id === selected ? undefined : node.id)} onNodeDoubleClick={(_, node) => onOpenService?.(node.id)} onPaneClick={() => setSelected(undefined)}>
          <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="var(--border)" />
          <Controls showInteractive={false} />
        </ReactFlow>}
      <div className="absolute top-2 left-2 flex gap-1 rounded-md border border-border bg-chrome/90 p-1" role="group" aria-label={text('wireKinds')}>
        {(['binding', 'rule', 'subscription'] as const).map((kind) => <button key={kind} type="button" aria-pressed={!hidden.has(kind)} onClick={() => setHidden((old) => { const next = new Set(old); if (next.has(kind)) next.delete(kind); else next.add(kind); return next; })}
          className="inline-flex h-6 items-center gap-1.5 rounded-sm px-2 text-xs text-muted-foreground aria-pressed:bg-raised aria-pressed:text-foreground">
          <span className="h-0.5 w-3 rounded-full" style={{ background: wireColor[kind] }} />{text(`wire_${kind}`)}</button>)}
      </div>
    </div>
    <Panel fill title={selected ? <span className="font-mono">{selected}</span> : text('connections')} count={focus.length} flush
      description={selected ? text('connectionsOf') : text('connectionsHelp')}
      footer={selected && onOpenService ? <button type="button" className="text-link hover:underline" onClick={() => onOpenService(selected)}>{text('openService')}</button> : undefined}>
      <div>
        {!focus.length && <EmptyState className="py-8" title={text('noConnections')} />}
        {focus.map((wire, index) => { const open = wire.target && onOpenWiring ? () => onOpenWiring(wire.target!) : undefined; return <button key={index} type="button" disabled={!open} onClick={open} title={open ? text('openInWiring') : undefined}
          className="grid w-full gap-0.5 border-b border-border px-3 py-2 text-left last:border-b-0 enabled:hover:bg-raised">
          <div className="flex items-center gap-2 text-xs"><span className="h-0.5 w-3 shrink-0 rounded-full" style={{ background: wireColor[wire.kind] }} />
            <span className="font-mono">{wire.from}</span><span className="text-muted-foreground">→</span><span className="font-mono">{wire.to}</span>
            <Badge variant="outline" className="ml-auto">{text(`wire_${wire.kind}`)}</Badge></div>
          <div className="truncate pl-5 font-mono text-2xs text-muted-foreground" title={wire.detail}>{wire.label} · {wire.detail}</div>
        </button>; })}
      </div>
    </Panel>
  </div>;
}
