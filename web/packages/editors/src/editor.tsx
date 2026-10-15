import { useEffect, useRef, useState } from 'react';
import { Compartment, EditorState, RangeSetBuilder, Transaction } from '@codemirror/state';
import { Decoration, EditorView, ViewPlugin, keymap, lineNumbers, highlightActiveLine, drawSelection, hoverTooltip, type DecorationSet, type ViewUpdate } from '@codemirror/view';
import { tags } from '@lezer/highlight';
import { history, historyKeymap, defaultKeymap, indentWithTab } from '@codemirror/commands';
import { syntaxHighlighting, HighlightStyle, bracketMatching, foldGutter, foldKeymap, indentOnInput } from '@codemirror/language';
import { closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete';
import { autocompletion, completionKeymap, type CompletionContext } from '@codemirror/autocomplete';
import { linter, lintGutter, setDiagnostics, type Diagnostic } from '@codemirror/lint';
import { jsonParseLinter } from '@codemirror/lang-json';
import { tokens, typography } from '@backplane-labs/backplane-theme';
import { boundDiagnostics } from './model.js';
import { languageExtension, queryKeywords } from './language.js';
import { useEditorText } from './locales.js';
import type { CodeEditorProps } from './types.js';

export function editorHighlight(mode: 'dark' | 'light') {
  const c = tokens[mode];
  // Keys, strings, numbers and literals each get their own color: JSON and
  // YAML read at a glance; punctuation and comments step back.
  return syntaxHighlighting(HighlightStyle.define([
    { tag: [tags.propertyName, tags.definition(tags.propertyName), tags.attributeName], color: c['chart-2'] },
    { tag: [tags.string, tags.special(tags.string), tags.content, tags.attributeValue], color: c['chart-1'] },
    { tag: [tags.tagName], color: c['chart-4'] },
    { tag: [tags.number, tags.integer, tags.float], color: c['chart-3'] },
    { tag: [tags.bool, tags.null, tags.atom, tags.keyword], color: c['chart-5'] },
    { tag: [tags.comment, tags.lineComment, tags.blockComment], color: c['muted-foreground'], fontStyle: 'italic' },
    { tag: [tags.punctuation, tags.separator, tags.bracket, tags.squareBracket, tags.brace, tags.angleBracket, tags.meta], color: c['muted-foreground'] },
    { tag: [tags.variableName, tags.operator, tags.typeName], color: c.foreground },
    { tag: tags.invalid, color: c.destructive, textDecoration: 'underline wavy' },
  ]));
}
/** maxGrowth is how tall a growing editor gets before it scrolls. */
const maxGrowth = 480;

/**
 * editorTheme styles an editor. 'fill' takes the container's height; a number
 * is the least height, and the editor grows with its text up to maxGrowth
 * (or the number, if larger). A fixed editor keeps the number; a fitting one
 * (a viewer) has no least height and is as tall as its text.
 */
export function editorTheme(mode: 'dark' | 'light', height: number | 'fill' = 160, size: 'grow' | 'fixed' | 'fit' = 'grow') {
  const c = tokens[mode];
  const box: Record<string, string> = height === 'fill' ? { height: '100%' } : size === 'fixed' ? { height: `${height}px` }
    : { ...(size === 'grow' && { minHeight: `${height}px` }), maxHeight: `${Math.max(height, maxGrowth)}px` };
  return EditorView.theme({
    '&': { backgroundColor: c.background, color: c.foreground, ...box, fontFamily: typography.fontFamilyMonospace,
      fontSize: '13px', border: `1px solid ${c.border}`, borderRadius: '6px', overflow: 'hidden' },
    '.cm-scroller': { overflow: 'auto', fontFamily: 'inherit', lineHeight: '1.6', flexGrow: '1' }, '.cm-content': { padding: '8px 4px', caretColor: c.foreground },
    '.cm-gutters': { backgroundColor: c.background, color: c['muted-foreground'], border: 'none', borderRight: `1px solid ${c.border}` },
    '.cm-lineNumbers .cm-gutterElement': { padding: '0 8px 0 12px', minWidth: '32px' },
    '.cm-activeLineGutter': { backgroundColor: c.muted, color: c.foreground },
    '.cm-foldGutter .cm-gutterElement': { padding: '0 4px', cursor: 'pointer' },
    '.cm-matchingBracket': { backgroundColor: c.accent, outline: `1px solid ${c.border}` },
    '&.cm-focused': { outline: `2px solid ${c.ring}`, outlineOffset: '2px' },
    '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': { backgroundColor: c.accent },
    '.cm-activeLine': { backgroundColor: c.muted }, '.cm-cursor': { borderLeftColor: c.foreground },
    '.cm-tooltip': { backgroundColor: c.popover, color: c.foreground, border: `1px solid ${c.border}`, zIndex: '60' },
    '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: c.accent, color: c.foreground },
    '.cm-tooltip-hover': { padding: '4px 8px', fontSize: '12px' },
    '.cm-completionDetail': { color: c['muted-foreground'], fontStyle: 'normal', marginLeft: '8px' },
  }, { dark: mode === 'dark' });
}

/** shown is the text a viewer displays: read-only JSON without diagnostics reads pretty-printed. */
function shown(props: CodeEditorProps): string {
  if (!props.readOnly || props.language !== 'json' || props.diagnostics?.length) return props.value;
  try { return JSON.stringify(JSON.parse(props.value), null, 2); } catch { return props.value; }
}

/** formatDocument pretty-prints JSON in place (two spaces); false for other languages or invalid JSON. */
export function formatDocument(view: EditorView, language?: string): boolean {
  if (language !== 'json' || view.state.readOnly) return false;
  const text = view.state.doc.toString();
  let pretty: string;
  try { pretty = JSON.stringify(JSON.parse(text), null, 2); } catch { return false; }
  if (pretty !== text) view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: pretty } });
  return true;
}

