import { timestampDate, type Duration, type Timestamp } from '@bufbuild/protobuf/wkt';
import { formatDuration, type StatusTone } from '@backplane-labs/backplane-ui';

/** "WS_PROTO" → "ws-proto"; numeric enums keep their reverse mapping. */
export function enumLabel(values: Record<number, string>, value: number): string {
  const name = values[value];
  return name && name !== 'UNSPECIFIED' ? name.toLowerCase().replaceAll('_', '-') : '—';
}
export function date(value?: Timestamp): Date | undefined { return value ? timestampDate(value) : undefined; }
export function durationText(value?: Duration): string | undefined {
  return value ? formatDuration(Number(value.seconds) * 1000 + value.nanos / 1e6) : undefined;
}
export function jsonText(bytes: Uint8Array): string {
  const text = new TextDecoder().decode(bytes);
  try { return JSON.stringify(JSON.parse(text), null, 2); } catch { return text; }
}
export const phaseTones: Record<string, StatusTone> = { starting: 'info', serving: 'success', stopping: 'warning' };
