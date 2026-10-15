import { StreamLanguage, type StreamParser } from '@codemirror/language';
import { html } from '@codemirror/lang-html';
import { json } from '@codemirror/lang-json';
import { yaml } from '@codemirror/lang-yaml';
import type { EditorLanguage, QueryLanguage } from './types.js';

export const queryKeywords: Record<QueryLanguage, readonly string[]> = {
  cel: ['true', 'false', 'null', 'in', 'has', 'all', 'exists', 'exists_one', 'map', 'filter', 'size', 'string', 'int', 'uint', 'double', 'timestamp', 'duration'],
  promql: ['sum', 'avg', 'min', 'max', 'count', 'by', 'without', 'on', 'ignoring', 'group_left', 'group_right', 'offset', 'bool', 'and', 'or', 'unless', 'rate', 'increase', 'histogram_quantile'],
  metricsql: ['sum', 'avg', 'min', 'max', 'count', 'by', 'without', 'on', 'ignoring', 'offset', 'and', 'or', 'unless', 'rate', 'increase', 'WITH', 'default', 'if', 'ifnot', 'rollup', 'histogram_quantile'],
  logsql: ['AND', 'OR', 'NOT', '_time', '_stream', '_msg', 'stats', 'count', 'sum', 'avg', 'min', 'max', 'by', 'sort', 'limit', 'fields', 'filter', 'format', 'extract', 'unpack_json'],
  traceql: ['true', 'false', 'nil', 'duration', 'name', 'status', 'kind', 'rootName', 'rootServiceName', 'traceDuration', 'span', 'resource', 'event', 'link', 'count', 'avg', 'min', 'max', 'sum', 'by'],
};

/** Lexical highlighting only. Storage/CEL engines remain authoritative parsers. */
export function queryParser(language: QueryLanguage): StreamParser<{ quote: string | null }> {
  const words = new Set(queryKeywords[language]);
  return { startState: () => ({ quote: null }), token(stream, state) {
    if (stream.eatSpace()) return null;
    if (!state.quote && (stream.match('//') || stream.match('#'))) { stream.skipToEnd(); return 'comment'; }
    if (!state.quote && stream.match(/["'`]/)) state.quote = stream.current();
    if (state.quote) {
      let escaped = false, next: string | void;
      while ((next = stream.next()) !== undefined) { if (next === state.quote && !escaped) { state.quote = null; break; } escaped = next === '\\' && !escaped; }
      return 'string';
    }
    if (stream.match(/(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?(?:ns|us|µs|ms|s|m|h|d|w|y)?/)) return 'number';
    if (stream.match(/[A-Za-z_$][\w.$:]*/)) return words.has(stream.current()) ? 'keyword' : 'variableName';
    if (stream.match(/[+*/%=!~<>|&^-]+/)) return 'operator';
    stream.next(); return null;
  } };
}
export function languageExtension(language: EditorLanguage) { return language === 'json' ? json() : language === 'yaml' ? yaml() : language === 'html' ? html() : language === 'text' ? [] : StreamLanguage.define(queryParser(language)); }
