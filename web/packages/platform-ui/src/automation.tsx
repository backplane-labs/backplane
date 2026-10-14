import { useCallback } from 'react';
import { create } from '@bufbuild/protobuf';
import * as api from '@backplane-labs/backplane-api';
import { useClient, useSnapshotWatch } from '@backplane-labs/backplane-react';
import { Badge, Button, EmptyState, Panel, StatusBadge, Timestamp } from '@backplane-labs/backplane-ui';
import { ArrowUpRight, Cable, Workflow, Zap } from 'lucide-react';
import { usePlatformText } from './locales.js';
import { date, enumLabel } from './format.js';
import { bindingTone, ruleTone, type WiringTarget } from './wiring/editor.js';

/**
 * A service's wiring at a glance: its hooks with their bindings, the rules on
 * its events, and the bindings and rules elsewhere that call its activities.
 * Editing happens in Wiring; every row opens it there.
 */
export function AutomationPanel({ service, onOpenWiring }: { service: string; mode?: 'dark' | 'light'; onOpenWiring?: (target?: WiringTarget) => void }) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText();
  const hookWatch = useSnapshotWatch(useCallback((signal: AbortSignal) => bindings.watchBindings(create(api.WatchBindingsRequestSchema), { signal }), [bindings]));
  const ruleWatch = useSnapshotWatch(useCallback((signal: AbortSignal) => rules.watchRules(create(api.WatchRulesRequestSchema), { signal }), [rules]));
  const allHooks = hookWatch.value?.bindings ?? [], allRules = (ruleWatch.value?.rules ?? []).filter((rule) => !rule.current?.deleted);
  const hooks = allHooks.filter((entry) => (entry.service || entry.hook.split('.')[0]) === service);
  const own = allRules.filter((rule) => rule.current?.definition?.event.startsWith(`${service}.`));
  // Wiring elsewhere that calls this service's activities: what depends on its contract.
  const calls = (steps?: Record<string, api.Step>) => Object.entries(steps ?? {}).filter(([, step]) => step.activity.startsWith(`${service}.`) || step.undo.startsWith(`${service}.`));
  const usedBy = [
    ...allHooks.filter((entry) => entry.current && !entry.current.deleted).flatMap((entry) => calls(entry.current?.definition?.steps).map(([name, step]) => ({ key: `b:${entry.hook}:${name}`, target: { kind: 'binding' as const, hook: entry.hook }, title: entry.hook, detail: `${name} → ${step.activity}`, kind: 'binding' as const }))),
    ...allRules.flatMap((rule) => calls(rule.current?.definition?.steps).map(([name, step]) => ({ key: `r:${rule.id}:${name}`, target: { kind: 'rule' as const, id: rule.id }, title: rule.current?.name || rule.id, detail: `${rule.current?.definition?.event} · ${name} → ${step.activity}`, kind: 'rule' as const }))),
  ];
  const row = 'flex w-full items-center gap-3 border-b border-border px-3 py-2 text-left last:border-b-0 hover:bg-raised disabled:hover:bg-transparent';
  const loading = hookWatch.status === 'loading' || ruleWatch.status === 'loading';
  return <div className="grid min-h-0 content-start gap-4 xl:grid-cols-3">
    <Panel flush maxBodyHeight={480} title={<><Cable className="size-4 text-muted-foreground" />{text('bindings')}</>} count={hooks.length} description={text('bindingsHelp')}
      actions={onOpenWiring && <Button size="xs" variant="ghost" onClick={() => onOpenWiring()}>{text('openWiring')}<ArrowUpRight /></Button>}>
      {!hooks.length && <EmptyState className="py-8" title={loading ? text('loading') : text('noHooks')} />}
      {hooks.map((entry) => { const steps = Object.values(entry.current?.definition?.steps ?? {}); return <button key={entry.hook} type="button" className={row} disabled={!onOpenWiring} onClick={() => onOpenWiring?.({ kind: 'binding', hook: entry.hook })}>
        <span className="min-w-0 flex-1"><span className="flex items-center gap-1.5"><span className="truncate font-mono text-sm">{entry.hook}</span>{entry.required && <Badge variant="outline">{text('required')}</Badge>}</span>
          <span className="block truncate text-xs text-muted-foreground">{entry.current && !entry.current.deleted
            ? <>v{entry.current.version.toString()} · {steps.map((step) => step.activity).join(', ') || text('noSteps')} · <Timestamp value={date(entry.current.createdAt)} /></>
            : entry.description || text('noBinding')}</span></span>
        <StatusBadge tone={bindingTone[entry.state]}>{enumLabel(api.BindingState, entry.state)}</StatusBadge>
      </button>; })}
    </Panel>
    <Panel flush maxBodyHeight={480} title={<><Zap className="size-4 text-muted-foreground" />{text('rules')}</>} count={own.length} description={text('rulesHelp')}>
      {!own.length && <EmptyState className="py-8" title={loading ? text('loading') : text('noRules')} />}
      {own.map((rule) => <button key={rule.id} type="button" className={row} disabled={!onOpenWiring} onClick={() => onOpenWiring?.({ kind: 'rule', id: rule.id })}>
        <span className="min-w-0 flex-1"><span className="block truncate text-sm">{rule.current?.name || rule.id}</span>
          <span className="block truncate font-mono text-xs text-muted-foreground">{rule.current?.definition?.event} → {Object.values(rule.current?.definition?.steps ?? {}).map((step) => step.activity).join(', ')}</span></span>
        <StatusBadge tone={ruleTone[rule.state]}>{enumLabel(api.RuleState, rule.state)}</StatusBadge>
      </button>)}
    </Panel>
    <Panel flush maxBodyHeight={480} title={<><Workflow className="size-4 text-muted-foreground" />{text('usedBy')}</>} count={usedBy.length} description={text('usedByHelp')}>
      {!usedBy.length && <EmptyState className="py-8" title={loading ? text('loading') : text('notUsed')} />}
      {usedBy.map((entry) => <button key={entry.key} type="button" className={row} disabled={!onOpenWiring} onClick={() => onOpenWiring?.(entry.target)}>
        {entry.kind === 'binding' ? <Cable className="size-3.5 shrink-0 text-muted-foreground" /> : <Zap className="size-3.5 shrink-0 text-muted-foreground" />}
        <span className="min-w-0 flex-1"><span className={`block truncate text-sm ${entry.kind === 'binding' ? 'font-mono' : ''}`}>{entry.title}</span>
          <span className="block truncate font-mono text-xs text-muted-foreground">{entry.detail}</span></span>
      </button>)}
    </Panel>
  </div>;
}
