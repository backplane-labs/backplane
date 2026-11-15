import { isMessage, toJsonString } from '@bufbuild/protobuf';
import { DurationSchema, TimestampSchema } from '@bufbuild/protobuf/wkt';
import type { Native, NativeStruct } from '@gopherex/schemapb';
import { stringify } from 'lossless-json';
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
