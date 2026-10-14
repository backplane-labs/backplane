import { federation } from '@module-federation/vite';
import react from '@vitejs/plugin-react';
import type { PluginOption, UserConfig } from 'vite';
import { grafanaAssets } from './index.js';
import { createRequire } from 'node:module';

export interface ModuleBuildOptions {
  service: string;
  nav: Array<{ path: string; labelKey: string }>;
  embedded?: boolean;
}

// Release CI stamps this manifest together with every shared platform package.
const sdkVersion = createRequire(import.meta.url)('../package.json').version as string;
const platformVersion = `^${sdkVersion}`;

const versions: Record<string, string> = {
  react: '^19.3.0', 'react/': '^19.3.0', 'react-dom': '^19.3.0', 'react-dom/': '^19.3.0',
  'react-router': '^7.18.4', 'react-router-dom': '^7.18.4',
  i18next: '^26.4.2', 'react-i18next': '^17.0.15',
  '@grafana/ui': '13.2.3', '@grafana/data': '13.2.3',
  '@backplane-labs/backplane-theme': platformVersion, '@backplane-labs/backplane-ui': platformVersion,
  '@backplane-labs/backplane-client': platformVersion, '@backplane-labs/backplane-react': platformVersion,
  '@backplane-labs/backplane-api': platformVersion,
  '@backplane-labs/backplane-plugin-sdk': platformVersion,
  '@backplane-labs/backplane-schema-forms': platformVersion,
  '@backplane-labs/backplane-editors': platformVersion,
  '@backplane-labs/backplane-charts': platformVersion,
  '@backplane-labs/backplane-observability-ui': platformVersion,
  '@backplane-labs/backplane-platform-ui': platformVersion,

};
// Router dedupe is intentionally excluded: Grafana carries private v5/v6
// routers, including in the dev dependency optimizer. MF shares the public v7.
function shared(embedded: boolean) {
  return Object.fromEntries(Object.entries(versions).map(([name, requiredVersion]) => [name, {
    singleton: true, strictVersion: true, requiredVersion,
    ...(embedded ? { import: false as const } : {}),
  }]));
}

// Grafana's private v5/v6 compatibility controls must retain their own router
// dependencies. MF matches bare names before normal package resolution; keep
// that rewrite from upgrading those private dependencies to the host router.
function privateGrafanaRouters(): PluginOption {
  const resolvePrivate = (source: string, importer?: string) => {
    if (!importer?.includes('/node_modules/') || !['react-router', 'react-router-dom'].includes(source)) return;
    const request = createRequire(importer.split('?')[0]);
    const installed = request(`${source}/package.json`) as { version: string };
    if (installed.version.startsWith('7.')) return;
    return request.resolve(source);
  };
  return {
    name: 'backplane-private-grafana-routers', enforce: 'pre',
    config(config) {
      // Vite's dependency optimizer has its own plugin pipeline. Resolve
      // Grafana's legacy routers there before federation rewrites shared names.
      config.optimizeDeps ??= {};
      config.optimizeDeps.rolldownOptions ??= {};
      config.optimizeDeps.rolldownOptions.plugins = [{
        name: 'backplane-private-grafana-routers-optimizer',
        resolveId: { order: 'pre', handler: resolvePrivate },
      }, config.optimizeDeps.rolldownOptions.plugins];
    },
    resolveId: {
      order: 'pre',
      handler: resolvePrivate,
    },
  };
}
export function moduleBuild(options: ModuleBuildOptions): UserConfig {
  if (!/^[a-z][a-z0-9-]*$/.test(options.service)) throw new Error('Invalid module service name');
  const plugins: PluginOption[] = [privateGrafanaRouters(), react()];
  if (!options.embedded) plugins.push(grafanaAssets());
  else plugins.push(federation({
    name: `backplane_${options.service.replaceAll('-', '_')}`,
    filename: 'remoteEntry.js', manifest: true, dts: false,
    exposes: { './Routes': './src/embedded.tsx', './Nav': './src/navigation.ts' },
    shared: shared(true), hostInitInjectLocation: 'entry',
  }), {
    name: 'backplane-plugin-manifest',
    generateBundle() {
      this.emitFile({ type: 'asset', fileName: 'plugin.json', source: JSON.stringify({
        name: options.service, sdk_major: 0, nav: options.nav,
      }, null, 2) + '\n' });
    },
  });
  return {
    plugins, base: options.embedded ? './' : '/',
    resolve: { dedupe: Object.keys(versions).filter((name) => !name.endsWith('/') && !name.startsWith('react-router')) },
    build: { outDir: options.embedded ? 'dist/plugin' : 'dist/standalone', ...(options.embedded ? { rollupOptions: { input: './src/embedded.tsx' } } : {}) },
  };
}
export function platformHostBuild(options: { base?: string } = {}): UserConfig {
  return {
    plugins: [privateGrafanaRouters(), grafanaAssets(), react(), federation({
      name: 'backplane_console', shared: shared(false), hostInitInjectLocation: 'entry', dts: false,
    })], base: options.base ?? '/',
    resolve: { dedupe: Object.keys(versions).filter((name) => !name.endsWith('/') && !name.startsWith('react-router')) },
  };
}
