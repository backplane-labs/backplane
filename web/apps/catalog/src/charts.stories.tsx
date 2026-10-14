import { useState } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { TimeSeriesChart, Histogram, BarChart, Heatmap, Stat, BarGauge, RadialGauge, SeriesTable, VisualizationControls, VisualizationPanel, type ChartSeries, type VisualizationSettings } from '@backplane-labs/backplane-charts';
import { Stack } from '@backplane-labs/backplane-ui';
const origin = Date.UTC(2026, 8, 29);
const series: ChartSeries[] = [{ id: 'requests', label: 'Requests', points: Array.from({ length: 20000 }, (_, index) => ({ x: origin + index * 1000, value: index === 10001 ? 200 : Math.sin(index / 200) * 10 + 20 })) },
  { id: 'sequence', label: 'Sequence', points: [{ x: origin, value: 18446744073709551615n }, { x: origin + 20000000, value: 18446744073709551615n }] }];
function Plots({ mode }: { mode: 'dark' | 'light' }) {
  const [range, setRange] = useState('');
  return <Stack><h1>Charts and original values</h1><VisualizationPanel title="Requests over time"><TimeSeriesChart label="Requests over time" series={series} mode={mode} maxPoints={1000} partial onRangeChange={(range) => setRange(JSON.stringify(range))} /></VisualizationPanel>
    <output aria-label="Selected range">{range}</output><SeriesTable label="Series summary" series={series} />
    <Histogram label="Duration distribution" mode={mode} series={[{ id: 'latency', label: 'Latency', points: [1, 2, 3, 4].map((x) => ({ x, value: x * x })) }]} />
    <BarChart label="Request counts" mode={mode} series={[{ id: 'counts', label: 'Counts', points: [1, 2, 3].map((x) => ({ x: origin + x * 1000, value: x * 3 })) }]} />
    <TimeSeriesChart label="Empty series" mode={mode} series={[]} />
  </Stack>;
}
function Values({ mode }: { mode: 'dark' | 'light' }) {
  const [value, setValue] = useState<VisualizationSettings>({ unit: 'none', stats: ['lastNotNull'], color: mode === 'dark' ? '#63f2bf' : '#008362', thresholds: [{ value: 80, color: '#ff8787' }] });
  return <Stack><h1>Visualization values and settings</h1><Stat label="Sequence number" value={18446744073709551615n} mode={mode} />
    <BarGauge label="Capacity" value="74" min={0} max={100} mode={mode} color={value.color} thresholds={value.thresholds} />
    <RadialGauge label="Utilization" value="74" min={0} max={100} mode={mode} color={value.color} thresholds={value.thresholds} />
    <VisualizationControls mode={mode} value={value} onChange={setValue} /><output aria-label="Visualization settings">{JSON.stringify(value)}</output>
  </Stack>;
}
function Heat({ mode }: { mode: 'dark' | 'light' }) {
  const [selected, setSelected] = useState('');
  return <Stack><h1>Heatmap</h1><Heatmap label="Latency buckets" mode={mode} cells={Array.from({ length: 10000 }, (_, index) => ({ id: String(index), x: `Minute ${index % 50}`, y: `Bucket ${Math.floor(index / 50)}`, value: index % 20 }))} maxCells={500} onSelect={(cell) => setSelected(cell.id)} /><output aria-label="Selected cell">{selected}</output></Stack>;
}
export default { title: 'Kit/Charts' } satisfies Meta;
type Story = StoryObj;
export const PlotsAndData: Story = { render: (_, context) => <Plots mode={context.globals.theme === 'light' ? 'light' : 'dark'} /> };
export const ValuesAndControls: Story = { render: (_, context) => <Values mode={context.globals.theme === 'light' ? 'light' : 'dark'} /> };
export const HeatmapData: Story = { render: (_, context) => <Heat mode={context.globals.theme === 'light' ? 'light' : 'dark'} /> };
