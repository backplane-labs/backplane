import { MergeView } from '@codemirror/merge';
import { EditorState } from '@codemirror/state';
import { EditorView, lineNumbers } from '@codemirror/view';
import { useEffect, useRef } from 'react';
import { editorHighlight, editorTheme } from './editor.js';
import { languageExtension } from './language.js';
import { useEditorText } from './locales.js';
import type { DiffViewerProps } from './types.js';

export function DiffSurface({ label, before, after, mode, language = 'text', height = 300 }: DiffViewerProps) {
  const host = useRef<HTMLDivElement>(null),
    text = useEditorText();
  const beforeLabel = `${label}: ${text('before')}`,
    afterLabel = `${label}: ${text('after')}`;
  useEffect(() => {
    if (!host.current) return;
    const extensions = [
      EditorState.readOnly.of(true),
      lineNumbers(),
      languageExtension(language),
      editorHighlight(mode),
      editorTheme(mode, height, 'fixed'),
    ];
    const view = new MergeView({
      parent: host.current,
      a: {
        doc: before,
        extensions: [
          ...extensions,
          EditorView.contentAttributes.of({ 'aria-label': beforeLabel, 'aria-readonly': 'true' }),
        ],
      },
      b: {
        doc: after,
        extensions: [
          ...extensions,
          EditorView.contentAttributes.of({ 'aria-label': afterLabel, 'aria-readonly': 'true' }),
        ],
      },
      collapseUnchanged: { margin: 3, minSize: 8 },
      highlightChanges: true,
      gutter: true,
    });
    return () => view.destroy();
  }, [before, after, mode, language, height, beforeLabel, afterLabel]);
  return (
    <section aria-label={label}>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr' }}>
        <span>{text('before')}</span>
        <span>{text('after')}</span>
      </div>
      <div ref={host} />
    </section>
  );
}
