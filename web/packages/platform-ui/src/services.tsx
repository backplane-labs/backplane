import { useCallback, useState, type ReactNode } from 'react';
import { create } from '@bufbuild/protobuf';
import { BindingServiceClient, BindingState, CatalogServiceClient, GetServiceRequestSchema, ListBindingsRequestSchema, WatchCatalogRequestSchema, type Instance, type ServiceSummary } from '@backplane-labs/backplane-api';
import { ActivityKind, ApiKind, type Manifest } from '@backplane-labs/backplane-api/backplanepb/v1/manifest_pb';
import { ConfigSource, InstancePhase, NodeKind, type InstanceState, type NodeStatus } from '@backplane-labs/backplane-api/backplanepb/v1/instance_pb';
import { useClient, useSnapshotWatch } from '@backplane-labs/backplane-react';
import { Badge, DataTable, DetailDrawer, Duration, EmptyState, KeyValueList, Panel, StatusBadge, StatusDot, Timestamp, type StatusTone } from '@backplane-labs/backplane-ui';
import { JSONViewer } from '@backplane-labs/backplane-editors';
import { Box, Boxes, Cable, CalendarClock, ChevronRight, Component, Link2, Radio, Route, Workflow, Zap } from 'lucide-react';
import { ConnectionNotice, QueryState, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { date, durationText, enumLabel, jsonText } from './format.js';

export function ServiceCatalog({ onSelect }: { onSelect: (service: ServiceSummary) => void }) {
  const client = useClient(CatalogServiceClient), text = usePlatformText();
  const state = useSnapshotWatch(useCallback((signal: AbortSignal) => client.watchCatalog(create(WatchCatalogRequestSchema), { signal }), [client]));
  return <section><ConnectionNotice />{state.status !== 'ready' && <p role="status">{text(state.status)}</p>}
    <DataTable label={text('service')} data={state.value?.services ?? []} getRowId={(service) => service.name} onActivate={onSelect} columns={[
      { id: 'name', label: text('service'), value: (service) => service.name }, { id: 'version', label: text('version'), value: (service) => service.latestVersion },
      { id: 'instances', label: text('instances'), value: (service) => service.instances }, { id: 'healthy', label: text('healthy'), value: (service) => service.healthy },
    ]} />
  </section>;
}

const phaseTone: Record<InstancePhase, StatusTone> = {
  [InstancePhase.UNSPECIFIED]: 'neutral', [InstancePhase.STARTING]: 'info', [InstancePhase.SERVING]: 'success', [InstancePhase.STOPPING]: 'warning',
};
const bindingTone: Record<BindingState, StatusTone> = {
  [BindingState.UNSPECIFIED]: 'neutral', [BindingState.BOUND]: 'success', [BindingState.UNBOUND]: 'neutral', [BindingState.REQUIRED_UNBOUND]: 'danger', [BindingState.BROKEN]: 'danger',
};

/** One dense row inside a flush panel. */
function Row({ children, onClick }: { children: ReactNode; onClick?: () => void }) {
  const className = 'flex w-full min-w-0 items-center gap-2.5 border-b border-border px-3 py-2 text-left text-sm last:border-b-0';
  return onClick ? <button type="button" className={`${className} cursor-pointer hover:bg-raised focus-visible:bg-raised focus-visible:outline-none`} onClick={onClick}>{children}</button> : <div className={className}>{children}</div>;
}
function Muted({ children, mono }: { children: ReactNode; mono?: boolean }) {
  return <span className={`min-w-0 truncate text-xs text-muted-foreground ${mono ? 'font-mono' : ''}`}>{children}</span>;
}
function Empty({ label }: { label: string }) { return <EmptyState className="py-6" title={label} />; }

export function ServiceInspector({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const client = useClient(CatalogServiceClient), bindings = useClient(BindingServiceClient), text = usePlatformText();
  const state = usePlatformQuery(`service:${service}`, (signal) => client.getService(create(GetServiceRequestSchema, { name: service }), { signal }));
  const bound = usePlatformQuery(`bindings:${service}`, (signal) => bindings.listBindings(create(ListBindingsRequestSchema, { service }), { signal }));
  const [selected, setSelected] = useState<string>();
  const manifest = state.value?.latest, instances = state.value?.instances ?? [];
  const instance = instances.find((entry) => entry.id === selected);
  // The tree depends on the instance's configuration: the latest version's first instance shows it.
  const tree = (instances.find((entry) => entry.state?.version === manifest?.version) ?? instances.find((entry) => entry.state))?.state?.nodes ?? [];
  return <QueryState state={state}>{state.value && <div className="grid h-full min-h-0 gap-4 xl:grid-rows-[minmax(0,1fr)] xl:grid-cols-[minmax(0,1fr)_320px]">
    <div className="flex min-h-0 min-w-0 flex-col gap-4 overflow-auto">
      <Panel title={text('instances')} count={instances.length} flush maxBodyHeight={320}>
        {instances.length ? <table aria-label={text('instances')} className="w-full text-sm">
          <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-xs text-muted-foreground">
            {[text('instance'), text('phase'), text('version'), text('address'), text('uptime'), text('config'), text('readiness')].map((label) => <th key={label} className="h-8 px-3 font-medium whitespace-nowrap">{label}</th>)}
            <th className="w-8"><span className="sr-only">{text('detail')}</span></th>
          </tr></thead>
          <tbody>{instances.map((entry) => <InstanceRow key={entry.id} instance={entry} onOpen={() => setSelected(entry.id)} />)}</tbody>
        </table> : <Empty label={text('noInstances')} />}
      </Panel>
      {manifest && <Contract manifest={manifest} hooks={bound.value?.bindings} />}
    </div>
    <aside className="flex min-h-0 min-w-0 flex-col gap-4 overflow-auto">
      <Panel title={text('about')}>
        <KeyValueList items={[
          { label: text('service'), value: service },
          { label: text('version'), value: state.value.summary?.latestVersion || '—', mono: true },
          { label: text('sdk'), value: manifest?.sdkVersion || '—', mono: true },
          { label: text('knownVersions'), value: <span className="flex flex-wrap gap-1">{(state.value.summary?.versions ?? []).map((version) => <Badge key={version} variant="outline" className="font-mono">{version}</Badge>)}</span> },
          { label: text('internalApi'), value: state.value.summary?.internalApi ? text('yes') : text('no') },
          { label: text('uiModule'), value: manifest?.ui ? <span className="font-mono text-xs">{manifest.ui.hash.slice(0, 12)} · sdk {manifest.ui.sdkMajor}</span> : text('no') },
          { label: text('configKeys'), value: `${manifest?.config?.keys.length ?? 0} · ${text('liveCount', { count: manifest?.config?.live.length ?? 0 })}` },
        ]} />
      </Panel>
      <Panel title={text('components')} count={tree.length} flush>
        {tree.length ? <ComponentTree nodes={tree} /> : <Empty label={text('noComponents')} />}
      </Panel>
    </aside>
    <DetailDrawer open={!!instance} onOpenChange={(open) => { if (!open) setSelected(undefined); }} size="lg" title={instance?.id} description={text('instanceDetail')}>
      {instance && <InstanceDetail instance={instance} mode={mode} />}
    </DetailDrawer>
  </div>}</QueryState>;
}

function InstanceRow({ instance, onOpen }: { instance: Instance; onOpen: () => void }) {
  const text = usePlatformText(), state = instance.state;
  const deps = dependencies(state), ready = deps.filter((node) => node.ready).length, total = deps.length;
  return <tr tabIndex={0} className="cursor-pointer border-b border-border last:border-b-0 hover:bg-raised focus-visible:bg-raised focus-visible:outline-none" onClick={onOpen} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); onOpen(); } }}>
    <td className="h-10 px-3"><span className="inline-flex items-center gap-2 font-mono text-xs"><StatusDot tone={instance.healthy ? 'success' : 'danger'} />{instance.id}</span></td>
    <td className="px-3">{state ? <StatusBadge tone={phaseTone[state.phase]}>{enumLabel(InstancePhase, state.phase)}</StatusBadge> : <Muted>{text('catalogOnly')}</Muted>}</td>
    <td className="px-3 font-mono text-xs text-muted-foreground">{state?.version || '—'}</td>
    <td className="px-3 font-mono text-xs text-muted-foreground">{instance.address ? `${instance.address}${instance.port ? `:${instance.port}` : ''}` : '—'}</td>
    <td className="px-3 text-xs text-muted-foreground">{state?.startedAt ? <Duration since={date(state.startedAt)} /> : '—'}</td>
    <td className="px-3 text-xs">{!state ? '—' : state.configRejectedRevision > 0n
      ? <span className="text-warning" title={state.configError}>{text('revRejected', { revision: state.configRejectedRevision.toString() })}</span>
      : <span className="text-muted-foreground">{text('revApplied', { revision: state.configRevision.toString() })}</span>}</td>
    <td className="px-3 text-xs"><span className={ready < total ? 'text-warning' : 'text-muted-foreground'}>{total ? `${ready}/${total}` : '—'}</span></td>
    <td className="px-2"><button type="button" className="grid size-7 place-items-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground" aria-label={text('openInstance', { id: instance.id })} onClick={(event) => { event.stopPropagation(); onOpen(); }}><ChevronRight className="size-4" /></button></td>
  </tr>;
}

