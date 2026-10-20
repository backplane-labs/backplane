import { describe, expect, it } from 'vitest';
import { numeric, prepareSeries, samplePoints } from '../packages/charts/src/model';
describe('plot bounds and precision', () => {
  it('keeps spikes, endpoints and original integer values within budget', () => {
    const points = Array.from({ length: 20000 }, (_, x) => ({ x, value: x === 555 ? 999999n : 10n }));
    const sample = samplePoints(points, 100);
    expect(sample.length).toBeLessThanOrEqual(100); expect(sample[0]).toBe(points[0]); expect(sample.at(-1)).toBe(points.at(-1)); expect(sample).toContain(points[555]);
  });
  it('bounds the union of timestamps and flags precision loss only in coordinates', () => {
    const series = Array.from({ length: 100 }, (_, id) => ({ id: String(id), label: String(id), points: Array.from({ length: 1000 }, (_, x) => ({ x: x * 100 + id, value: 18446744073709551615n })) }));
    const result = prepareSeries(series, 1000);
    expect(result.series.length).toBe(64); expect(result.x.length).toBeLessThanOrEqual(1000); expect(result.approximate).toBe(true); expect(result.sampled).toBe(true);
    expect(result.values[0].values().next().value).toBe(18446744073709551615n);
  });
  it('rejects non-finite coordinates and keeps gaps', () => {
    expect(numeric('NaN')).toBeNull(); expect(numeric(null)).toBeNull();
    const result = prepareSeries([{ id: 'a', label: 'A', points: [{ x: NaN, value: 1 }, { x: 1, value: null }, { x: 2, value: 'Inf' }] }]);
    expect(result.invalid).toBe(true); expect(result.data).toEqual([[1, 2], [null, null]]);
  });
});
describe('values in their unit', () => {
  it('humanizes durations and bytes, appends other units', async () => {
    const { formatValue } = await import('../packages/charts/src/model');
    expect(formatValue(0.1201, 's')).toBe('120 ms'); expect(formatValue(0.00085, 's')).toBe('850 µs'); expect(formatValue(1.5, 's')).toBe('1.5 s');
    expect(formatValue(138, 's')).toBe('2.3 min'); expect(formatValue(250, 'ms')).toBe('250 ms'); expect(formatValue(0, 's')).toBe('0 s');
    expect(formatValue(12_582_912, 'By')).toBe('12 MiB'); expect(formatValue(3.14159, 'req')).toBe('3.14 req'); expect(formatValue(null, 's')).toBe('—');
  });
});
