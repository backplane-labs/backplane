import {
  CalendarDays,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  ChevronsRight,
  ChevronUp,
  CircleHelp,
  Clipboard,
  Clock9,
  Copy,
  ExternalLink,
  Globe,
  Info,
  type LucideIcon,
  RotateCw,
  Search,
  X,
  ZoomOut,
} from 'lucide-react';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import type { Plugin } from 'vite';

// Only the icons used by the supported Grafana adapters belong here. These are
// Lucide glyphs, not copied Grafana application assets. Missing names fail loudly
// in the fixture instead of fetching HTML through the SPA fallback.
const glyphs: Record<string, LucideIcon> = {
  'clock-nine': Clock9,
  'angle-double-left': ChevronsLeft,
  'angle-double-right': ChevronsRight,
  'angle-left': ChevronLeft,
  'angle-right': ChevronRight,
  'angle-down': ChevronDown,
  'angle-up': ChevronUp,
  'search-minus': ZoomOut,
  search: Search,
  'calendar-alt': CalendarDays,
  copy: Copy,
  'clipboard-alt': Clipboard,
  'external-link-alt': ExternalLink,
  check: Check,
  times: X,
  sync: RotateCw,
  'info-circle': Info,
  'question-circle': CircleHelp,
  globe: Globe,
};

/** Installs assets and resolves their base independently of the page route. */
export function grafanaAssets(): Plugin {
  const virtual = 'virtual:backplane-grafana-assets';
  const resolved = `\0${virtual}`;
  const prefix = '/@backplane/grafana/';
  const files = new Map(
    Object.entries(glyphs).map(([name, icon]) => [
      `build/img/icons/unicons/${name}.svg`,
      renderToStaticMarkup(createElement(icon, { size: 24, color: 'currentColor', style: { fill: 'none' } })),
    ]),
  );
  let command: 'serve' | 'build';
  let reference: string;
  return {
    name: 'backplane-grafana-assets',
    configResolved(config) {
      command = config.command;
    },
    resolveId(id) {
      if (id === virtual) return resolved;
    },
    buildStart() {
      if (command !== 'build') return;
      for (const [name, source] of files) {
        const ref = this.emitFile({ type: 'asset', fileName: `grafana/${name}`, source });
        if (name.endsWith('/clock-nine.svg')) reference = ref;
      }
    },
    load(id) {
      if (id !== resolved) return;
      const base =
        command === 'serve'
          ? `new URL(${JSON.stringify(prefix)}, location.origin).href`
          : `new URL('../../../../', new URL(import.meta.ROLLUP_FILE_URL_${reference}, location.href)).href`;
      return `window.__grafana_public_path__ = ${base};`;
    },
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (!req.url?.startsWith(prefix)) return next();
        const file = files.get(req.url.slice(prefix.length).split('?')[0]);
        res.statusCode = file ? 200 : 404;
        res.setHeader('Content-Type', file ? 'image/svg+xml' : 'text/plain');
        res.end(file ?? 'Unknown platform icon');
      });
    },
  };
}
