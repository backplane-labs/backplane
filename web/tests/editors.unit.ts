import { StringStream } from '@codemirror/language';
import { describe, expect, it } from 'vitest';
import { queryParser } from '../packages/editors/src/language';
import { boundDiagnostics, displayJSON } from '../packages/editors/src/model';

describe('editor data boundaries', () => {
  it('preserves uint64 and handles cyclic display values', () => {
    const object: Record<string, unknown> = { sequence: 18446744073709551615n };
    object.self = object;
    expect(displayJSON(object)).toContain('18446744073709551615');
    expect(displayJSON(object)).toContain('[Circular');
  });
  it('bounds diagnostic offsets including malformed API results', () => {
    expect(
      boundDiagnostics(
        [
          { from: -10, to: 999, severity: 'error', message: 'bad' },
          { from: NaN, to: -1, severity: 'info', message: 'bad' },
        ],
        12,
      ),
    ).toEqual([
      { from: 0, to: 12, severity: 'error', message: 'bad' },
      { from: 0, to: 0, severity: 'info', message: 'bad' },
    ]);
  });
  it('tokenizes supported query families without treating string contents as keywords', () => {
    for (const language of ['cel', 'promql', 'metricsql', 'logsql', 'traceql'] as const) {
      const parser = queryParser(language),
        state = { quote: null as string | null },
        stream = new StringStream('"name \\"test\\"" 123ms', 4, 2);
      expect(parser.token(stream, state)).toBe('string');
      expect(state.quote).toBe(null);
      stream.start = stream.pos;
      expect(parser.token(stream, state)).toBe(null);
      stream.start = stream.pos;
      expect(parser.token(stream, state)).toBe('number');
    }
  });
});