function InstanceDetail({ instance, mode }: { instance: Instance; mode: 'dark' | 'light' }) {
  const text = usePlatformText(), state = instance.state;
  if (!state) return <Empty label={text('catalogOnly')} />;
  const sources = Object.entries(state.sources).sort(([a], [b]) => a.localeCompare(b)), deps = dependencies(state);
  const ports = [...(state.publicPort ? [`:${state.publicPort}`] : []), ...Object.entries(state.listeners).sort(([a], [b]) => a.localeCompare(b)).map(([name, port]) => `${name} :${port}`)];
  return <div className="grid gap-4">
    <KeyValueList items={[
      { label: text('phase'), value: <StatusBadge tone={phaseTone[state.phase]}>{enumLabel(InstancePhase, state.phase)}</StatusBadge> },
      { label: text('health'), value: <StatusBadge tone={instance.healthy ? 'success' : 'danger'}>{text(instance.healthy ? 'passing' : 'failing')}</StatusBadge> },
      { label: text('version'), value: state.version, mono: true }, { label: text('commit'), value: state.commit || '—', mono: true },
      { label: text('sdk'), value: state.sdkVersion || '—', mono: true },
      { label: text('address'), value: `${state.address}${state.platformPort ? ` · platform :${state.platformPort}` : ''}`, mono: true },
      { label: text('ports'), value: ports.length ? ports.join(' · ') : '—', mono: true },
      { label: text('started'), value: <><Timestamp value={date(state.startedAt)} absolute /> · <Duration since={date(state.startedAt)} /></> },
      { label: text('config'), value: state.configRejectedRevision > 0n
        ? <span className="text-warning">{text('revRejected', { revision: state.configRejectedRevision.toString() })}: {state.configError}</span>
        : text('revApplied', { revision: state.configRevision.toString() }) },
      { label: text('tags'), value: instance.tags.length ? <span className="flex flex-wrap gap-1">{instance.tags.map((tag) => <Badge key={tag} variant="outline">{tag}</Badge>)}</span> : '—' },
    ]} />
    <Panel title={text('transports')} flush>
      {state.transports.map((transport) => <Row key={transport.name}><StatusDot tone={transport.connected ? 'success' : 'danger'} /><span className="font-medium">{transport.name}</span>{transport.error && <Muted>{transport.error}</Muted>}</Row>)}
    </Panel>
    <Panel title={text('readiness')} count={deps.length} flush>
      {deps.length ? deps.map((node) => <Row key={node.path}><StatusDot tone={node.ready ? 'success' : 'danger'} /><span className="font-mono text-xs">{node.path}</span>{node.error && <Muted>{node.error}</Muted>}</Row>) : <Empty label={text('noComponents')} />}
    </Panel>
    <Panel title={text('configSources')} count={sources.length} flush>
      {sources.map(([path, source]) => <Row key={path}><span className="min-w-0 flex-1 truncate font-mono text-xs">{path}</span><Badge variant={source === ConfigSource.KV ? 'default' : 'outline'}>{enumLabel(ConfigSource, source)}</Badge></Row>)}
    </Panel>
    <JSONViewer label={text('effective')} value={jsonText(state.config)} mode={mode} />
  </div>;
}

