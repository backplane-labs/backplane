import { useEffect, useRef } from 'react';
import { EditorState } from '@codemirror/state';
import { EditorView, keymap } from '@codemirror/view';
import { defaultHighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { useTranslation } from 'react-i18next';
import { tokens, typography, type ThemeMode } from '@backplane-labs/backplane-theme';

// Compatibility fixture. Language parsing/completion belongs to the editors
// package; the fixture must not advertise a working LogsQL parser yet.
export function QueryEditor({ mode }: { mode: ThemeMode }) {
  const host = useRef<HTMLDivElement>(null);
  const value = useRef('_time:5m {service.name="hello"}');
  const { t } = useTranslation();
  const label = t('queryLabel');
  useEffect(() => {
    if (!host.current) return;
    const colors = tokens[mode];
    const view = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: value.current,
        extensions: [
          syntaxHighlighting(defaultHighlightStyle),
          keymap.of([]),
          EditorView.contentAttributes.of({ 'aria-label': label }),
          EditorView.updateListener.of((update) => { if (update.docChanged) value.current = update.state.doc.toString(); }),
          EditorView.theme({
            '&': { backgroundColor: colors.muted, color: colors.foreground, minHeight: '96px', fontFamily: typography.fontFamilyMonospace },
            '.cm-content': { padding: '12px', caretColor: colors.foreground },
            '&.cm-focused': { outline: `2px solid ${colors.ring}`, outlineOffset: '2px' },
            '.cm-selectionBackground': { backgroundColor: colors.accent },
          }, { dark: mode === 'dark' }),
        ],
      }),
    });
    return () => view.destroy();
  }, [mode, label]);
  return <div ref={host} />;
}
