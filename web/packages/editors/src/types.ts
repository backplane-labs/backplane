export type QueryLanguage = 'cel' | 'promql' | 'metricsql' | 'logsql' | 'traceql';
export type EditorLanguage = QueryLanguage | 'json' | 'yaml' | 'html' | 'text';
export interface EditorDiagnostic { from: number; to: number; severity: 'error' | 'warning' | 'info' | 'hint'; message: string }
export interface EditorCompletion { label: string; detail?: string; type?: 'variable' | 'function' | 'keyword' | 'property'; apply?: string }
/** Completions that replace the text from `from` (UTF-16 offset) to the cursor. */
export interface EditorCompletionResult { from: number; options: readonly EditorCompletion[] }
export interface CodeEditorProps {
  label: string; value: string; onChange?: (value: string) => void; language?: EditorLanguage;
  /** The least height in pixels (the editor grows with its text, then scrolls), or 'fill': the container's height. */
  mode: 'dark' | 'light'; readOnly?: boolean; disabled?: boolean; inline?: boolean; height?: number | 'fill';
  onSubmit?: () => void; diagnostics?: readonly EditorDiagnostic[];
  complete?: (request: { value: string; position: number; language: EditorLanguage; signal: AbortSignal; explicit: boolean }) => Promise<readonly EditorCompletion[] | EditorCompletionResult | null>;
  validate?: (value: string, signal: AbortSignal) => Promise<readonly EditorDiagnostic[]>;
  onDiagnostics?: (diagnostics: readonly EditorDiagnostic[]) => void;
  /** Text of a hover tooltip at a UTF-16 offset; undefined: none. */
  hover?: (position: number, value: string) => string | undefined;
  /** Selects and scrolls to a range whenever `key` changes. */
  reveal?: { from: number; to: number; key: number };
  /** Reports the cursor's UTF-16 offset as it moves. */
  onCursor?: (position: number) => void;
  /** Hide the list of diagnostics under the editor (the host shows them). */
  hideMessages?: boolean;
}
export interface DiffViewerProps { label: string; before: string; after: string; mode: 'dark' | 'light'; language?: 'json' | 'yaml' | 'text'; height?: number }