function Contract({ manifest, hooks }: { manifest: Manifest; hooks?: { hook: string; state: BindingState }[] }) {
  const text = usePlatformText();
  const state = new Map(hooks?.map((hook) => [hook.hook, hook.state]));
  return <div className="grid gap-4 lg:grid-cols-2">
    <Panel title={<><Route className="size-4 text-muted-foreground" />{text('apis')}</>} count={manifest.apis.length} flush maxBodyHeight={260}>
      {manifest.apis.length ? manifest.apis.map((route, index) => <Row key={index}>
        <Badge variant="outline" className="w-20 justify-center font-mono uppercase">{enumLabel(ApiKind, route.kind)}</Badge>
        <span className="min-w-0 truncate font-mono text-xs">{route.host && <span className="text-muted-foreground">{route.host}</span>}{route.prefix}</span>
        <span className="ml-auto shrink-0"><Muted mono>{route.listener}{route.services.length ? ` ${route.services.length} svc` : ''}</Muted></span>
      </Row>) : <Empty label={text('noAPIs')} />}
    </Panel>
    <Panel title={<><Link2 className="size-4 text-muted-foreground" />{text('hooks')}</>} count={manifest.hooks.length} flush maxBodyHeight={260}>
      {manifest.hooks.length ? manifest.hooks.map((hook) => {
        const binding = state.get(`${manifest.service}.${hook.name}`) ?? state.get(hook.name);
        return <Row key={hook.name}><span className="font-mono text-xs">{hook.name}</span>{hook.required && <Badge variant="outline">{text('required')}</Badge>}
          <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{hook.description}</span>
          {binding !== undefined && <StatusBadge tone={bindingTone[binding]}>{enumLabel(BindingState, binding)}</StatusBadge>}</Row>;
      }) : <Empty label={text('noHooks')} />}
    </Panel>
    <Panel title={<><Zap className="size-4 text-muted-foreground" />{text('activities')}</>} count={manifest.activities.length} flush maxBodyHeight={260}>
      {manifest.activities.length ? manifest.activities.map((activity) => <Row key={activity.name}>
        <span className="font-mono text-xs">{activity.name}</span><Badge variant="outline">{enumLabel(ActivityKind, activity.kind)}</Badge>
        <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{activity.description}</span>
        {activity.startToClose && <Muted mono>{durationText(activity.startToClose)}</Muted>}
      </Row>) : <Empty label={text('noActivities')} />}
    </Panel>
    <Panel title={<><Radio className="size-4 text-muted-foreground" />{text('events')}</>} count={manifest.events.length + manifest.subscriptions.length} flush maxBodyHeight={260}>
      {manifest.events.map((event) => <Row key={`e:${event.name}`}><Badge variant="outline">{text('publishes')}</Badge><span className="font-mono text-xs">{event.name}</span><span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{event.description}</span></Row>)}
      {manifest.subscriptions.map((subscription) => <Row key={`s:${subscription.consumer}`}><Badge variant="secondary">{text('consumes')}</Badge><span className="font-mono text-xs">{subscription.event}</span><Muted mono>{subscription.consumer}</Muted></Row>)}
      {!manifest.events.length && !manifest.subscriptions.length && <Empty label={text('noEvents')} />}
    </Panel>
    <Panel title={<><Workflow className="size-4 text-muted-foreground" />{text('workflows')}</>} count={manifest.workflows.length} flush maxBodyHeight={260}>
      {manifest.workflows.length ? manifest.workflows.map((workflow) => <Row key={workflow.name}><span className="font-mono text-xs">{workflow.name}</span><span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{workflow.description}</span></Row>) : <Empty label={text('noWorkflows')} />}
    </Panel>
    <Panel title={<><CalendarClock className="size-4 text-muted-foreground" />{text('schedules')}</>} count={manifest.schedules.length} flush maxBodyHeight={260}>
      {manifest.schedules.length ? manifest.schedules.map((schedule) => <Row key={schedule.name}>
        <span className="font-mono text-xs">{schedule.name}</span>
        <Badge variant="outline" className="font-mono">{schedule.spec.case === 'cron' ? schedule.spec.value : schedule.spec.case === 'every' ? `every ${durationText(schedule.spec.value)}` : '—'}</Badge>
        <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">→ {schedule.workflow}</span>
        {schedule.paused && <StatusBadge tone="warning">{text('paused')}</StatusBadge>}
      </Row>) : <Empty label={text('noSchedules')} />}
    </Panel>
  </div>;
}

