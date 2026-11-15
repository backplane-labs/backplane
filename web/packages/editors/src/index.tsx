import { Button, ClipboardButton, SelectControl } from '@backplane-labs/backplane-ui';
import { lazy, Suspense } from 'react';
import { useEditorText } from './locales.js';
import { type DisplayValue, displayJSON } from './model.js';
import type { CodeEditorProps, DiffViewerProps, QueryLanguage } from './types.js';

export { editorsEnglish } from './locales.js';
export { displayJSON } from './model.js';
export type * from './types.js';

// Named exports behind React.lazy keep the code-split chunks default-free.
const Editor = lazy(() => import('./editor.js').then((m) => ({ default: m.CodeEditorSurface }))),
  Diff = lazy(() => import('./diff.js').then((m) => ({ default: m.DiffSurface })));

export function CodeEditor(props: CodeEditorProps) {
  const text = useEditorText();
  return (
    <Suspense fallback={<span role="status">{text('loading')}</span>}>
      <Editor {...props} />
    </Suspense>
  );
}
export function InlineCodeEditor(props: Omit<CodeEditorProps, 'inline'>) {
  return <CodeEditor {...props} inline />;
}
export function DiffViewer(props: DiffViewerProps) {
  const text = useEditorText();
  return (
    <Suspense fallback={<span role="status">{text('loading')}</span>}>
      <Diff {...props} />
    </Suspense>
  );
}
export function QueryEditor({
  languages,
  language,
  onLanguageChange,
  ...props
}: Omit<CodeEditorProps, 'language'> & {
  languages: readonly QueryLanguage[];
  language: QueryLanguage;
  onLanguageChange: (language: QueryLanguage) => void;
}) {
  const text = useEditorText();
  if (!languages.includes(language)) throw new Error('Selected query language is not supported by this source');
  return (
    <div style={{ display: 'grid', gap: 8 }}>
      <div style={{ display: 'flex', gap: 8 }}>
        <SelectControl
          aria-label={text('language')}
          value={language}
          disabled={props.disabled || props.readOnly}
          onValueChange={(next) => {
            const selected = languages.find((item) => item === next);
            if (selected) onLanguageChange(selected);
          }}
          options={languages.map((item) => ({
            value: item,
            label:
              item === 'cel'
                ? 'CEL'
                : item === 'logsql'
                  ? 'LogsQL'
                  : item === 'traceql'
                    ? 'TraceQL'
                    : item === 'metricsql'
                      ? 'MetricsQL'
                      : 'PromQL',
          }))}
        />
        {props.onSubmit && (
          <Button type="button" disabled={props.disabled} onClick={props.onSubmit}>
            {text('run')}
          </Button>
        )}
      </div>
      <CodeEditor {...props} language={language} />
    </div>
  );
}
/** Pass raw JSON text to preserve number lexemes; objects containing bigint become decimal strings. */
export function JSONViewer({
  value,
  ...props
}: Omit<CodeEditorProps, 'value' | 'language' | 'readOnly' | 'onChange'> & { value: DisplayValue }) {
  const content = typeof value === 'string' ? value : displayJSON(value);
  return <CodeEditor {...props} value={content} language="json" readOnly />;
}
export function EditorActions({
  value,
  filename = 'value.txt',
  contentType = 'text/plain',
}: {
  value: string;
  filename?: string;
  contentType?: string;
}) {
  const text = useEditorText();
  return (
    <div style={{ display: 'flex', gap: 8 }}>
      <ClipboardButton value={value} />
      <Button
        type="button"
        variant="outline"
        onClick={() => {
          const url = URL.createObjectURL(new Blob([value], { type: contentType }));
          const anchor = document.createElement('a');
          anchor.href = url;
          anchor.download = filename;
          anchor.click();
          setTimeout(() => URL.revokeObjectURL(url), 1000);
        }}
      >
        {text('download')}
      </Button>
    </div>
  );
}
