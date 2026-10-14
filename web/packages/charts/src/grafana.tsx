import { useId, useMemo } from 'react';
import { BarGauge, BigValue, BigValueColorMode, BigValueGraphMode, ThemeContext, UnitPicker, StatsPicker, ColorPickerInput } from '@grafana/ui';
import { VizOrientation, ThresholdsMode } from '@grafana/data';
import { BarGaugeDisplayMode } from '@grafana/schema';
import { createGrafanaTheme, tokens } from '@backplane-labs/backplane-theme';
import { Button, Input } from '@backplane-labs/backplane-ui';
import { exact, numeric } from './model.js';
import { useChartText } from './locales.js';
import type { ValueProps, VisualizationControlsProps } from './types.js';

export function ValueVisualization({ label, value, mode, unit, min = 0, max = 100, thresholds = [], color, height = 180, kind }: ValueProps & { kind: 'stat' | 'bar' | 'radial' }) {
  const theme = useMemo(() => createGrafanaTheme(mode), [mode]);
  const number = numeric(value), ordered = [...thresholds].filter((threshold) => Number.isFinite(threshold.value)).sort((a, b) => a.value - b.value);
  const selected = number === null ? undefined : ordered.filter((threshold) => number >= threshold.value).at(-1);
  const display = { numeric: number ?? NaN, text: exact(value), suffix: unit ? ` ${unit}` : undefined, title: label, color: selected?.color ?? color ?? tokens[mode].primary };
  const field = { min, max, thresholds: { mode: ThresholdsMode.Absolute, steps: [{ value: -Infinity, color: color ?? tokens[mode].primary }, ...ordered] } };
  return <ThemeContext.Provider value={theme}><section aria-label={label} style={{ maxWidth: 400 }}>
    <div aria-hidden="true">{kind === 'stat' ? <BigValue width={360} height={height} value={display} theme={theme} colorMode={BigValueColorMode.None} graphMode={BigValueGraphMode.None} />
      : kind === 'bar' ? <BarGauge width={360} height={height} value={display} field={field} theme={theme} orientation={VizOrientation.Horizontal} displayMode={BarGaugeDisplayMode.Basic} />
        : <svg viewBox="0 0 200 130" style={{ width: '100%', height }}>
          <path d="M 30 100 A 70 70 0 0 1 170 100" fill="none" stroke={tokens[mode].border} strokeWidth={14} pathLength={100} />
          <path d="M 30 100 A 70 70 0 0 1 170 100" fill="none" stroke={display.color} strokeWidth={14} pathLength={100} strokeDasharray={`${number === null || max <= min ? 0 : Math.max(0, Math.min(100, (number - min) / (max - min) * 100))} 100`} />
          <text x={100} y={90} textAnchor="middle" fill={tokens[mode].foreground} fontSize={20}>{exact(value)}</text>
          <text x={25} y={125} textAnchor="middle" fill={tokens[mode]['muted-foreground']} fontSize={12}>{min}</text><text x={175} y={125} textAnchor="middle" fill={tokens[mode]['muted-foreground']} fontSize={12}>{max}</text>
        </svg>}</div>
    <output aria-label={label}>{label}: {exact(value)}{unit ? ` ${unit}` : ''}</output>
  </section></ThemeContext.Provider>;
}
export function Controls({ value, onChange, mode }: VisualizationControlsProps) {
  const theme = useMemo(() => createGrafanaTheme(mode), [mode]); const text = useChartText(), id = useId();
  return <ThemeContext.Provider value={theme}><div style={{ display: 'grid', gap: 12 }}>
    <label htmlFor={`${id}-unit`}>{text('unit')}</label><UnitPicker id={`${id}-unit`} value={value.unit} onChange={(unit) => onChange({ ...value, unit: unit ?? 'none' })} />
    <label htmlFor={`${id}-stats`}>{text('stats')}</label><StatsPicker id={`${id}-stats`} stats={value.stats} allowMultiple onChange={(stats) => onChange({ ...value, stats })} />
    <label htmlFor={`${id}-color`}>{text('color')}</label><ColorPickerInput id={`${id}-color`} value={value.color} onChange={(color) => onChange({ ...value, color })} />
    <fieldset><legend>{text('thresholds')}</legend>{value.thresholds.map((threshold, index) => <div key={index} style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
      <Input type="number" aria-label={`${text('thresholdValue')} ${index + 1}`} value={threshold.value} onChange={(event) => { if (event.target.value !== '' && Number.isFinite(event.target.valueAsNumber)) onChange({ ...value, thresholds: value.thresholds.map((item, at) => at === index ? { ...item, value: event.target.valueAsNumber } : item) }); }} />
      <ColorPickerInput aria-label={`${text('thresholdColor')} ${index + 1}`} value={threshold.color} onChange={(color) => onChange({ ...value, thresholds: value.thresholds.map((item, at) => at === index ? { ...item, color } : item) })} />
      <Button type="button" variant="outline" onClick={() => onChange({ ...value, thresholds: value.thresholds.filter((_, at) => at !== index) })}>{text('removeThreshold')}</Button>
    </div>)}<Button type="button" variant="outline" onClick={() => onChange({ ...value, thresholds: [...value.thresholds, { value: 80, color: tokens[mode].destructive }] })}>{text('addThreshold')}</Button></fieldset>
  </div></ThemeContext.Provider>;
}