const nodeIcon: Record<NodeKind, ReactNode> = {
  [NodeKind.UNSPECIFIED]: <Box className="size-3.5" />, [NodeKind.COMPONENT]: <Component className="size-3.5" />,
  [NodeKind.DEPENDENCY]: <Cable className="size-3.5" />, [NodeKind.SINGLETON]: <Boxes className="size-3.5" />,
};
/** The dependencies of the instance's tree: the nodes with a readiness. */
function dependencies(state: InstanceState | undefined): NodeStatus[] {
  return state?.nodes.filter((node) => node.kind === NodeKind.DEPENDENCY) ?? [];
}
function ComponentTree({ nodes }: { nodes: NodeStatus[] }) {
  const text = usePlatformText();
  // The instance reports the tree depth-first; depth follows the path segments.
  return <ul className="m-0 list-none py-1.5 pl-0">{nodes.map((node) => {
    const depth = node.path.split('/').length - 1, name = node.path.slice(node.path.lastIndexOf('/') + 1);
    return <li key={node.path} className="flex items-center gap-2 py-1 pr-3 text-xs" style={{ paddingLeft: 12 + depth * 14 }} title={node.path}>
      <span className="text-muted-foreground">{nodeIcon[node.kind]}</span><span className="truncate font-mono">{name}</span>
      <span className="ml-auto shrink-0 text-2xs text-muted-foreground">{enumLabel(NodeKind, node.kind)}{node.optional ? ` · ${text('optional')}` : ''}</span>
    </li>;
  })}</ul>;
}