/** hangingIndent continues a wrapped line two columns under its own indentation instead of at the margin. */
const hangingIndent = ViewPlugin.fromClass(class {
  decorations: DecorationSet;
  constructor(view: EditorView) { this.decorations = this.build(view); }
  update(update: ViewUpdate) { if (update.docChanged || update.viewportChanged) this.decorations = this.build(update.view); }
  build(view: EditorView): DecorationSet {
    const builder = new RangeSetBuilder<Decoration>();
    let last = -1;
    for (const { from, to } of view.visibleRanges) for (let pos = from; pos <= to;) {
      const line = view.state.doc.lineAt(pos);
      if (line.from > last) {
        const columns = (/^[ \t]*/.exec(line.text)?.[0].length ?? 0) + 2;
        builder.add(line.from, line.from, Decoration.line({ attributes: { style: `padding-left: calc(${columns}ch + 6px); text-indent: -${columns}ch` } }));
        last = line.from;
      }
      pos = line.to + 1;
    }
    return builder.finish();
  }
}, { decorations: (plugin) => plugin.decorations });

/** viewSettings is what follows the props: language, theme, read-only state and gutters. */
function viewSettings(props: CodeEditorProps) {
  return [
    languageExtension(props.language ?? 'text'), editorHighlight(props.mode), editorTheme(props.mode, props.inline ? 38 : props.height, props.inline ? 'fixed' : props.readOnly ? 'fit' : 'grow'),
    EditorState.readOnly.of(!!props.readOnly || !!props.disabled), EditorView.editable.of(!props.disabled),
    EditorView.contentAttributes.of({ 'aria-label': props.label, 'aria-readonly': String(!!props.readOnly), 'aria-disabled': String(!!props.disabled), tabindex: props.disabled ? '-1' : '0' }),
    // Long values wrap: a narrow panel shows all of a line instead of cutting it.
    props.inline ? [] : [lineNumbers(), foldGutter(), highlightActiveLine(), lintGutter(), EditorView.lineWrapping, hangingIndent],
  ];
}

