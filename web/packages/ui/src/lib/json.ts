/**
 * prettyJson re-indents JSON text without parsing its values: numbers
 * beyond 2^53 (uint64 ids, sequences) keep every digit, which
 * JSON.stringify(JSON.parse(text)) would round. Invalid JSON answers
 * undefined.
 */
export function prettyJson(text: string, indent = 2): string | undefined {
  try { JSON.parse(text); } catch { return undefined; }
  const pad = (depth: number) => '\n' + ' '.repeat(depth * indent);
  let out = '', depth = 0, inString = false, escaped = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text.charAt(i);
    if (inString) {
      out += ch;
      if (escaped) escaped = false;
      else if (ch === '\\') escaped = true;
      else if (ch === '"') inString = false;
      continue;
    }
    if (ch === ' ' || ch === '\n' || ch === '\r' || ch === '\t') continue;
    if (ch === '"') { inString = true; out += ch; continue; }
    if (ch === '{' || ch === '[') {
      const close = ch === '{' ? '}' : ']';
      let next = i + 1;
      while (next < text.length && ' \n\r\t'.includes(text.charAt(next))) next++;
      if (text.charAt(next) === close) { out += ch + close; i = next; continue; }
      depth++; out += ch + pad(depth); continue;
    }
    if (ch === '}' || ch === ']') { depth--; out += pad(depth) + ch; continue; }
    if (ch === ',') { out += ',' + pad(depth); continue; }
    if (ch === ':') { out += ': '; continue; }
    out += ch;
  }
  return out;
}
