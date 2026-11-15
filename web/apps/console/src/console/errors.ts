import { type CapturedInput, createReactErrorHandler } from '@backplane-labs/backplane-errors';
import { createBrowserClient, instrumentBrowser } from '@backplane-labs/backplane-errors/browser';
import type { ErrorReporter } from '@backplane-labs/backplane-plugin-sdk';
import type { RootOptions } from 'react-dom/client';

type ReportedError = Parameters<ErrorReporter>[0];
/** What React hands a root's error callbacks: any thrown value, untyped by React's contract. */
type ReactThrown = Parameters<NonNullable<RootOptions['onCaughtError']>>[0];
type ReportOptions = { handled?: boolean; attributes?: Readonly<Record<string, string>> };
/** CapturedInput already covers every value a catch can hold; the check only makes that explicit to the compiler. */
const isCaptured = (error: ReactThrown): error is CapturedInput =>
  error === null ||
  error === undefined ||
  typeof error === 'boolean' ||
  typeof error === 'number' ||
  typeof error === 'string' ||
  typeof error === 'bigint' ||
  typeof error === 'symbol' ||
  typeof error === 'object' ||
  typeof error === 'function';

/**
 * The console reports its own errors like any frontend: through the
 * installation's session-authenticated OTLP ingest, as backplane-console. Only the live
 * console reports (the compatibility fixture has no ingest). Nothing is sent
 * when the ingest is disabled or no session exists: delivery failures stay local.
 */
export function consoleErrors(base: string) {
  const url = new URL(`${base.replace(/\/$/, '')}/auth/telemetry/v1/logs`, location.origin).href;
  const client = createBrowserClient({
    url,
    resource: { 'service.name': 'backplane-console' },
    captureUnhandled: true,
    rateLimit: { burst: 20, perSecond: 1 },
  });
  // Fetches, XHRs and navigations become breadcrumbs; URLs lose query and credentials.
  instrumentBrowser(client, { fetch: true, xhr: true, navigation: true, excludeUrls: [url] });
  const reactError = createReactErrorHandler(client);
  const reporter = {
    client,
    // React hands over its errors as an untyped boundary value; accepted here, not in the package's handler.
    onReactError: (error: ReactThrown, info: { componentStack?: string | null }) => {
      if (isCaptured(error)) reactError(error, info);
    },
    report: (error: ReportedError, options?: ReportOptions) => {
      if (isCaptured(error))
        client.captureException(error, { handled: options?.handled ?? true, attributes: options?.attributes });
    },
  };
  current = reporter.report;
  return reporter;
}

let current: ((error: ReportedError, options?: ReportOptions) => void) | undefined;
/** The live console's reporter for module contexts; undefined in the fixture. */
export const consoleErrorReporter = (): ErrorReporter | undefined => current;
