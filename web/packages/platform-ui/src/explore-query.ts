import { ObsLanguage, ObsSignal } from '@backplane-labs/backplane-api';
import type { QueryLanguage } from '@backplane-labs/backplane-editors';

export type ExploreSignal = 'logs' | 'metrics' | 'traces';
export const signalIds: Record<ExploreSignal, ObsSignal> = { logs: ObsSignal.LOGS, metrics: ObsSignal.METRICS, traces: ObsSignal.TRACES };
export const languageNames: Partial<Record<ObsLanguage, QueryLanguage>> = { [ObsLanguage.LOGSQL]: 'logsql', [ObsLanguage.METRICSQL]: 'metricsql', [ObsLanguage.PROMQL]: 'promql', [ObsLanguage.TRACEQL]: 'traceql' };
export const languageIds: Record<string, ObsLanguage> = { logsql: ObsLanguage.LOGSQL, metricsql: ObsLanguage.METRICSQL, promql: ObsLanguage.PROMQL, traceql: ObsLanguage.TRACEQL };
export const languageLabel: Record<string, string> = { logsql: 'LogsQL', metricsql: 'MetricsQL', promql: 'PromQL', traceql: 'TraceQL', cel: 'CEL' };
export const ranges = { '5m': 5 * 60e3, '15m': 15 * 60e3, '1h': 3600e3, '3h': 3 * 3600e3, '6h': 6 * 3600e3, '12h': 12 * 3600e3, '24h': 86400e3, '7d': 7 * 86400e3 } as const;
export type ExploreRange = keyof typeof ranges;
export type MetricFunction = 'raw' | 'rate' | 'sum' | 'p50' | 'p95' | 'p99';

/** Structured filters of the query builder; the expression stays editable. */
export interface QueryBuilder {
  service?: string; level?: string; text?: string;
  metric?: string; fn?: MetricFunction; groupBy?: string;
  span?: string; errors?: boolean;
}

const quote = (value: string) => JSON.stringify(value);
export const nanosNow = () => BigInt(Date.now()) * 1_000_000n;

/** Labels a histogram's buckets carry: le for explicit buckets, vmrange for exponential ones (VictoriaMetrics); quantiles group by both. */
export const bucketLabels = ['le', 'vmrange'] as const;

/** Metric names of an expression: quoted __name__ matchers and bare selectors. */
function metricNames(expression: string): string[] {
  const quoted = [...expression.matchAll(/__name__\s*=\s*"([^"]+)"/g)].map((match) => match[1]!);
  const bare = [...expression.matchAll(/(?:^|[(\s,])([a-zA-Z_][\w.:]*)\s*(?:\{|\[)/g)].map((match) => match[1]!);
  return [...quoted, ...bare];
}

/** Unit of a metric by its name, as OpenTelemetry names durations (.duration, _seconds, _ms); undefined when unknown. */
export function metricUnit(metric: string): string | undefined {
  const base = metric.replace(/_(bucket|sum|count)$/, '');
  if (/(_ms|_milliseconds|\.ms)$/.test(base)) return 'ms';
  if (/([._]duration|[._]latency|_seconds)$/.test(base)) return 's';
  return undefined;
}

/** Unit of a query's values: a quantile or a raw duration is in the duration's unit; a rate or a count has none it can name. */
export function queryUnit(expression: string): string | undefined {
  const names = metricNames(expression);
  if (/histogram_quantile\s*\(/.test(expression)) return names.map(metricUnit).find(Boolean);
  if (/\b(rate|increase|irate|count)\s*\(/.test(expression)) return undefined;
  const units = names.map(metricUnit);
  return units.length && units.every((unit) => unit && unit === units[0]) && !names.some((name) => /_(bucket|count)$/.test(name)) ? units[0] : undefined;
}

/** Default aggregation for a metric by its OpenTelemetry export suffix. */
export function defaultFunction(metric: string): MetricFunction {
  if (metric.endsWith('_bucket')) return 'p95';
  if (/(_count|_sum|_total|\.calls|\.runs|\.published|\.messages|\.matched|\.greetings|\.attempts|\.rewrites|\.updates|\.revisions)$/.test(metric)) return 'rate';
  return 'raw';
}

/** Expression for the builder's filters in the selected language. */
export function buildQuery(signal: ExploreSignal, language: string | undefined, builder: QueryBuilder, fields: Record<string, string>): string {
  const field = (key: string) => fields[key] ?? key;
  if (signal === 'metrics') {
    const promql = language === 'promql', label = (key: string) => promql ? quote(key) : key;
    const matchers = [builder.metric ? `__name__=${quote(builder.metric)}` : '', builder.service ? `${label(field('service.name'))}=${quote(builder.service)}` : ''].filter(Boolean);
    // Without a metric there is nothing to plot: the generated dashboard is shown instead.
    if (!builder.metric) return '';
    const selector = `{${matchers.join(', ')}}`, by = builder.groupBy ? ` by (${label(builder.groupBy)})` : '';
    switch (builder.fn ?? (builder.metric ? defaultFunction(builder.metric) : 'raw')) {
      case 'rate': return `sum(rate(${selector}[5m]))${by}`;
      case 'sum': return `sum(${selector})${by}`;
      case 'p50': case 'p95': case 'p99': {
        const quantile = { p50: '0.5', p95: '0.95', p99: '0.99' }[builder.fn === 'p50' || builder.fn === 'p99' ? builder.fn : 'p95'];
        // Histograms of different buckets (explicit and exponential, or other bounds) do not add up: without a grouping a
        // quantile is per service, which is what an operator reads anyway.
        const group = builder.groupBy ?? (builder.service ? undefined : field('service.name'));
        return `histogram_quantile(${quantile}, sum(rate(${selector}[5m])) by (${bucketLabels.map(label).join(', ')}${group ? `, ${label(group)}` : ''}))`;
      }
      default: return selector;
    }
  }
  if (language === 'traceql') {
    const clauses = [builder.service ? `resource.service.name = ${quote(builder.service)}` : '', builder.span ? `name = ${quote(builder.span)}` : '', builder.errors ? 'status = error' : ''].filter(Boolean);
    return `{ ${clauses.join(' && ')} }`.replace('{  }', '{}');
  }
  const clauses = signal === 'traces'
    ? [builder.service ? `${field('service.name')}:=${quote(builder.service)}` : '', builder.span ? `name:=${quote(builder.span)}` : '', builder.errors ? 'status_code:=2' : '']
    : [builder.service ? `${field('service.name')}:=${quote(builder.service)}` : '', builder.level ? `severity_text:=${quote(builder.level)}` : '', builder.text ? quote(builder.text) : ''];
  return clauses.filter(Boolean).join(' ') || '*';
}

/** Step of a range query so a window renders about `points` buckets. */
export function stepFor(range: ExploreRange, points: number): bigint {
  return BigInt(Math.max(15, Math.round(ranges[range] / 1000 / points))) * 1_000_000_000n;
}
