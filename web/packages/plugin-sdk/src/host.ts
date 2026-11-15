import { getInstance } from '@module-federation/runtime';
import type { ComponentType } from 'react';
import { assertRelativePath, type PluginMetadata, SDK_MAJOR } from './index.js';

/** A descriptor from the platform catalog, never a URL supplied by module code. */
export interface PluginDescriptor {
  service: string;
  hash: string;
  path: string;
  sdkMajor: number;
}
export interface LoadedPlugin {
  Routes: ComponentType;
  navigation: PluginMetadata & { english: Record<string, unknown> };
}

function validateNavigation(value: unknown): asserts value is PluginMetadata['nav'] {
  if (!Array.isArray(value)) throw new Error('Invalid plugin navigation');
  const seen = new Set<string>();
  for (const entry of value) {
    if (
      !entry ||
      typeof entry.path !== 'string' ||
      typeof entry.labelKey !== 'string' ||
      !entry.labelKey ||
      seen.has(entry.path)
    ) {
      throw new Error('Invalid plugin navigation entry');
    }
    assertRelativePath(entry.path);
    seen.add(entry.path);
  }
}

/** Checks catalog + JSON metadata before any remote JavaScript is requested. */
export async function loadPlugin(
  descriptor: PluginDescriptor,
  options: { consoleURL: string; signal?: AbortSignal },
): Promise<LoadedPlugin> {
  if (descriptor.sdkMajor !== SDK_MAJOR) throw new Error(`Unsupported plugin SDK major: ${descriptor.sdkMajor}`);
  if (!/^[a-z][a-z0-9-]*$/.test(descriptor.service) || !/^[a-zA-Z0-9_-]+$/.test(descriptor.hash))
    throw new Error('Invalid plugin identity');
  const consoleURL = new URL(options.consoleURL);
  if (
    consoleURL.origin !== location.origin ||
    consoleURL.search ||
    consoleURL.hash ||
    !consoleURL.pathname.endsWith('/')
  )
    throw new Error('Invalid console URL');
  const expected = new URL(`plugins/${descriptor.service}/${descriptor.hash}/`, consoleURL);
  const base = new URL(descriptor.path, consoleURL);
  if (base.href !== expected.href) throw new Error('Plugin assets must use the catalog path on the console origin');
  const response = await fetch(new URL('plugin.json', base), {
    credentials: 'same-origin',
    signal: options.signal,
    redirect: 'error',
  });
  if (!response.ok) throw new Error(`Plugin metadata request failed: ${response.status}`);
  const metadata = await response.json();
  if (metadata.name !== descriptor.service || metadata.sdk_major !== SDK_MAJOR)
    throw new Error('Plugin metadata does not match the catalog');
  validateNavigation(metadata.nav);
  options.signal?.throwIfAborted();
  const runtime = getInstance((instance) => instance.name === 'backplane_console');
  if (!runtime) throw new Error('The platformHostBuild runtime is required');
  const name = `backplane_${descriptor.service.replaceAll('-', '_')}_${descriptor.hash}`;
  runtime.registerRemotes([{ name, entry: new URL('mf-manifest.json', base).href }]);
  const [routes, nav] = await Promise.all([
    runtime.loadRemote<{ default: ComponentType }>(`${name}/Routes`),
    runtime.loadRemote<{ default: LoadedPlugin['navigation'] }>(`${name}/Nav`),
  ]);
  options.signal?.throwIfAborted();
  if (
    !routes ||
    typeof routes.default !== 'function' ||
    !nav?.default ||
    nav.default.service !== descriptor.service ||
    !nav.default.english ||
    typeof nav.default.english !== 'object'
  ) {
    throw new Error('Invalid plugin exports');
  }
  validateNavigation(nav.default.nav);
  if (JSON.stringify(nav.default.nav) !== JSON.stringify(metadata.nav))
    throw new Error('Plugin navigation does not match its metadata');
  return { Routes: routes.default, navigation: nav.default };
}
