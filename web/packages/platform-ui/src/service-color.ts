const palette = ['var(--chart-1)', 'var(--chart-2)', 'var(--chart-3)', 'var(--chart-4)', 'var(--chart-5)'];
/** The color of a service, shared by the audit and error feeds. */
export function serviceColor(service: string): string {
  let hash = 0;
  for (const char of service) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return palette[hash % palette.length] ?? palette[0];
}
