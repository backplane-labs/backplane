import { defineConfig } from 'vite';
import { platformHostBuild } from '@backplane-labs/backplane-plugin-build/federation';
import tailwind from '@tailwindcss/vite';
import { consoleDevServer } from './dev-proxy';
export default defineConfig(({ command }) => {
  const base = process.env.BACKPLANE_CONSOLE_BASE ?? '/backplane/';
  const config = platformHostBuild({ base });
  config.plugins!.push(tailwind());
  if (command === 'serve') {
    config.server = consoleDevServer(base, process.env.BACKPLANE_DEV_TARGET);
    // These workspace-kit chunks are lazy and invisible to the initial scan.
    // Discovering them on navigation would reload the page and discard drafts.
    config.optimizeDeps = { include: [
      '@codemirror/state', '@codemirror/view', '@codemirror/language',
      '@codemirror/lang-json', '@codemirror/lang-yaml', '@codemirror/commands', '@codemirror/autocomplete',
      '@codemirror/lint', '@codemirror/merge', '@lezer/highlight',
      'uplot', '@grafana/schema',
      '@gopherex/schemapb', '@gopherex/ws-proto-transport',
      '@tanstack/react-table', '@tanstack/react-virtual', '@xyflow/react',
      'class-variance-authority', 'clsx', 'cmdk', 'date-fns/locale',
      'embla-carousel-react', 'lossless-json', 'radix-ui', 'react-day-picker',
      'react-hook-form', 'react-resizable-panels', 'sonner', 'tailwind-merge', 'yaml',
    ] };
  }
  return config;
});
