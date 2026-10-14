import { useLayoutEffect, type ReactNode } from 'react';
import type { Preview } from '@storybook/react-vite';
import { I18nextProvider } from 'react-i18next';
import { TooltipProvider } from '@backplane-labs/backplane-ui';
import { i18n } from '../src/i18n';
import '@fontsource/ibm-plex-sans/400.css';
import '@fontsource/ibm-plex-mono/400.css';
import '@backplane-labs/backplane-ui/style.css';
import '@backplane-labs/backplane-theme/style.css';

function Frame({ mode, children }: { mode: string; children: ReactNode }) {
  useLayoutEffect(() => {
    document.documentElement.dataset.theme = mode;
    document.documentElement.classList.toggle('dark', mode === 'dark');
    document.body.style.backgroundColor = 'var(--background)';
  }, [mode]);
  return <I18nextProvider i18n={i18n}><TooltipProvider>
    <main style={{ padding: 24, maxWidth: 960, color: 'var(--foreground)', fontFamily: 'var(--font-sans)', fontSize: 14 }}>
      {children}
    </main>
  </TooltipProvider></I18nextProvider>;
}

const preview: Preview = {
  globalTypes: { theme: { description: 'Platform theme', toolbar: { icon: 'circlehollow', items: ['dark', 'light'], dynamicTitle: true } } },
  initialGlobals: { theme: 'dark' },
  parameters: { layout: 'fullscreen', a11y: { test: 'error', manual: true } }, // Playwright owns automated axe runs; avoid concurrent addon scans.
  decorators: [(Story, context) => <Frame mode={context.globals.theme}><Story /></Frame>],
};
export default preview;
