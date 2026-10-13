import { useId, useMemo, useState } from 'react';
import { create } from '@bufbuild/protobuf';
import { CatalogServiceClient, GetServiceRequestSchema } from '@gopherex/backplane-api';
import { ApiKind, type Manifest } from '@gopherex/backplane-api/backplanepb/v1/manifest_pb';
import { useBackplane, useClient } from '@gopherex/backplane-react';
import { Badge, Button, ClipboardButton, EmptyState, Input, SelectControl, Panel, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@gopherex/backplane-ui';
import { EditorActions, JSONViewer } from '@gopherex/backplane-editors';
import type { ThemeMode } from '@gopherex/backplane-theme';
import { readAPIDocument, type APIField, type APISection } from './api-document.js';
import { usePlatformText } from './locales.js';
import { QueryState, usePlatformQuery } from './runtime.js';
import { readAPIBundle, type APIFileReader } from './api-bundle.js';

export interface ServiceAPIState { api: number; operation?: string }

/** External, declared API documentation; it never invokes a service endpoint. */
export function ServiceAPI({ service, mode, state, onStateChange }: {
  service: string; mode: ThemeMode; state?: ServiceAPIState; onStateChange?: (state: ServiceAPIState) => void;
}) {
  const client = useClient(CatalogServiceClient), runtime = useBackplane(), text = usePlatformText();
  const query = usePlatformQuery(`external-api:${service}`, (signal) => client.getService(create(GetServiceRequestSchema, { name: service }), { signal }));
  return <div className="grid min-h-0 gap-3 overflow-y-auto pr-1"><QueryState state={query} />
    <div className="flex items-center justify-between gap-3"><p className="m-0 text-sm text-muted-foreground">{text('externalAPIHelp')}</p><Button size="sm" variant="outline" onClick={query.refresh}>{text('refresh')}</Button></div>
    {query.value?.latest && <ServiceAPIDocument key={`${service}:${query.value.latest.version}`} manifest={query.value.latest} mode={mode} state={state} onStateChange={onStateChange}
      readFile={runtime.readSchemaFile ? (hash, file, signal) => runtime.readSchemaFile!(service, hash, file, signal) : undefined} />}
  </div>;
}

/** Presentational viewer for a supplied manifest, also usable by kit consumers. */
export function ServiceAPIDocument({ manifest, mode, state: controlled, onStateChange, readFile }: {
  manifest: Manifest; mode: ThemeMode; state?: ServiceAPIState; onStateChange?: (state: ServiceAPIState) => void; readFile?: APIFileReader;
}) {
  const text = usePlatformText(), [local, setLocal] = useState<ServiceAPIState>({ api: 0 });
  const state = controlled ?? local;
  const update = (next: ServiceAPIState) => { setLocal(next); onStateChange?.(next); };
  const index = Number.isSafeInteger(state.api) && manifest.apis[state.api] ? state.api : 0;
  const route = manifest.apis[index];
  const result = useMemo(() => route ? readAPIDocument(route, manifest) : undefined, [route, manifest]);
  if (!route) return <EmptyState title={text('noAPIs')} />;
  return <div className="grid min-w-0 gap-4">
    <label className="grid gap-1 text-sm">{text('externalEndpoint')}<SelectControl aria-label={text('externalEndpoint')} value={String(index)} onValueChange={(value) => update({ api: Number(value) })}
      options={manifest.apis.map((entry, i) => ({ value: String(i), label: `${ApiKind[entry.kind]} · ${entry.host}${entry.prefix}${entry.port ? ` :${entry.port}` : ''}` }))} /></label>
    {route.schema.case === 'bundle' ? <APIBundleView key={`${route.schema.value.hash}:${route.schema.value.entry}`} bundle={route.schema.value} readFile={readFile} mode={mode} operation={state.operation} onOperation={(operation) => update({ api: index, operation })} /> : <>
      {result && 'problem' in result && <EmptyState title={text(result.problem === 'missing' ? 'apiSchemaMissing' : 'apiSchemaInvalid')} description={result.problem === 'missing' ? text('apiSchemaMissingHelp') : result.detail} />}
      {result && 'document' in result && <APIBrowser key={`${index}:${manifest.version}`} document={result.document} mode={mode} operation={state.operation} onOperation={(operation) => update({ api: index, operation })} />}
    </>}
  </div>;
}

function APIBundleView({ bundle, readFile, ...props }: { bundle: import('@gopherex/backplane-api/backplanepb/v1/manifest_pb').APISchemaBundle; readFile?: APIFileReader; mode: ThemeMode; operation?: string; onOperation: (id: string) => void }) {
  const text = usePlatformText();
  const state = usePlatformQuery(`api-files:${bundle.hash}:${bundle.format}:${bundle.entry}`, async (signal) => {
    if (!readFile) throw new Error('The host client does not provide API file delivery.');
    return readAPIBundle(bundle, readFile, signal);
  });
  return <><QueryState state={state} />{state.value && ('document' in state.value ? <APIBrowser document={state.value.document} {...props} /> : <EmptyState title={text('apiSchemaInvalid')} description={state.value.detail} />)}</>;
}

function APIBrowser({ document, mode, operation, onOperation }: {
  document: import('./api-document.js').APIDocument; mode: ThemeMode; operation?: string; onOperation: (id: string) => void;
}) {
  const text = usePlatformText(), [filter, setFilter] = useState(''), [typeFilter, setTypeFilter] = useState(''), scope = useId();
  const [expanded, setExpanded] = useState(() => new Set(document.schemas.length <= 8 ? document.schemas.map((schema) => schema.name) : []));
  const matching = document.operations.filter((entry) => `${entry.method} ${entry.name} ${entry.title ?? ''}`.toLowerCase().includes(filter.toLowerCase()));
  const selected = matching.find((entry) => entry.id === operation) ?? matching[0];
  const schemaId = (name: string) => `${scope}-schema-${encodeURIComponent(name)}`;
  const openSchema = (name: string) => {
    setTypeFilter(''); setExpanded((current) => new Set([...current, name]));
    requestAnimationFrame(() => { const element = window.document.getElementById(schemaId(name)); element?.scrollIntoView({ block: 'nearest', behavior: 'instant' }); element?.querySelector('summary')?.focus(); });
  };
  const schemaLink = (field: APIField) => field.type.split(/(\s*[|&,<>]\s*|\[|\]|!|\(|\))/).map((part, index) => {
    const name = document.schemas.find((schema) => part.replace(/[!\[\]]/g, '').trim() === schema.name)?.name;
    return name ? <a key={index} href={`#${schemaId(name)}`} className="underline decoration-dotted underline-offset-4" style={{ color: 'var(--link)' }} onClick={(event) => { event.preventDefault(); openSchema(name); }}>{part}</a> : part;
  });
  const sectionTitle = (name: string) => name === 'parameters' ? text('apiParameters') : name === 'requestBody' ? text('apiRequest') : name === 'response' ? text('apiResponse') : name === 'security' ? text('apiSecurity') : name === 'call' ? text('apiCall') : `${text('apiResponse')} ${name}`;
  return <>
    <div className="flex flex-wrap items-center gap-2"><h2 className="m-0 text-lg font-semibold">{document.title || text('externalAPI')}</h2><Badge variant="outline">{document.format}</Badge></div>
    {document.description && <p className="m-0 whitespace-pre-wrap text-sm text-muted-foreground">{document.description}</p>}
    {document.warnings.length > 0 && <div role="status" className="rounded-md border border-border p-3 text-sm"><p>{text('apiUnresolvedRefs')}</p><ul className="list-inside list-disc">{document.warnings.map((ref) => <li className="break-all font-mono text-xs" key={ref}>{ref}</li>)}</ul></div>}
    <div className="grid min-w-0 gap-4 lg:grid-cols-[minmax(15rem,1fr)_minmax(0,3fr)]">
      <Panel className="self-start lg:sticky lg:top-0" title={text('apiOperations')} count={document.operations.length}>
        <Input aria-label={text('apiSearch')} placeholder={text('apiSearch')} value={filter} onChange={(event) => setFilter(event.target.value)} />
        <nav aria-label={text('apiOperations')} className="mt-2 grid max-h-[32rem] gap-1 overflow-auto">
          {matching.map((entry) => <button type="button" key={entry.id} aria-current={selected?.id === entry.id ? 'true' : undefined}
            onClick={() => onOperation(entry.id)} className={`grid gap-1 rounded-md p-2 text-left hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring ${selected?.id === entry.id ? 'bg-muted' : ''}`}>
            <span className="text-xs font-semibold" style={{ color: 'var(--link)' }}>{entry.method}</span><span className="break-all font-mono text-xs">{entry.name}</span>
            {entry.title && <span className="text-xs text-muted-foreground">{entry.title}</span>}
          </button>)}
        </nav>
      </Panel>
      {selected ? <Panel title={<span className="break-all">{selected.method} {selected.name}</span>}><div className="grid gap-4">
        {selected.title && <h3 className="m-0 text-base font-medium">{selected.title}</h3>}
        {selected.deprecated && <Badge variant="secondary">{text('apiDeprecated')}</Badge>}
        {selected.description && <p className="m-0 whitespace-pre-wrap text-sm">{selected.description}</p>}
        {selected.sections.map((section, index) => <APISectionView key={index} section={{ ...section, name: sectionTitle(section.name), description: section.name === 'call' ? text('apiCallHelp') : section.description }} mode={mode} renderType={schemaLink} />)}
      </div></Panel> : <EmptyState title={text('apiNoOperations')} />}
    </div>
    <Panel title={text('apiTypes')} count={document.schemas.length}><div className="grid gap-2">
      {!!document.schemas.length && <div className="flex flex-wrap items-center gap-2"><Input className="min-w-0 flex-1" aria-label={text('apiSearchTypes')} placeholder={text('apiSearchTypes')} value={typeFilter} onChange={(event) => setTypeFilter(event.target.value)} />
        <Button variant="outline" size="sm" onClick={() => setExpanded(new Set(document.schemas.map((schema) => schema.name)))}>{text('apiExpandTypes')}</Button>
        <Button variant="outline" size="sm" onClick={() => setExpanded(new Set())}>{text('apiCollapseTypes')}</Button></div>}
      {document.schemas.filter((schema) => schema.name.toLowerCase().includes(typeFilter.toLowerCase())).map((schema, index) => <details id={schemaId(schema.name)} open={expanded.has(schema.name)} onToggle={(event) => { const open = event.currentTarget.open; setExpanded((current) => { if (current.has(schema.name) === open) return current; const next = new Set(current); if (open) next.add(schema.name); else next.delete(schema.name); return next; }); }} className="scroll-mt-4 rounded-md border border-border p-3" key={`${schema.name}:${index}`}>
        <summary className="cursor-pointer break-all font-mono text-sm">{schema.name}{schema.type && <span className="ml-3 font-sans text-xs text-muted-foreground">{schema.type}</span>}</summary><div className="mt-3"><APISectionView section={schema} mode={mode} hideTitle renderType={schemaLink} /></div>
      </details>)}
      {!document.schemas.length && <p className="text-sm text-muted-foreground">{text('apiNoTypes')}</p>}
      {!!document.schemas.length && !document.schemas.some((schema) => schema.name.toLowerCase().includes(typeFilter.toLowerCase())) && <p className="text-sm text-muted-foreground">{text('apiNoMatchingTypes')}</p>}
    </div></Panel>
    {document.metadata && <details><summary className="cursor-pointer text-sm">{text('apiServersAuth')}</summary><JSONViewer label={text('apiServersAuth')} value={document.metadata} mode={mode} /></details>}
    <details><summary className="cursor-pointer text-sm">{text('apiDocument')}</summary><div className="mt-2 grid gap-2">
      <EditorActions value={document.source} filename="api.json" contentType="application/json" />
      <JSONViewer label={text('apiDocument')} value={document.source} mode={mode} height={400} />
    </div></details>
  </>;
}

function APISectionView({ section, mode, hideTitle, renderType }: { section: APISection; mode: ThemeMode; hideTitle?: boolean; renderType: (field: APIField) => import('react').ReactNode }) {
  const text = usePlatformText();
  return <section className="grid min-w-0 gap-2">{!hideTitle && <h3 className="m-0 text-sm font-medium">{section.name}{section.required && <Badge variant="outline" className="ml-2">{text('required')}</Badge>}</h3>}
    {section.description && <p className="m-0 whitespace-pre-wrap text-sm text-muted-foreground">{section.description}</p>}
    {!!section.fields?.length && <APIFields name={section.name} fields={section.fields} renderType={renderType} />}
    {section.examples?.map((example, index) => <div className="grid min-w-0 gap-2 rounded-md border border-border bg-muted p-3" key={index}><div className="flex flex-wrap items-center justify-between gap-2"><h4 className="m-0 text-xs font-medium">{text('apiExample')} · {example.name}</h4><ClipboardButton label={text('apiCopyExample')} value={typeof example.value === 'string' ? example.value : JSON.stringify(example.value, null, 2)} /></div><pre className="m-0 max-h-80 overflow-auto whitespace-pre-wrap break-all font-mono text-xs">{typeof example.value === 'string' ? example.value : JSON.stringify(example.value, null, 2)}</pre></div>)}
    {section.value !== undefined && <details><summary className="cursor-pointer text-xs text-muted-foreground">{text('apiDefinition')}</summary><JSONViewer label={section.name} value={section.value} mode={mode} height={240} /></details>}
  </section>;
}

function APIFields({ fields, name, renderType }: { fields: APIField[]; name: string; renderType: (field: APIField) => import('react').ReactNode }) {
  const text = usePlatformText();
  return <Table aria-label={name}><TableHeader><TableRow><TableHead>{text('name')}</TableHead><TableHead>{text('apiType')}</TableHead><TableHead>{text('detail')}</TableHead></TableRow></TableHeader>
    <TableBody>{fields.map((field, index) => <TableRow key={index}><TableCell className="font-mono text-xs">{field.name}{field.required && <span className="ml-2 font-sans text-muted-foreground">{text('required')}</span>}</TableCell>
      <TableCell className="break-all font-mono text-xs">{renderType(field)}</TableCell><TableCell className="whitespace-pre-wrap text-sm text-muted-foreground">{field.description}</TableCell></TableRow>)}</TableBody></Table>;
}
