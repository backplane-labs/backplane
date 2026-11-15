import type { ThemeMode } from '@backplane-labs/backplane-theme';
import type { i18n } from 'i18next';
import { createContext, type ReactNode, useContext } from 'react';
import { type RouteObject, useNavigate, useRoutes } from 'react-router-dom';

export const SDK_MAJOR = 0;

export interface NavEntry {
  path: string;
  labelKey: string;
}
export interface PluginMetadata {
  service: string;
  nav: readonly NavEntry[];
}
/** Every value a `catch` can hold, as the host's Errors pipeline accepts it. */
export type ThrownValue = null | undefined | boolean | number | string | bigint | symbol | object;
export interface PluginDefinition extends PluginMetadata {
  routes: RouteObject[];
  english: Record<string, string>;
}
export interface PluginContext {
  service: string;
  basePath: string;
  mode: ThemeMode;
  /** Explicit fixture marker, never inferred from connection failures. */
  environment: 'standalone' | 'embedded';
  /** The host's error reporter (the console's @backplane-labs/backplane-errors client); absent standalone. */
  reportError?: ErrorReporter;
}
/** Reports an exception to the installation's Errors, with attributes that filter it there. */
export type ErrorReporter = (
  error: ThrownValue,
  options?: { handled?: boolean; attributes?: Readonly<Record<string, string>> },
) => void;

export function assertRelativePath(path: string) {
  let decoded: string;
  try {
    decoded = decodeURIComponent(path);
  } catch {
    throw new Error(`Invalid module path: ${path}`);
  }
  if (
    decoded.startsWith('/') ||
    [...decoded].some((char) => char.charCodeAt(0) <= 0x1f || char === '\\' || char === '?' || char === '#') ||
    decoded.split('/').some((part) => part === '.' || part === '..')
  ) {
    throw new Error(`Module path must remain relative: ${path}`);
  }
}

export function definePlugin(definition: PluginDefinition): PluginDefinition {
  if (!/^[a-z][a-z0-9-]*$/.test(definition.service)) throw new Error('Invalid module service name');
  const visit = (routes: RouteObject[]) => {
    for (const route of routes) {
      if (route.path !== undefined) assertRelativePath(route.path);
      if (route.children) visit(route.children);
    }
  };
  visit(definition.routes);
  const paths = new Set<string>();
  for (const entry of definition.nav) {
    assertRelativePath(entry.path);
    if (!entry.labelKey || paths.has(entry.path))
      throw new Error('Navigation entries need unique paths and translation keys');
    paths.add(entry.path);
  }
  return definition;
}

export function installPluginTranslations(instance: i18n, definition: Pick<PluginDefinition, 'service' | 'english'>) {
  const namespace = `module.${definition.service}`;
  instance.addResourceBundle('en', namespace, definition.english, false, true);
  return namespace;
}

const Context = createContext<PluginContext | null>(null);
export function PluginProvider({ context, children }: { context: PluginContext; children: ReactNode }) {
  return <Context.Provider value={context}>{children}</Context.Provider>;
}
export function usePluginContext(): PluginContext {
  const context = useContext(Context);
  if (!context) throw new Error('PluginProvider is required');
  return context;
}
export function PluginRoutes({ definition }: { definition: PluginDefinition }) {
  const context = usePluginContext();
  if (context.service !== definition.service) throw new Error('Module does not match its host route');
  return useRoutes(definition.routes);
}
export function usePluginNavigate() {
  const navigate = useNavigate();
  const { basePath } = usePluginContext();
  return (path: string) => {
    assertRelativePath(path);
    void navigate(`${basePath.replace(/\/$/, '')}/${path}`);
  };
}

/**
 * Reports a module's exceptions to the installation's Errors section, marked
 * backplane.module=<service> (standalone: to the browser console). Unhandled
 * errors and render failures are reported by the host already; this is for
 * handled ones worth seeing.
 */
export function usePluginErrorReporter(): ErrorReporter {
  const { service, reportError } = usePluginContext();
  return (error, options) => {
    if (!reportError) {
      // Standalone has no Errors pipeline: the platform's reportError puts it in the browser console.
      globalThis.reportError(error);
      return;
    }
    reportError(error, {
      handled: true,
      ...options,
      attributes: { ...options?.attributes, 'backplane.module': service },
    });
  };
}