export default function Editor(props: CodeEditorProps) {
  const host = useRef<HTMLDivElement>(null), viewRef = useRef<EditorView | null>(null), latest = useRef(props); latest.current = props;
  const settings = useRef(new Compartment()), externalDiagnostics = useRef(new Compartment());
  const [failure, setFailure] = useState<'completionFailed' | 'validationFailed' | null>(null);
  const [messages, setMessages] = useState<readonly Diagnostic[]>([]);
  const text = useEditorText();
  useEffect(() => {
    if (!host.current) return;
    const requests = new Set<AbortController>(); let disposed = false;
    const complete = async (context: CompletionContext) => {
      const language = latest.current.language ?? 'text';
      const word = context.matchBefore(/[\w.$:]+/); if (!context.explicit && !word && !(language === 'yaml' && latest.current.complete)) return null;
      const controller = new AbortController(); requests.add(controller);
      context.addEventListener('abort', () => controller.abort(), { onDocChange: true });
      try {
        const answer = latest.current.complete
          ? await latest.current.complete({ value: context.state.doc.toString(), position: context.pos, language, signal: controller.signal, explicit: context.explicit })
          : language === 'json' || language === 'yaml' || language === 'html' || language === 'text' ? [] : queryKeywords[language].map((label) => ({ label, type: 'keyword' as const }));
        if (disposed || controller.signal.aborted || context.aborted || !answer) return null;
        const result = 'from' in answer ? answer : { from: word?.from ?? context.pos, options: answer };
        setFailure(null); return { from: result.from, options: [...result.options].slice(0, 500), validFor: /^[\w]*$/ };
      } catch { if (!disposed && !controller.signal.aborted) setFailure('completionFailed'); return null; }
      finally { requests.delete(controller); }
    };
    let validation: AbortController | undefined;
    const validate = async (view: EditorView): Promise<readonly Diagnostic[]> => {
      validation?.abort(); const controller = new AbortController(); validation = controller; requests.add(controller);
      const doc = view.state.doc.toString();
      try {
        const local = latest.current.language === 'json' ? jsonParseLinter()(view) : [];
        // Without a validator the host's diagnostics are the lint result, so a lint pass never clears them.
        const remote = latest.current.validate ? await latest.current.validate(doc, controller.signal) : latest.current.diagnostics ?? [];
        if (disposed || controller.signal.aborted || view.state.doc.toString() !== doc) return [];
        const result = [...await local, ...boundDiagnostics(remote, doc.length)];
        setMessages(result); latest.current.onDiagnostics?.(result); setFailure(null); return result;
      } catch { if (!disposed && !controller.signal.aborted) setFailure('validationFailed'); return []; }
      finally { requests.delete(controller); }
    };
    const view = new EditorView({ parent: host.current, state: EditorState.create({ doc: shown(latest.current), extensions: [
      history(), drawSelection(), bracketMatching(), closeBrackets(), indentOnInput(),
      settings.current.of(viewSettings(latest.current)), externalDiagnostics.current.of([]),
      keymap.of([{ key: 'Mod-Enter', run: () => { if (!latest.current.disabled) latest.current.onSubmit?.(); return true; } },
        { key: 'Shift-Alt-f', run: (view) => formatDocument(view, latest.current.language) },
        ...closeBracketsKeymap, ...completionKeymap, ...foldKeymap, ...defaultKeymap, ...historyKeymap, indentWithTab]),
      autocompletion({ override: [complete] }), linter(validate, { delay: 300 }),
      hoverTooltip((view, pos) => {
        const content = latest.current.hover?.(pos, view.state.doc.toString()); if (!content) return null;
        return { pos, create: () => { const dom = document.createElement('div'); dom.textContent = content; return { dom }; } };
      }),
      EditorState.changeFilter.of((transaction) => !latest.current.inline || !transaction.docChanged || !transaction.newDoc.toString().includes('\n')),
      EditorView.updateListener.of((update) => {
        if (update.docChanged) {
          validation?.abort();
          if (!update.transactions.some((transaction) => transaction.annotation(Transaction.remote))) latest.current.onChange?.(update.state.doc.toString());
        }
        if (update.selectionSet || update.docChanged) latest.current.onCursor?.(update.state.selection.main.head);
      }),
    ] }) });
    viewRef.current = view;
    return () => { disposed = true; requests.forEach((controller) => controller.abort()); view.destroy(); viewRef.current = null; };
  }, []);
  useEffect(() => {
    const view = viewRef.current; if (!view) return;
    view.dispatch({ effects: settings.current.reconfigure(viewSettings(props)) });
  }, [props.language, props.mode, props.inline, props.height, props.readOnly, props.disabled, props.label]);
  useEffect(() => {
    const view = viewRef.current, text = shown(props); if (view && view.state.doc.toString() !== text) view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text }, annotations: Transaction.remote.of(true) });
  }, [props.value, props.readOnly, props.language]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { const view = viewRef.current; if (view && props.diagnostics) view.dispatch(setDiagnostics(view.state, boundDiagnostics(props.diagnostics, view.state.doc.length))); }, [props.diagnostics, props.value]);
  useEffect(() => {
    const view = viewRef.current, reveal = props.reveal; if (!view || !reveal) return;
    const length = view.state.doc.length, from = Math.max(0, Math.min(length, reveal.from)), to = Math.max(from, Math.min(length, reveal.to));
    view.dispatch({ selection: { anchor: from, head: to }, effects: EditorView.scrollIntoView(from, { y: 'center' }) }); view.focus();
  }, [props.reveal?.key]); // eslint-disable-line react-hooks/exhaustive-deps -- a reveal is keyed, not compared.
  const fill = props.height === 'fill';
  return <div style={fill ? { display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 } : undefined}>
    <div style={{ position: 'relative', ...(fill ? { flex: '1 1 auto', minHeight: 0 } : {}) }}>
      <div ref={host} style={fill ? { height: '100%' } : undefined} />
      {props.language === 'json' && !props.readOnly && !props.disabled && !props.inline && <button type="button" title={text('formatHint')}
        onClick={() => { const view = viewRef.current; if (view) formatDocument(view, 'json'); }}
        style={{ position: 'absolute', top: 6, right: 8, zIndex: 2, fontSize: 11, padding: '2px 8px', borderRadius: 4, cursor: 'pointer',
          border: '1px solid var(--border)', background: 'var(--background)', color: 'var(--muted-foreground)' }}>{text('format')}</button>}
    </div>
    {!props.hideMessages && <small style={{ color: 'var(--muted-foreground)' }}>{text('escapeTab')}</small>}
    {failure && <p role="status">{text(failure)}</p>}
    {!props.hideMessages && !!messages.length && <ul aria-label={text('diagnostics')}>{messages.map((message, index) => <li key={index}>{message.message}</li>)}</ul>}
  </div>;
}
