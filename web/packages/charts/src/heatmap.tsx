import { useMemo } from 'react';
import { tokens } from '@backplane-labs/backplane-theme';
import { DataTable } from '@backplane-labs/backplane-ui';
import { exact, numeric } from './model.js';
import { useChartText } from './locales.js';
import type { HeatmapProps } from './types.js';

export default function Heatmap({ label, cells, mode, height = 300, maxCells = 2000, partial, onSelect }: HeatmapProps) {
  const text = useChartText();
  const visible = useMemo(() => cells.slice(0, Math.max(1, Math.min(10000, Math.floor(maxCells) || 2000))), [cells, maxCells]);
  const x = [...new Set(visible.map((cell) => cell.x))], y = [...new Set(visible.map((cell) => cell.y))];
  const xIndex = new Map(x.map((key, index) => [key, index])), yIndex = new Map(y.map((key, index) => [key, index]));
  const maximum = Math.max(1, ...visible.map((cell) => Math.abs(numeric(cell.value) ?? 0)));
  return <section aria-label={label}>{(partial || visible.length < cells.length) && <p role="status">{text('partial')}</p>}
    {!visible.length ? <p>{text('empty')}</p> : <svg role="img" aria-label={label} viewBox={`0 0 ${Math.max(1, x.length) * 20} ${Math.max(1, y.length) * 20}`} style={{ width: '100%', height }}>
      {visible.map((cell) => <rect key={cell.id} x={xIndex.get(cell.x)! * 20} y={yIndex.get(cell.y)! * 20} width={19} height={19} fill={tokens[mode].primary} opacity={.1 + .9 * Math.abs(numeric(cell.value) ?? 0) / maximum}><title>{cell.x} · {cell.y}: {exact(cell.value)}</title></rect>)}
    </svg>}
    <DataTable label={label} data={visible} getRowId={(cell) => cell.id} height={200} onActivate={onSelect} columns={[
      { id: 'x', label: 'x', value: (cell) => cell.x }, { id: 'y', label: 'y', value: (cell) => cell.y }, { id: 'value', label: text('value'), value: (cell) => cell.value },
    ]} />
  </section>;
}
