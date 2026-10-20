import { useMemo, useState } from 'react';
import { EmptyState, Panel } from '@backplane-labs/backplane-ui';
import { TimeSeriesChart, formatValue } from '@backplane-labs/backplane-charts';
import { Activity } from 'lucide-react';
import { usePlatformText } from './locales.js';
import type { ExploreRun } from './explore.js';
import { queryUnit } from './explore-query.js';

// Canvas plots need concrete colors, not CSS variables.
const palette = ['#25e2a5', '#4dabf7', '#fcc419', '#ff8787', '#b197fc', '#63e6be', '#74c0fc', '#ffa94d', '#e599f7', '#a9e34b'];
const format = (value: number) => !Number.isFinite(value) ? String(value) : Math.abs(value) >= 1000 || (Math.abs(value) < 0.01 && value !== 0) ? value.toExponential(2) : Number(value.toFixed(3)).toString();

/** Time series with a legend table: the labels that differ between series, and last/min/avg/max. */
export function MetricResults({ run, mode }: { run: ExploreRun; mode: 'dark' | 'light' }) {
  const text = usePlatformText(), unit = useMemo(() => queryUnit(run.query), [run.query]);
  const series = useMemo(() => run.response.result.case === 'timeSeries' ? run.response.result.value.series : [], [run]);
  const varying = useMemo(() => {
    const keys = [...new Set(series.flatMap((entry) => Object.keys(entry.labels)))];
    return series.length > 1 ? keys.filter((key) => new Set(series.map((entry) => entry.labels[key] ?? '')).size > 1) : keys.filter((key) => key !== '__name__');
  }, [series]);
  const [hidden, setHidden] = useState<Set<number>>(new Set());
  const rows = series.map((entry, index) => {
    const values = entry.samples.map((sample) => Number(sample.value)).filter(Number.isFinite);
    const label = varying.map((key) => `${key}=${entry.labels[key] ?? ''}`).join(', ') || entry.labels.__name__ || String(index + 1);
    return { index, label, color: palette[index % palette.length]!, last: values.at(-1) ?? NaN, min: Math.min(...values), max: Math.max(...values), avg: values.reduce((sum, value) => sum + value, 0) / (values.length || 1) };
  });
  const chart = series.flatMap((entry, index) => hidden.has(index) ? [] : [{ id: String(index), label: rows[index]!.label, color: rows[index]!.color,
    points: entry.samples.map((sample) => ({ x: Number(sample.timestampSeconds) * 1000, value: sample.value })) }]);
  if (!series.length) return <Panel fill><EmptyState icon={<Activity />} title={text('noSeries')} description={text('noSeriesHint')} /></Panel>;
  return <div className="flex min-h-0 flex-1 flex-col gap-3">
    <Panel title={<><Activity className="size-4 text-muted-foreground" />{text('metrics')}</>} count={series.length} className="shrink-0">
      <TimeSeriesChart label={text('metrics')} series={chart} mode={mode} height={300} unit={unit} controls={false} timeZone={Intl.DateTimeFormat().resolvedOptions().timeZone} partial={run.response.info?.partial || run.response.info?.truncated} />
    </Panel>
    <Panel fill title={text('seriesLegend')} flush>
      <table aria-label={text('seriesLegend')} className="w-full text-xs">
        <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-muted-foreground">
          <th className="h-8 px-3 font-medium">{text('series')}</th>{(['last', 'min', 'avg', 'max'] as const).map((key) => <th key={key} className="h-8 px-3 text-right font-medium">{text(`stat_${key}`)}</th>)}
        </tr></thead>
        <tbody>{rows.map((row) => <tr key={row.index} tabIndex={0} aria-selected={!hidden.has(row.index)} className={`cursor-pointer border-b border-border last:border-b-0 hover:bg-raised ${hidden.has(row.index) ? 'opacity-40' : ''}`}
          onClick={() => setHidden((old) => { const next = new Set(old); if (next.has(row.index)) next.delete(row.index); else next.add(row.index); return next; })}
          onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); event.currentTarget.click(); } }}>
          <td className="max-w-0 px-3 py-1.5"><span className="flex min-w-0 items-center gap-2"><span className="h-0.5 w-3 shrink-0 rounded-full" style={{ background: row.color }} /><span className="truncate font-mono" title={row.label}>{row.label}</span></span></td>
          {[row.last, row.min, row.avg, row.max].map((value, index) => <td key={index} className="px-3 text-right font-mono tabular-nums">{unit && Number.isFinite(value) ? formatValue(value, unit) : format(value)}</td>)}
        </tr>)}</tbody>
      </table>
    </Panel>
  </div>;
}
