import { lazy, Suspense, type ReactNode } from 'react';
import { DataTable } from '@backplane-labs/backplane-ui';
import { exact } from './model.js';
export { formatValue } from './model.js';
import { useChartText } from './locales.js';
import type { ChartProps, ChartSeries, HeatmapProps, ValueProps, VisualizationControlsProps } from './types.js';
export type * from './types.js';
export { chartsEnglish } from './locales.js';
const Plot = lazy(() => import('./plot.js')), Heat = lazy(() => import('./heatmap.js'));
const Value = lazy(() => import('./grafana.js').then((module) => ({ default: module.ValueVisualization })));
const Controls = lazy(() => import('./grafana.js').then((module) => ({ default: module.Controls })));
function Deferred({ children }: { children: ReactNode }) { const text = useChartText(); return <Suspense fallback={<span role="status">{text('loading')}</span>}>{children}</Suspense>; }
export function TimeSeriesChart(props: ChartProps) { return <Deferred><Plot {...props} /></Deferred>; }
export function BarChart(props: Omit<ChartProps, 'kind'>) { return <TimeSeriesChart {...props} kind="bars" />; }
export function Histogram(props: Omit<ChartProps, 'kind'>) { return <TimeSeriesChart {...props} kind="histogram" />; }
export function Heatmap(props: HeatmapProps) { return <Deferred><Heat {...props} /></Deferred>; }
export function Stat(props: ValueProps) { return <Deferred><Value {...props} kind="stat" /></Deferred>; }
export function BarGauge(props: ValueProps) { return <Deferred><Value {...props} kind="bar" /></Deferred>; }
export function RadialGauge(props: ValueProps) { return <Deferred><Value {...props} kind="radial" /></Deferred>; }
export function VisualizationControls(props: VisualizationControlsProps) { return <Deferred><Controls {...props} /></Deferred>; }
export function SeriesTable({ label, series }: { label: string; series: readonly ChartSeries[] }) {
  const text = useChartText();
  return <DataTable label={label} data={[...series]} getRowId={(series) => series.id} columns={[
    { id: 'label', label: text('series'), value: (series) => series.label }, { id: 'count', label: text('samples'), value: (series) => series.points.length },
    { id: 'last', label: text('last'), value: (series) => series.points.at(-1)?.value, render: (series) => exact(series.points.at(-1)?.value ?? null) },
  ]} height={200} />;
}
export function VisualizationPanel({ title, actions, children }: { title: string; actions?: ReactNode; children: ReactNode }) {
  return <section aria-label={title} style={{ border: '1px solid var(--border)', borderRadius: 4, padding: 12, minWidth: 0 }}><header style={{ display: 'flex', justifyContent: 'space-between', gap: 8 }}><h2>{title}</h2>{actions}</header>{children}</section>;
}
