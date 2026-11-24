import { prettyJson } from '@backplane-labs/backplane-ui';
import { type Timestamp, timestampDate } from '@bufbuild/protobuf/wkt';

/** "WS_PROTO" → "ws-proto"; numeric enums keep their reverse mapping. */
export function enumLabel(values: Record<number, string>, value: number): string {
  const name = values[value];
  return name && name !== 'UNSPECIFIED' ? name.toLowerCase().replaceAll('_', '-') : '—';
}
export function date(value?: Timestamp): Date | undefined {
  return value ? timestampDate(value) : undefined;
}
export function jsonText(bytes: Uint8Array): string {
  const text = new TextDecoder().decode(bytes);
  return prettyJson(text) ?? text;
}
