import { isMessage, toJsonString } from '@bufbuild/protobuf';
import { DurationSchema, TimestampSchema } from '@bufbuild/protobuf/wkt';
import type { Native, NativeStruct } from '@gopherex/schemapb';
import { isLosslessNumber, type parse, stringify } from 'lossless-json';
export function nativeJSON(value: Native | NativeStruct, indent?: number): string {
  return (
    stringify(
      value,
      (_key, item) => {
        if (item instanceof Uint8Array) return btoa(Array.from(item, (byte) => String.fromCharCode(byte)).join(''));
        if (isMessage(item, DurationSchema)) return JSON.parse(toJsonString(DurationSchema, item));
        if (isMessage(item, TimestampSchema)) return JSON.parse(toJsonString(TimestampSchema, item));
        return item;
      },
      indent,
    ) ?? 'null'
  );
}

/** The unvalidated result of lossless-json `parse`, read once at this boundary. */
export type Parsed = ReturnType<typeof parse>;

/** Converts a parsed JSON tree to native schema values; JSON numbers keep their digits (lossless). */
export function toNativeJSON(value: Parsed): Native {
  if (value === null) return null;
  if (isLosslessNumber(value)) {
    const text = value.toString();
    return /^-?\d+$/.test(text) ? BigInt(text) : Number(text);
  }
  if (Array.isArray(value)) return value.map(toNativeJSON);
  if (typeof value === 'object')
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, toNativeJSON(item)]));
  if (typeof value === 'string' || typeof value === 'boolean' || typeof value === 'bigint' || typeof value === 'number')
    return value;
  return null;
}
