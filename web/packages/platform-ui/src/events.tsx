import { useState } from 'react';
import { create } from '@bufbuild/protobuf';
import * as api from '@backplane-labs/backplane-api';
import { structToNative } from '@gopherex/schemapb';
import { useClient } from '@backplane-labs/backplane-react';
import { SchemaForm } from '@backplane-labs/backplane-schema-forms';
import { Badge, Button, Checkbox, ConfirmAction, Count, DetailDrawer, EmptyState, Input, KeyValueList, Panel, StatusBadge, StatusDot, Timestamp, type StatusTone } from '@backplane-labs/backplane-ui';
import { CodeEditor, JSONViewer } from '@backplane-labs/backplane-editors';
import { Inbox, Radio, RefreshCw, RotateCcw, Send, Trash2, TriangleAlert } from 'lucide-react';
import { usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { date, durationText, enumLabel } from './format.js';
import { nativeJSON } from './serialization.js';

const kindTone: Record<api.SubscriberKind, StatusTone> = {
  [api.SubscriberKind.UNSPECIFIED]: 'neutral', [api.SubscriberKind.REACTOR]: 'info', [api.SubscriberKind.RULE]: 'accent', [api.SubscriberKind.OTHER]: 'neutral',
};
const PAGE = 50;
const pretty = (data: string) => { try { return JSON.stringify(JSON.parse(data), null, 2); } catch { return data; } };

/** Events a service publishes and consumes, their subscribers, messages and dead letters. */
export function EventStreams(props: { service: string; mode: 'dark' | 'light' }) { return <ServiceEvents key={props.service} {...props} />; }
function ServiceEvents({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const client = useClient(api.EventServiceClient), text = usePlatformText();
  const all = usePlatformQuery(`events:all`, (signal) => client.listEvents(create(api.ListEventsRequestSchema), { signal }));
  const published = all.value?.events.filter((event) => event.service === service) ?? [];
  const consumed = all.value?.events.filter((event) => event.service !== service && event.subscribers.some((subscriber) => subscriber.service === service)) ?? [];
  const [selected, setSelected] = useState<string>(), [publishing, setPublishing] = useState<api.EventInfo>();
  const event = all.value?.events.find((entry) => entry.event === selected) ?? published[0] ?? consumed[0];
  const deadLetters = all.value?.events.flatMap((entry) => entry.subscribers).filter((subscriber) => subscriber.service === service).reduce((sum, subscriber) => sum + subscriber.deadLetters, 0n) ?? 0n;
  const row = (entry: api.EventInfo) => <button key={entry.event} type="button" onClick={() => setSelected(entry.event)} aria-pressed={entry.event === event?.event}
    className="flex w-full items-center gap-2 border-b border-border px-3 py-2 text-left last:border-b-0 hover:bg-raised aria-pressed:bg-primary/10">
    <span className="min-w-0 flex-1"><span className="block truncate font-mono text-xs">{entry.event}</span>
      <span className="block truncate text-2xs text-muted-foreground">{entry.lastTime ? <>{text('lastMessage')} <Timestamp value={date(entry.lastTime)} /></> : text('noMessages')}</span></span>
    {!entry.declared && <Badge variant="outline" className="text-warning">{text('undeclared')}</Badge>}
    <Count>{entry.messages.toString()}</Count>
  </button>;
  return <div className="flex h-full min-h-0 flex-col gap-4">
    {all.value?.natsError && <div className="flex items-center gap-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-sm text-warning" role="status"><TriangleAlert className="size-4" />{all.value.natsError}</div>}
    <div className="grid min-h-0 flex-1 gap-4 xl:grid-rows-[minmax(0,1fr)] xl:grid-cols-[320px_minmax(0,1fr)]">
      <div className="flex min-h-0 min-w-0 flex-col gap-4">
        <Panel fill title={<><Radio className="size-4 text-muted-foreground" />{text('publishes')}</>} count={published.length} flush
          actions={<Button size="icon-sm" variant="ghost" aria-label={text('refresh')} onClick={all.refresh}><RefreshCw className={all.loading ? 'animate-spin' : ''} /></Button>}>
          {published.length ? published.map(row) : <EmptyState className="py-6" title={all.loading ? text('loading') : text('noPublished')} />}
        </Panel>
        <Panel fill title={<><Inbox className="size-4 text-muted-foreground" />{text('consumes')}</>} count={consumed.length} flush>
          {consumed.length ? consumed.map(row) : <EmptyState className="py-6" title={all.loading ? text('loading') : text('noConsumed')} />}
        </Panel>
      </div>
      <div className="flex min-h-0 min-w-0 flex-col gap-4 overflow-auto">
        <StreamState service={service} />
        {event ? <EventDetail key={event.event} event={event} mode={mode} onPublish={() => setPublishing(event)} /> : <Panel><EmptyState icon={<Radio />} title={text('noEvents')} /></Panel>}
        {deadLetters > 0n && <DeadLettersPanel subscriber={service} mode={mode} />}
      </div>
    </div>
    <DetailDrawer open={!!publishing} onOpenChange={(open) => { if (!open) setPublishing(undefined); }} title={text('publishTest', { event: publishing?.event ?? '' })} description={text('publishHelp')}>
      {publishing && <PublishTest key={publishing.event} event={publishing} mode={mode} onDone={all.refresh} />}
    </DetailDrawer>
  </div>;
}

const bytes = (value: bigint) => { const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']; let size = Number(value), unit = 0; while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++; } return `${size.toFixed(unit ? 1 : 0)} ${units[unit]}`; };

/** The service's event and dead-letter streams as NATS holds them. */
function StreamState({ service }: { service: string }) {
  const client = useClient(api.EventServiceClient), text = usePlatformText();
  const state = usePlatformQuery(`stream:${service}`, (signal) => client.getStream(create(api.GetStreamRequestSchema, { service }), { signal }));
  const stream = (label: string, value?: api.StreamState) => <div className="min-w-0 rounded-md border border-border p-2.5">
    <div className="mb-1.5 flex items-center gap-2 text-xs"><span className="font-medium">{label}</span>{value?.exists ? <span className="truncate font-mono text-2xs text-muted-foreground">{value.name}</span> : <StatusBadge tone="neutral">{text('notCreated')}</StatusBadge>}</div>
    {value?.exists && <div className="grid grid-cols-3 gap-2 text-xs">
      {[[text('messages'), value.messages.toString()], [text('size'), bytes(value.bytes)], [text('consumersCount'), String(value.consumers)],
        [text('retention'), value.maxAge ? durationText(value.maxAge) ?? '∞' : '∞'], [text('replicas'), String(value.replicas)], [text('lastMessage'), '']].map(([label, figure]) => <div key={label} className="min-w-0">
        <div className="truncate text-2xs tracking-wide text-muted-foreground uppercase">{label}</div>
        <div className="truncate font-mono">{label === text('lastMessage') ? <Timestamp value={date(value.lastTime)} /> : figure}</div></div>)}
    </div>}
  </div>;
  if (!state.value) return null;
  return <Panel title={text('streams')} description={state.value.deadLetterCounts.length ? text('deadLettersBy', { consumers: state.value.deadLetterCounts.map((count) => `${count.consumer}: ${count.count}`).join(', ') }) : undefined}>
    <div className="grid gap-2 lg:grid-cols-2">{stream(text('eventsStream'), state.value.events)}{stream(text('deadLetterStream'), state.value.deadLetters)}</div>
  </Panel>;
}

function EventDetail({ event, mode, onPublish }: { event: api.EventInfo; mode: 'dark' | 'light'; onPublish: () => void }) {
  const client = useClient(api.EventServiceClient), text = usePlatformText();
  // Newest first: the page starts PAGE messages before the stream tail and walks back.
  const tail = event.lastSeq > BigInt(PAGE) ? event.lastSeq - BigInt(PAGE) + 1n : 1n;
  const [start, setStart] = useState(tail), [message, setMessage] = useState<api.EventMessage>();
  const messages = usePlatformQuery(`messages:${event.event}:${start}`, (signal) => client.peekMessages(create(api.PeekMessagesRequestSchema, { service: event.service, event: event.event, startSeq: start, limit: PAGE }), { signal }));
  return <>
    <Panel title={<span className="font-mono">{event.event}</span>} description={event.description || event.subject}
      actions={event.declared && <Button size="sm" variant="outline" onClick={onPublish}><Send />{text('publishTestShort')}</Button>}>
      <KeyValueList items={[
        { label: text('subject'), value: event.subject, mono: true }, { label: text('messages'), value: event.messages.toString() },
        { label: text('lastSequence'), value: event.lastSeq.toString(), mono: true }, { label: text('lastMessage'), value: <Timestamp value={date(event.lastTime)} /> },
      ]} />
    </Panel>
    <Panel title={text('subscribers')} count={event.subscribers.length} flush maxBodyHeight={260}>
      {!event.subscribers.length && <EmptyState className="py-6" title={text('noSubscribers')} />}
      {event.subscribers.length > 0 && <table className="w-full text-sm">
        <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-xs text-muted-foreground">{[text('consumer'), text('kind'), text('pendingCount'), text('ackPending'), text('redelivered'), text('deadLetters'), text('lastDelivered')].map((label) => <th key={label} className="h-8 px-3 font-medium whitespace-nowrap">{label}</th>)}</tr></thead>
        <tbody>{event.subscribers.map((subscriber) => { const state = subscriber.state; return <tr key={subscriber.durable} className="border-b border-border last:border-b-0">
          <td className="h-10 px-3"><div className="flex items-center gap-2"><StatusDot tone={state?.paused ? 'warning' : 'success'} /><span className="font-mono text-xs">{subscriber.service}</span></div><div className="pl-4 font-mono text-2xs text-muted-foreground">{subscriber.consumer || subscriber.durable}</div></td>
          <td className="px-3"><StatusBadge tone={kindTone[subscriber.kind]} dot={false}>{enumLabel(api.SubscriberKind, subscriber.kind)}</StatusBadge></td>
          <td className={`px-3 font-mono text-xs ${(state?.numPending ?? 0n) > 0n ? 'text-warning' : ''}`}>{state?.numPending.toString() ?? '—'}</td>
          <td className="px-3 font-mono text-xs">{state?.numAckPending.toString() ?? '—'}</td>
          <td className="px-3 font-mono text-xs">{state?.numRedelivered.toString() ?? '—'}</td>
          <td className={`px-3 font-mono text-xs ${subscriber.deadLetters > 0n ? 'text-destructive' : ''}`}>{subscriber.deadLetters.toString()}</td>
          <td className="px-3 text-xs text-muted-foreground"><Timestamp value={date(state?.lastDelivered)} /></td>
        </tr>; })}</tbody>
      </table>}
    </Panel>
    <Panel title={text('messages')} count={messages.value?.messages.length} flush maxBodyHeight={420}
      actions={<Button size="icon-sm" variant="ghost" aria-label={text('refresh')} onClick={messages.refresh}><RefreshCw className={messages.loading ? 'animate-spin' : ''} /></Button>}
      footer={<><span>{text('fromSequence', { seq: start.toString() })}</span><span className="ml-auto flex gap-1">
        <Button size="xs" variant="ghost" disabled={start >= tail} onClick={() => setStart(start + BigInt(PAGE) > tail ? tail : start + BigInt(PAGE))}>{text('newerPage')}</Button>
        <Button size="xs" variant="ghost" disabled={start <= 1n} onClick={() => setStart(start > BigInt(PAGE) ? start - BigInt(PAGE) : 1n)}>{text('olderPage')}</Button></span></>}>
      {!messages.value?.messages.length ? <EmptyState className="py-6" title={messages.loading ? text('loading') : text('noMessages')} /> : <ol className="m-0 list-none p-0">
        {[...messages.value.messages].reverse().map((entry) => <li key={entry.seq.toString()}><button type="button" onClick={() => setMessage(entry)} className="grid w-full grid-cols-[64px_minmax(0,1fr)_auto] items-center gap-3 border-b border-border px-3 py-1.5 text-left text-xs hover:bg-raised">
          <span className="font-mono text-muted-foreground">#{entry.seq.toString()}</span>
          <span className="truncate font-mono">{entry.test && <Badge variant="outline" className="mr-1.5">{text('test')}</Badge>}{entry.data}</span>
          <span className="text-muted-foreground"><Timestamp value={date(entry.storedAt)} /></span>
        </button></li>)}
      </ol>}
    </Panel>
    <DetailDrawer open={!!message} onOpenChange={(open) => { if (!open) setMessage(undefined); }} title={message && `#${message.seq}`} description={message?.subject}>
      {message && <div className="grid gap-4">
        <KeyValueList items={[
          { label: text('eventId'), value: message.cloudEvent?.id ?? '—', mono: true }, { label: text('source'), value: message.cloudEvent?.source ?? '—', mono: true },
          { label: text('instance'), value: message.cloudEvent?.instance || '—', mono: true }, { label: text('time'), value: <Timestamp value={date(message.cloudEvent?.time ?? message.storedAt)} absolute /> },
          ...Object.entries(message.cloudEvent?.extensions ?? {}).map(([key, value]) => ({ key, label: key, value, mono: true })),
        ]} />
        <JSONViewer label={text('payload')} value={pretty(message.data)} mode={mode} />
      </div>}
    </DetailDrawer>
  </>;
}

function PublishTest({ event, mode, onDone }: { event: api.EventInfo; mode: 'dark' | 'light'; onDone: () => void }) {
  const client = useClient(api.EventServiceClient), text = usePlatformText(), action = usePlatformAction<api.PublishTestEventResponse>();
  const [payload, setPayload] = useState('{}'), [key, setKey] = useState('');
  const publish = async (data: string, signal: AbortSignal) => { const response = await client.publishTestEvent(create(api.PublishTestEventRequestSchema, { event: event.event, payload: data, key }), { signal }); onDone(); return response; };
  return <div className="grid gap-4">
    <Input aria-label={text('eventKey')} placeholder={text('eventKey')} value={key} onChange={(entry) => setKey(entry.target.value)} />
    {event.schema ? <SchemaForm schema={event.schema} label={text('payload')} submitLabel={text('publishTestShort')} disabled={action.pending || action.disabled} onSubmit={async (baked, signal) => {
      const response = await action.run((abort) => publish(nativeJSON(structToNative(baked.values)), AbortSignal.any([signal, abort])));
      if (response === undefined) throw new Error('Publish outcome is unavailable');
    }} /> : <><CodeEditor label={text('payload')} value={payload} onChange={setPayload} language="json" mode={mode} height={180} />
      <div><Button disabled={action.pending || action.disabled} onClick={() => void action.run((signal) => publish(payload, signal))}><Send />{text('publishTestShort')}</Button></div></>}
    {action.error !== undefined && <p className="m-0 text-xs text-destructive" role="alert">{text('mutationFailed')}</p>}
    {action.value && <div className="rounded-lg border border-success/30 bg-success/10 px-3 py-2 text-sm" role="status">
      {text(action.value.duplicate ? 'publishedDuplicate' : 'published', { seq: action.value.seq.toString() })} <span className="font-mono text-xs text-muted-foreground">{action.value.id}</span></div>}
  </div>;
}

/** Messages a service's reactors gave up on: inspect, redrive, purge. */
export function DeadLettersPanel({ subscriber, consumer = '', mode }: { subscriber: string; consumer?: string; mode: 'dark' | 'light' }) {
  return <DeadLetters key={`${subscriber}:${consumer}`} subscriber={subscriber} initialConsumer={consumer} mode={mode} />;
}
function DeadLetters({ subscriber, initialConsumer, mode }: { subscriber: string; initialConsumer: string; mode: 'dark' | 'light' }) {
  const client = useClient(api.EventServiceClient), text = usePlatformText(), action = usePlatformAction<string>();
  const [consumer, setConsumer] = useState(initialConsumer), [pages, setPages] = useState<bigint[]>([0n]), [selected, setSelected] = useState<Set<string>>(new Set()), [detail, setDetail] = useState<api.DeadLetter>();
  const start = pages.at(-1)!;
  const state = usePlatformQuery(`dlq:${subscriber}:${consumer}:${start}`, (signal) => client.listDeadLetters(create(api.ListDeadLettersRequestSchema, { subscriber, consumer, startSeq: start, limit: 100 }), { signal }));
  const letters = (state.value?.deadLetters ?? []).filter((letter) => !!letter.message);
  const toggle = (seq: string) => setSelected((old) => { const next = new Set(old); if (next.has(seq)) next.delete(seq); else next.add(seq); return next; });
  const operate = (operation: 'redrive' | 'purge') => async (signal: AbortSignal) => {
    const seqs = [...selected].map(BigInt);
    const response = await action.run(async (abort) => {
      const options = { signal: AbortSignal.any([signal, abort]) }, request = { subscriber, consumer, seqs };
      if (operation === 'redrive') { const result = await client.redriveDeadLetters(create(api.RedriveDeadLettersRequestSchema, request), options); return text('redriven', { count: Number(result.redriven), failed: result.failed.length }); }
      const result = await client.purgeDeadLetters(create(api.PurgeDeadLettersRequestSchema, request), options); return text('purged', { count: Number(result.purged), failed: result.failed.length });
    });
    if (response === undefined) throw new Error('Dead-letter outcome is unavailable'); setSelected(new Set()); setDetail(undefined); state.refresh();
  };
  return <Panel title={<><TriangleAlert className="size-4 text-destructive" />{text('deadLetters')}</>} count={letters.length} flush maxBodyHeight={360}
    actions={<>
      <div className="flex gap-1" role="group" aria-label={text('consumer')}>
        <button type="button" aria-pressed={!consumer} onClick={() => { setConsumer(''); setPages([0n]); setSelected(new Set()); }} className="h-6 rounded-sm border border-border px-2 text-xs text-muted-foreground aria-pressed:bg-primary/10 aria-pressed:text-foreground">{text('allConsumers')}</button>
        {state.value?.counts.map((count) => <button key={count.consumer} type="button" aria-pressed={consumer === count.consumer} onClick={() => { setConsumer(count.consumer); setPages([0n]); setSelected(new Set()); }}
          className="h-6 rounded-sm border border-border px-2 font-mono text-xs text-muted-foreground aria-pressed:bg-primary/10 aria-pressed:text-foreground">{count.consumer} · {count.count.toString()}</button>)}
      </div>
      <ConfirmAction trigger={<><RotateCcw className="size-3.5" />{text('redrive')}</>} title={text('redriveConfirm', { count: selected.size })} description={text('redriveHelp')} disabled={!selected.size || !consumer || action.pending || action.disabled} onConfirm={operate('redrive')} />
      <ConfirmAction trigger={<><Trash2 className="size-3.5" />{text('purge')}</>} title={text('purgeConfirm', { count: selected.size })} description={text('purgeHelp')} disabled={!selected.size || action.pending || action.disabled} onConfirm={operate('purge')} />
    </>}
    footer={<>{action.value && <span role="status">{action.value}</span>}{action.error !== undefined && <span className="text-destructive" role="alert">{text('mutationFailed')}</span>}
      {!consumer && <span>{text('redriveNeedsConsumer')}</span>}
      <span className="ml-auto flex gap-1"><Button size="xs" variant="ghost" disabled={pages.length < 2} onClick={() => setPages(pages.slice(0, -1))}>{text('previous')}</Button>
        <Button size="xs" variant="ghost" disabled={!state.value?.nextSeq} onClick={() => setPages([...pages, state.value!.nextSeq])}>{text('next')}</Button></span></>}>
    {!letters.length ? <EmptyState className="py-6" title={state.loading ? text('loading') : text('noDeadLetters')} /> : <table className="w-full text-sm">
      <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-xs text-muted-foreground">
        <th className="w-8 px-3"><Checkbox aria-label={text('selectAll')} checked={letters.length > 0 && letters.every((letter) => selected.has(letter.message!.seq.toString()))} onCheckedChange={(checked) => setSelected(checked ? new Set(letters.map((letter) => letter.message!.seq.toString())) : new Set())} /></th>
        {[text('sequence'), text('consumer'), text('failure'), text('deliveries'), text('time')].map((label) => <th key={label} className="h-8 px-3 font-medium">{label}</th>)}</tr></thead>
      <tbody>{letters.map((letter) => { const seq = letter.message!.seq.toString(); return <tr key={seq} className="cursor-pointer border-b border-border last:border-b-0 hover:bg-raised" onClick={() => setDetail(letter)}>
        <td className="px-3" onClick={(event) => event.stopPropagation()}><Checkbox aria-label={text('selectRow', { seq })} checked={selected.has(seq)} onCheckedChange={() => toggle(seq)} /></td>
        <td className="h-9 px-3 font-mono text-xs">#{seq}</td><td className="px-3 font-mono text-xs">{letter.consumer}</td>
        <td className="max-w-md truncate px-3 text-xs text-destructive" title={letter.error}>{letter.error}</td>
        <td className="px-3 font-mono text-xs">{letter.delivered}</td><td className="px-3 text-xs text-muted-foreground"><Timestamp value={date(letter.message!.storedAt)} /></td>
      </tr>; })}</tbody>
    </table>}
    <DetailDrawer open={!!detail} onOpenChange={(open) => { if (!open) setDetail(undefined); }} title={detail && `#${detail.message?.seq}`} description={detail?.eventSubject}>
      {detail && <div className="grid gap-4">
        <div className="rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive"><pre className="m-0 font-mono text-xs whitespace-pre-wrap">{detail.error}</pre></div>
        <KeyValueList items={[{ label: text('consumer'), value: detail.consumer, mono: true }, { label: text('deliveries'), value: detail.delivered }, { label: text('event'), value: detail.message?.event ?? '—', mono: true }]} />
        <JSONViewer label={text('payload')} value={pretty(detail.message?.data ?? '')} mode={mode} />
      </div>}
    </DetailDrawer>
  </Panel>;
}
