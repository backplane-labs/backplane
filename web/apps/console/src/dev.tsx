import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { AuditExplorer, AutomationPanel, ConfigurationPanel, EventStreams, ExplorePanel, ServiceCatalog, ServiceInspector, ServiceOperations, WiringWorkspace, WorkflowRuns, SchedulesPanel, type WiringState, defaultAuditState } from '@gopherex/backplane-platform-ui';
import { PageHeader, Panel, TabBar } from '@gopherex/backplane-ui';

const views = ['services', 'configuration', 'operations', 'wiring', 'automation', 'events', 'runs', 'schedules', 'audit', 'explore'] as const;

/** Technical acceptance page: every platform composition against the live installation. */
export default function Development({ mode }: { mode: 'dark' | 'light' }) {
  const { t } = useTranslation('host'), [service, setService] = useState('hello'), [view, setView] = useState<typeof views[number]>('services');
  const [wiring, setWiring] = useState<WiringState>({ target: { kind: 'binding', hook: 'hello.Greet' }, view: 'yaml' });
  return <section className="grid gap-4">
    <PageHeader title={t('development')} description={t('developmentDescription')} />
    <TabBar aria-label={t('development')}>{views.map((name) => <button key={name} type="button" aria-pressed={view === name} aria-current={view === name ? 'page' : undefined} onClick={() => setView(name)}
      className="relative -mb-px inline-flex h-9 items-center border-b-2 border-transparent px-2.5 text-sm text-muted-foreground hover:text-foreground aria-[current=page]:border-primary aria-[current=page]:font-medium aria-[current=page]:text-foreground">{t(name)}</button>)}</TabBar>
    {view === 'services' && <><Panel title={t('services')}><ServiceCatalog onSelect={(selected) => setService(selected.name)} /></Panel><ServiceInspector service={service} mode={mode} /></>}
    {view === 'configuration' && <ConfigurationPanel service={service} mode={mode} />}
    {view === 'operations' && <ServiceOperations service={service} mode={mode} />}
    {view === 'wiring' && <div className="h-[760px]"><WiringWorkspace mode={mode} state={wiring} onStateChange={setWiring} /></div>}
    {view === 'automation' && <AutomationPanel service={service} mode={mode} />}
    {view === 'events' && <EventStreams service={service} mode={mode} />}
    {view === 'runs' && <WorkflowRuns service={service} mode={mode} />}
    {view === 'schedules' && <SchedulesPanel service={service} mode={mode} />}
    {view === 'audit' && <AuditLocal mode={mode} />}
    {view === 'explore' && <ExplorePanel mode={mode} />}
  </section>;
}

function AuditLocal({ mode }: { mode: 'dark' | 'light' }) {
  const [state, setState] = useState(defaultAuditState);
  return <AuditExplorer mode={mode} state={state} onStateChange={setState} />;
}
