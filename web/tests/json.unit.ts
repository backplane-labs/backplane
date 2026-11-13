import { describe, expect, it } from 'vitest';
import { prettyJson } from '../packages/ui/src/lib/json';

describe('prettyJson', () => {
  it.each([
    ['uint64 keeps every digit', '{"sequence":18446744073709551615}', '{\n  "sequence": 18446744073709551615\n}'],
    ['nested, empty containers, strings with structure', '{"a":[1,{"b":"x,{]:\\"y"}],"e":{},"f":[]}',
      '{\n  "a": [\n    1,\n    {\n      "b": "x,{]:\\"y"\n    }\n  ],\n  "e": {},\n  "f": []\n}'],
    ['already pretty is stable', '{\n  "a": 1\n}', '{\n  "a": 1\n}'],
    ['scalar', ' 12345678901234567890 ', '12345678901234567890'],
  ])('%s', (_, input, want) => {
    expect(prettyJson(input)).toBe(want);
  });

  it('answers undefined for invalid JSON', () => {
    expect(prettyJson('{"a":')).toBeUndefined();
    expect(prettyJson('not json')).toBeUndefined();
  });
});
