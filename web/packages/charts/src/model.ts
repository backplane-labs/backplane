import type { ChartPoint, ChartSeries, NumericValue } from './types.js';
export function numeric(value: NumericValue): number | null {
  if (value === null || value === '') return null;
  const result = Number(value); return Number.isFinite(result) ? result : null;
}
export function exact(value: NumericValue): string { return value === null ? '—' : String(value); }

const precise = (value: number) => String(Number(value.toPrecision(3)));
/** Seconds as the closest unit: 850 µs, 120 ms, 1.5 s, 2.3 min, 1.2 h. */
function duration(seconds: number): string {
  const size = Math.abs(seconds);
  if (size === 0) return '0 s';
  if (size < 1e-3) return `${precise(seconds * 1e6)} µs`;
  if (size < 1) return `${precise(seconds * 1e3)} ms`;
  if (size < 60) return `${precise(seconds)} s`;
  if (size < 3600) return `${precise(seconds / 60)} min`;
  return `${precise(seconds / 3600)} h`;
}
function bytes(value: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']; let size = value, at = 0;
  while (Math.abs(size) >= 1024 && at < units.length - 1) { size /= 1024; at++; }
  return `${precise(size)} ${units[at]}`;
}
/** A value as people read it in its unit: durations (s, ms) and bytes (By) humanized, other units appended. */
export function formatValue(value: NumericValue, unit?: string): string {
  const number = numeric(value);
  if (number === null) return '—';
  switch (unit) {
    case 's': return duration(number);
    case 'ms': return duration(number / 1e3);
    case 'us': return duration(number / 1e6);
    case 'By': return bytes(number);
    case undefined: case '': case '1': return precise(number);
    default: return `${precise(number)} ${unit}`;
  }
}
/** Keep both extrema per bucket, first/last and null gaps, with a strict output bound. */
export function samplePoints(points: readonly ChartPoint[], limit: number): ChartPoint[] {
  const budget = Math.max(5, Math.floor(limit));
  if (points.length <= budget) return [...points];
  const buckets = Math.max(1, Math.floor((budget - 2) / 3)); const chosen = new Set([0, points.length - 1]);
  for (let bucket = 0; bucket < buckets; bucket++) {
    const from = 1 + Math.floor(bucket * (points.length - 2) / buckets), to = 1 + Math.floor((bucket + 1) * (points.length - 2) / buckets);
    let min = from, max = from, gap = -1;
    for (let index = from; index < to; index++) {
      const value = numeric(points[index].value);
      if (value === null) { gap = index; continue; }
      if (numeric(points[min].value) === null || value < numeric(points[min].value)!) min = index;
      if (numeric(points[max].value) === null || value > numeric(points[max].value)!) max = index;
    }
    chosen.add(min); chosen.add(max); if (gap >= 0) chosen.add(gap);
  }
  return [...chosen].sort((a, b) => a - b).slice(0, budget).map((index) => points[index]);
}
export function prepareSeries(input: readonly ChartSeries[], maxPoints = 2000) {
  const limited = input.slice(0, 64), budget = Math.max(512, Math.min(20000, Math.floor(maxPoints) || 2000));
  let invalid = false, approximate = false, sampled = input.length > limited.length;
  const series = limited.map((series) => {
    const points = series.points.filter((point) => { if (!Number.isFinite(point.x)) { invalid = true; return false; } return true; }).sort((a, b) => a.x - b.x);
    for (const point of points) {
      const value = numeric(point.value);
      if (point.value !== null && value === null) invalid = true;
      if (value !== null && (typeof point.value === 'bigint' ? BigInt(Math.trunc(value)) !== point.value : typeof point.value === 'string' && /^[+-]?\d+$/.test(point.value) ? BigInt(Math.trunc(value)) !== BigInt(point.value) : false)) approximate = true;
    }
    const selected = samplePoints(points, Math.max(4, Math.floor(budget / Math.max(1, limited.length))));
    sampled ||= selected.length < points.length;
    return { ...series, points: selected };
  });
  const x = [...new Set(series.flatMap((series) => series.points.map((point) => point.x)))].sort((a, b) => a - b);
  const values = series.map((series) => new Map(series.points.map((point) => [point.x, point.value])));
  return { series, x, values, data: [x, ...values.map((values) => x.map((x) => values.has(x) ? numeric(values.get(x)!) : undefined))], invalid, approximate, sampled };
}
