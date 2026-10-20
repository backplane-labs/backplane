import { describe, expect, it } from 'vitest';
import { buildQuery, metricUnit, queryUnit } from '../packages/platform-ui/src/explore-query';

describe('explore metrics queries', () => {
  it('reads quantiles of explicit and exponential histograms alike', () => {
    const query = buildQuery('metrics', 'metricsql', { metric: 'templates.render.duration_bucket', fn: 'p95', groupBy: 'service.name' }, {});
    expect(query).toBe('histogram_quantile(0.95, sum(rate({__name__="templates.render.duration_bucket"}[5m])) by (le, vmrange, service.name))');
  });
  it('takes a quantile per service unless one is chosen or another grouping is', () => {
    expect(buildQuery('metrics', 'metricsql', { metric: 'x.duration_bucket', fn: 'p95' }, {})).toContain('by (le, vmrange, service.name))');
    expect(buildQuery('metrics', 'metricsql', { metric: 'x.duration_bucket', fn: 'p95', service: 'hello' }, {})).toContain('by (le, vmrange))');
  });
  it('knows a duration by its name', () => {
    expect(metricUnit('templates.render.duration_bucket')).toBe('s'); expect(metricUnit('http_request_duration_seconds')).toBe('s');
    expect(metricUnit('queue_wait_ms')).toBe('ms'); expect(metricUnit('backplane.event.published')).toBeUndefined();
  });
  it('names the unit of a query only when its values are in it', () => {
    expect(queryUnit('histogram_quantile(0.95, sum(rate({__name__="courier.delivery.duration_bucket"}[5m])) by (le, vmrange))')).toBe('s');
    expect(queryUnit('sum(rate({__name__="courier.delivery.duration_count"}[5m]))')).toBeUndefined();
    expect(queryUnit('{__name__="backplane.event.published"}')).toBeUndefined();
  });
});
