import { createOtlpClient } from '@gopherex/backplane-errors/otlp';
import { createReactErrorHandler, instrumentBrowser } from '@gopherex/backplane-errors/browser';

/**
 * The console reports its own errors like any frontend: through the
 * installation's session-authenticated OTLP ingest, as backplane-console. Only the live
 * console reports (the compatibility fixture has no ingest). Nothing is sent
 * when the ingest is disabled or no session exists: delivery failures stay local.
 */
export function consoleErrors(base: string) {
  const url = new URL(`${base.replace(/\/$/, '')}/auth/telemetry/v1/logs`, location.origin).href;
  const client = createOtlpClient({
    url,
    resource: { 'service.name': 'backplane-console' },
    captureUnhandled: true,
    rateLimit: { burst: 20, perSecond: 1 },
  });
  // Fetches, XHRs and navigations become breadcrumbs; URLs lose query and credentials.
  instrumentBrowser(client, { fetch: true, xhr: true, navigation: true, excludeUrls: [url] });
  const reporter = { client, onReactError: createReactErrorHandler(client),
    report: (error: unknown, options?: { handled?: boolean; attributes?: Readonly<Record<string, string>> }) =>
      client.captureException(error, { handled: options?.handled ?? true, attributes: options?.attributes }) };
  current = reporter.report;
  return reporter;
}

let current: ((error: unknown, options?: { handled?: boolean; attributes?: Readonly<Record<string, string>> }) => void) | undefined;
/** The live console's reporter for module contexts; undefined in the fixture. */
export const consoleErrorReporter = () => current;
