import { useEffect, useMemo, useRef, useState } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { tokens, typography } from '@backplane-labs/backplane-theme';
import { Button, DataTable, Input } from '@backplane-labs/backplane-ui';
import { exact, prepareSeries } from './model.js';
import { useChartText } from './locales.js';
import type { ChartProps } from './types.js';

export default function Plot({ label, series, mode, kind = 'line', height = 280, maxPoints, timeZone = 'UTC', loading, error, partial, onRangeChange, controls = true }: ChartProps) {
  const host = useRef<HTMLDivElement>(null), plot = useRef<uPlot | null>(null), callback = useRef(onRangeChange); callback.current = onRangeChange;
  const prepared = useMemo(() => prepareSeries(series, maxPoints), [series, maxPoints]);
  const [hidden, setHidden] = useState<Set<string>>(new Set()), [showData, setShowData] = useState(false), [cursor, setCursor] = useState<number>();
  const [from, setFrom] = useState(''), [to, setTo] = useState(''), [rangeError, setRangeError] = useState(false); const text = useChartText();
  useEffect(() => {
    if (!host.current || !prepared.x.length) return;
    const c = tokens[mode], colors = [c['chart-1'], c['chart-2'], c['chart-3'], c['chart-4'], c['chart-5']];
    // Label precision follows the visible span, so ticks never collide.
    const span = (prepared.x.at(-1) ?? 0) - (prepared.x[0] ?? 0);
    const formatter = new Intl.DateTimeFormat('en', span > 2 * 86400e3 ? { timeZone, month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }
      : span > 600e3 ? { timeZone, hour: '2-digit', minute: '2-digit', hourCycle: 'h23' } : { timeZone, hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' });
    const view = new uPlot({ width: Math.max(200, host.current.clientWidth), height,
      scales: { x: { time: false } }, legend: { show: false },
      cursor: { drag: { x: true, y: false, setScale: false } },
      series: [{}, ...prepared.series.map((series, index): uPlot.Series => ({ label: series.label, stroke: series.color ?? colors[index % colors.length], width: 1.5,
        fill: kind === 'line' ? undefined : series.color ?? colors[index % colors.length], paths: kind === 'line' ? undefined : uPlot.paths.bars?.({ size: [.75, 64] }),
        points: { show: false }, spanGaps: false,
      }))],
      axes: [
        { stroke: c['muted-foreground'], grid: { stroke: c.border, width: 1 }, ticks: { stroke: c.border }, font: `11px ${typography.fontFamily}`, space: 90, values: (_u, splits) => splits.map((x) => kind === 'histogram' ? String(x) : formatter.format(x)) },
        { stroke: c['muted-foreground'], grid: { stroke: c.border, width: 1 }, ticks: { stroke: c.border }, font: `11px ${typography.fontFamily}`, size: 56 },
      ],
      hooks: { setCursor: [(view) => setCursor(view.cursor.idx ?? undefined)], setSelect: [(view) => {
        if (view.select.width > 0) callback.current?.({ from: view.posToVal(view.select.left, 'x'), to: view.posToVal(view.select.left + view.select.width, 'x') });
      }] },
    }, prepared.data as uPlot.AlignedData, host.current);
    plot.current = view;
    const resize = new ResizeObserver(() => { if (host.current) view.setSize({ width: Math.max(200, host.current.clientWidth), height }); }); resize.observe(host.current);
    return () => { resize.disconnect(); view.destroy(); plot.current = null; };
  }, [prepared, mode, height, kind, timeZone]);
  useEffect(() => { prepared.series.forEach((series, index) => plot.current?.setSeries(index + 1, { show: !hidden.has(series.id) })); }, [prepared, hidden, mode, height, kind, timeZone]);
  const rows = useMemo(() => prepared.series.flatMap((series) => series.points.map((point, index) => ({ ...point, id: `${series.id}:${index}`, series: series.label }))), [prepared]);
  return <section aria-label={label} style={{ display: 'grid', gap: 8 }}>
    {loading && <p role="status">{text('loading')}</p>}{error && <p role="alert">{error}</p>}{partial && <p role="status">{text('partial')}</p>}
    {prepared.sampled && <p role="status">{text('sampled')}</p>}{prepared.approximate && <p role="status">{text('approximate')}</p>}{prepared.invalid && <p role="status">{text('invalidData')}</p>}
    {!loading && !prepared.x.length && <p>{text('empty')}</p>}
    <div ref={host} role="img" aria-label={label} style={{ minWidth: 0 }} />
    {controls && <div role="group" aria-label={text('series')} style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>{prepared.series.map((series) => <Button key={series.id} type="button" variant="outline" aria-pressed={!hidden.has(series.id)} onClick={() => setHidden((previous) => { const next = new Set(previous); if (next.has(series.id)) next.delete(series.id); else next.add(series.id); return next; })}>{series.label}</Button>)}</div>}
    {cursor !== undefined && <output aria-label={text('value')} style={{ fontSize: 11, fontFamily: typography.fontFamilyMonospace, color: 'var(--muted-foreground)' }}>{prepared.series.map((series, index) => `${series.label}: ${exact(prepared.values[index].get(prepared.x[cursor]) ?? null)}`).join(' · ')}</output>}
    {onRangeChange && controls && <form style={{ display: 'flex', gap: 8 }} onSubmit={(event) => {
      event.preventDefault(); const start = Number(from), end = Number(to); const valid = from.trim() && to.trim() && Number.isFinite(start) && Number.isFinite(end) && start < end;
      setRangeError(!valid); if (valid) onRangeChange({ from: start, to: end });
    }}><Input aria-label={text('rangeStart')} value={from} onChange={(event) => setFrom(event.target.value)} /><Input aria-label={text('rangeEnd')} value={to} onChange={(event) => setTo(event.target.value)} /><Button type="submit">{text('applyRange')}</Button></form>}
    {rangeError && <p role="alert">{text('invalidRange')}</p>}
    {controls && <Button type="button" variant="outline" size="sm" onClick={() => setShowData(!showData)}>{text(showData ? 'hideData' : 'showData')}</Button>}
    {showData && <DataTable label={label} data={rows} getRowId={(row) => row.id} columns={[
      { id: 'series', label: text('series'), value: (row) => row.series }, { id: 'x', label: kind === 'histogram' ? 'x' : text('rangeStart'), value: (row) => row.x }, { id: 'value', label: text('value'), value: (row) => row.value, render: (row) => exact(row.value) },
    ]} height={240} />}
  </section>;
}
