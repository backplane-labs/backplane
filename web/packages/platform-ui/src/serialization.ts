import { toJsonString, isMessage } from '@bufbuild/protobuf';
import { DurationSchema, TimestampSchema } from '@bufbuild/protobuf/wkt';
import { stringify } from 'lossless-json';
import type { Native, NativeStruct } from '@gopherex/schemapb';
export function nativeJSON(value: Native | NativeStruct, indent?: number): string {
  return stringify(value, (_key, item) => {
    if (item instanceof Uint8Array) return btoa(Array.from(item, (byte) => String.fromCharCode(byte)).join(''));
    if (isMessage(item, DurationSchema)) return JSON.parse(toJsonString(DurationSchema, item));
    if (isMessage(item, TimestampSchema)) return JSON.parse(toJsonString(TimestampSchema, item));
    return item;
  }, indent) ?? 'null';
}
