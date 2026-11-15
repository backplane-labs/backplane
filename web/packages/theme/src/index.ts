import { createTheme } from '@grafana/data';
import { type ThemeMode, tokens, typography } from './tokens.js';

export { cssVariables, palette, type ThemeMode, tokens, typography } from './tokens.js';

// Grafana's exported factory is marked internal upstream. Keep its use isolated,
// pin the dependency, and verify both modes before upgrading.
export function createGrafanaTheme(mode: ThemeMode) {
  const t = tokens[mode];
  return createTheme({
    colors: {
      mode,
      primary: { main: t.primary, text: t.link, contrastText: t['primary-foreground'] },
      secondary: { main: t.secondary, text: t['secondary-foreground'] },
      success: { main: t.success, text: t.success, contrastText: t['success-foreground'] },
      error: { main: t.destructive, text: t.destructive, contrastText: t['destructive-foreground'] },
      warning: { main: t.warning, text: t.warning, contrastText: t['warning-foreground'] },
      info: { main: t.info, text: t.info, contrastText: t['info-foreground'] },
      text: { primary: t.foreground, secondary: t['muted-foreground'], link: t.link, maxContrast: t.foreground },
      background: { canvas: t.background, primary: t.card, secondary: t.raised, elevated: t.popover },
      border: { weak: t.border, medium: t['border-strong'], strong: t.subtle },
      action: { hover: t.accent, selected: t.accent, selectedBorder: t.ring, focus: t.ring },
    },
    typography,
    shape: { borderRadius: 4, borderRadiusSm: 2, borderRadiusLg: 6 },
  });
}
