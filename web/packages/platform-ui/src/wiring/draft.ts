import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { create, equals, toJsonString } from '@bufbuild/protobuf';
import * as api from '@backplane-labs/backplane-api';
import { useClient, useConnection } from '@backplane-labs/backplane-react';
import { definitionSchema, locate, parseDraft, type Definition, type Draft, type DraftProblem, type WiringKind } from './document.js';

/**
 * The text of a draft with an undo history of its own: YAML typing coalesces
 * into one step per pause, a graph edit is one step. CodeMirror keeps its own
 * history while typing; this one spans both views.
 */
export function useDraftText(initial: string) {
  const [state, setState] = useState({ text: initial, past: [] as string[], future: [] as string[] });
  const typing = useRef<{ since: number } | null>(null);
  const set = useCallback((text: string, coalesce: boolean) => setState((old) => {
    if (old.text === text) return old;
    const now = Date.now(), merge = coalesce && typing.current && now - typing.current.since < 800;
    typing.current = coalesce ? { since: now } : null;
    return { text, past: merge ? old.past : [...old.past.slice(-99), old.text], future: [] };
  }), []);
  return {
    text: state.text, canUndo: state.past.length > 0, canRedo: state.future.length > 0,
    /** A change typed in the YAML editor. */
    type: useCallback((text: string) => set(text, true), [set]),
    /** A change made as a whole (a graph edit, a rename, a reset). */
    apply: useCallback((text: string) => set(text, false), [set]),
    undo: useCallback(() => setState((old) => old.past.length ? { text: old.past.at(-1)!, past: old.past.slice(0, -1), future: [old.text, ...old.future] } : old), []),
    redo: useCallback(() => setState((old) => old.future.length ? { text: old.future[0]!, past: [...old.past, old.text], future: old.future.slice(1) } : old), []),
  };
}

export function useParsedDraft(kind: WiringKind, text: string): Draft {
  return useMemo(() => parseDraft(kind, text), [kind, text]);
}

/** JSON of a definition: the key of its analysis. Map order follows the message, so compare with sameDefinition. */
export function definitionKey(kind: WiringKind, definition?: Definition): string {
  return definition ? toJsonString(definitionSchema(kind), definition) : '';
}

/** Whether two definitions say the same (maps compared by key, not order). */
export function sameDefinition(kind: WiringKind, a?: Definition, b?: Definition): boolean {
  if (!a || !b) return a === b;
  return equals(definitionSchema(kind), a as never, b as never);
}

/**
 * The server's analysis of the draft's definition, 250 ms after it stops
 * changing. The last analysis stays while a new one runs, so the graph and
 * the problems do not flicker while typing.
 */
export function useAnalysis(kind: WiringKind, definition?: Definition) {
  const client = useClient(api.WiringServiceClient), connection = useConnection();
  const key = definitionKey(kind, definition);
  const [state, setState] = useState<{ key: string; analysis?: api.Analysis; pending: boolean; failed: boolean }>({ key: '', pending: false, failed: false });
  useEffect(() => {
    if (!definition || connection.connection !== 'connected') return;
    const controller = new AbortController();
    setState((old) => ({ ...old, pending: true }));
    const timer = setTimeout(() => {
      const request = kind === 'binding'
        ? client.analyzeBinding(create(api.AnalyzeBindingRequestSchema, { definition: definition as api.BindingDefinition }), { signal: controller.signal })
        : client.analyzeRule(create(api.AnalyzeRuleRequestSchema, { definition: definition as api.RuleDefinition }), { signal: controller.signal });
      void request.then((response) => { if (!controller.signal.aborted) setState({ key, analysis: response.analysis, pending: false, failed: false }); },
        () => { if (!controller.signal.aborted) setState((old) => ({ ...old, pending: false, failed: true })); });
    }, 250);
    return () => { clearTimeout(timer); controller.abort(); };
  }, [key, connection.connection]); // eslint-disable-line react-hooks/exhaustive-deps -- the key stands for the definition.
  return { analysis: state.analysis, current: !!definition && state.key === key && !state.pending, pending: state.pending, failed: state.failed };
}

/** Every problem of a draft at its place in the text: YAML and shape problems, then the server's violations. */
export function problemsOf(draft: Draft, violations: readonly api.Violation[] | undefined): DraftProblem[] {
  const out = [...draft.problems];
  for (const violation of violations ?? []) {
    const range = locate(draft, violation.path, violation.expr);
    out.push({ ...range, message: violation.message, severity: 'error', source: 'server', code: violation.code, path: violation.path });
  }
  return out.sort((a, b) => a.from - b.from);
}

/** Line and column (from 1) of an offset. */
export function lineColumn(text: string, offset: number): { line: number; column: number } {
  let line = 1, start = 0;
  for (let index = text.indexOf('\n'); index !== -1 && index < offset; index = text.indexOf('\n', index + 1)) { line++; start = index + 1; }
  return { line, column: offset - start + 1 };
}
