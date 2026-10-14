import { StrictMode, useLayoutEffect, useMemo, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { I18nextProvider, useTranslation } from 'react-i18next';
import { dateTime, type TimeRange } from '@grafana/data';
import { BigValue, BigValueColorMode, BigValueGraphMode, ThemeContext, TimeRangePicker } from '@grafana/ui';
import { createGrafanaTheme, tokens, type ThemeMode } from '@backplane-labs/backplane-theme';
import {
  Badge, Button, Input, Label, Table, TableHeader, TableBody, TableHead, TableRow, TableCell, TableCaption,
  Dialog, DialogTrigger, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter,
  Select, SelectTrigger, SelectContent, SelectItem, SelectValue,
} from '@backplane-labs/backplane-ui';
import { QueryEditor } from './query-editor';
import { i18n } from './i18n';
import '@fontsource/ibm-plex-sans/400.css';
import '@fontsource/ibm-plex-sans/500.css';
import '@fontsource/ibm-plex-mono/400.css';
import '@backplane-labs/backplane-ui/style.css';
import '@backplane-labs/backplane-theme/style.css';
import './style.css';

const sources = [
  { name: 'hello', instance: 'hello-01', healthy: true },
  { name: 'formatter', instance: 'formatter-01', healthy: true },
  { name: 'kratos', instance: 'kratos-01', healthy: false },
];

function Fixture() {
  const { t } = useTranslation();
  const [mode, setMode] = useState<ThemeMode>('dark');
  const theme = useMemo(() => createGrafanaTheme(mode), [mode]);
  const [filter, setFilter] = useState('');
  const [name, setName] = useState('hello');
  const [signal, setSignal] = useState('logs');
  const [applied, setApplied] = useState('');
  const [open, setOpen] = useState(false);
  const [timezone, setTimezone] = useState('utc');
  const [range, setRange] = useState<TimeRange>(() => ({ from: dateTime().subtract(15, 'minute'), to: dateTime(), raw: { from: 'now-15m', to: 'now' } }));
  const rows = sources.filter((source) => source.name.includes(filter.toLowerCase()));
  useLayoutEffect(() => {
    document.documentElement.classList.toggle('dark', mode === 'dark');
    document.documentElement.dataset.theme = mode;
  }, [mode]);
  function move(direction: number) {
    const width = range.to.valueOf() - range.from.valueOf();
    const from = dateTime(range.from.valueOf() + direction * width);
    const to = dateTime(range.to.valueOf() + direction * width);
    setRange({ from, to, raw: { from, to } });
  }
  return <ThemeContext.Provider value={theme}>
    <main>
      <header><div><h1>{t('title')}</h1><p>{t('intro')}</p></div>
        <Button variant="outline" onClick={() => setMode(mode === 'dark' ? 'light' : 'dark')}>{t(mode === 'dark' ? 'light' : 'dark')}</Button>
      </header>
      <div className="fixture-note">{t('fixture')}</div>
      <section aria-label={t('range')} className="range-row">
        <h2>{t('range')}</h2>
        <TimeRangePicker value={range} timeZone={timezone} onChange={setRange} onChangeTimeZone={setTimezone}
          onMoveBackward={() => move(-1)} onMoveForward={() => move(1)} onZoom={() => {
            const span = range.to.valueOf() - range.from.valueOf();
            const from = dateTime(range.from.valueOf() - span / 2), to = dateTime(range.to.valueOf() + span / 2);
            setRange({ from, to, raw: { from, to } });
          }} />
      </section>
      <section aria-label={t('sources')}>
        <div className="section-title"><h2>{t('sources')}</h2><Label htmlFor="filter">{t('filter')}</Label><Input id="filter" placeholder={t('filterPlaceholder')} value={filter} onChange={(e) => setFilter(e.target.value)} /></div>
        <Table>
          <TableCaption>{t('rows', { count: rows.length })}</TableCaption>
          <TableHeader><TableRow><TableHead>{t('source')}</TableHead><TableHead>{t('instance')}</TableHead><TableHead>{t('state')}</TableHead></TableRow></TableHeader>
          <TableBody>{rows.map((source) => <TableRow key={source.instance}><TableCell>{source.name}</TableCell><TableCell><code>{source.instance}</code></TableCell><TableCell><Badge variant="secondary">{t(source.healthy ? 'healthy' : 'waiting')}</Badge></TableCell></TableRow>)}
            {!rows.length && <TableRow><TableCell colSpan={3}>{t('noSources')}</TableCell></TableRow>}
          </TableBody>
        </Table>
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogTrigger asChild><Button variant="outline">{t('inspect')}</Button></DialogTrigger>
          <DialogContent><DialogHeader><DialogTitle>{t('inspectTitle')}</DialogTitle><DialogDescription>{t('inspectDescription')}</DialogDescription></DialogHeader>
            <form onSubmit={(e) => { e.preventDefault(); setApplied(name); setOpen(false); }}>
              <div className="field"><Label htmlFor="display-name">{t('displayName')}</Label><Input id="display-name" value={name} onChange={(e) => setName(e.target.value)} required /></div>
              <div className="field"><Label htmlFor="signal">{t('signal')}</Label><Select value={signal} onValueChange={setSignal}><SelectTrigger id="signal"><SelectValue /></SelectTrigger><SelectContent>{['logs', 'metrics', 'traces'].map((value) => <SelectItem key={value} value={value}>{t(value)}</SelectItem>)}</SelectContent></Select></div>
              <DialogFooter><Button type="submit">{t('apply')}</Button></DialogFooter>
            </form>
          </DialogContent>
        </Dialog>
        <p role="status">{applied ? t('applied', { name: applied }) : ''}</p>
      </section>
      <div className="bottom-row">
        <section aria-label={t('query')}><h2>{t('query')}</h2><QueryEditor mode={mode} /><p>{t('precision')}: <code>9007199254740993</code></p></section>
        <section aria-label={t('requests')}><BigValue theme={theme} width={240} height={150} value={{ numeric: 1284, text: '1,284', title: t('requests'), color: tokens[mode].primary }} colorMode={BigValueColorMode.Value} graphMode={BigValueGraphMode.None} /></section>
      </div>
    </main>
  </ThemeContext.Provider>;
}

createRoot(document.getElementById('root')!).render(<StrictMode><I18nextProvider i18n={i18n}><Fixture /></I18nextProvider></StrictMode>);
import 'virtual:backplane-grafana-assets';
