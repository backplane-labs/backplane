import type { Attributes } from '@opentelemetry/api';
import { OTLPLogExporter } from '@opentelemetry/exporter-logs-otlp-proto';
import { resourceFromAttributes } from '@opentelemetry/resources';
import {
  BatchLogRecordProcessor,
  type BatchLogRecordProcessorBrowserOptions,
  LoggerProvider,
} from '@opentelemetry/sdk-logs';
import { fetchSender } from './delivery.js';
import { type ClientOptions, createClient } from './index.js';
import { type OutboxPorts, PersistentLogProcessor } from './outbox.js';

export type { OutboxPorts } from './outbox.js';

export type OtlpClientOptions = Omit<ClientOptions, 'loggerProvider' | 'flush'> & {
  url: string;
  headers?: Record<string, string>;
  resource?: Attributes;
  /** The outbox's fetch (a host's own HTTP stack); the global one by default. */
  fetch?: typeof fetch;
  /** Persist exports in storage bound to this url and account; otherwise batch in memory. */
  outbox?: OutboxPorts;
  batch?: Omit<BatchLogRecordProcessorBrowserOptions, 'exporter'>;
};

/** Owns only this provider; never installs globals or closes another provider. */
export function createOtlpClient(options: OtlpClientOptions) {
  if (options.outbox && options.batch) throw new TypeError('outbox_and_batch_are_mutually_exclusive');
  // An outbox stores OTLP bytes and posts them itself; the batch path hands records to the exporter.
  const persistent = options.outbox
    ? new PersistentLogProcessor(
        fetchSender({ url: options.url, headers: options.headers, fetch: options.fetch }),
        options.outbox,
        options.runtime,
        (code) => options.onDiagnostic?.({ code, stage: 'capture' }),
      )
    : undefined;
  const provider = new LoggerProvider({
    resource: resourceFromAttributes(options.resource ?? {}),
    logRecordLimits: { attributeValueLengthLimit: Infinity, attributeCountLimit: Infinity },
    processors: [
      persistent ??
        new BatchLogRecordProcessor({
          ...options.batch,
          exporter: new OTLPLogExporter({ url: options.url, headers: options.headers }),
        }),
    ],
  });
  let client: ReturnType<typeof createClient>;
  try {
    client = createClient({
      ...options,
      loggerProvider: provider,
      flush: () => provider.forceFlush(),
    });
  } catch (error) {
    persistent?.stop();
    void provider.shutdown().catch(() => {});
    throw error;
  }
  let shutdown: Promise<void> | undefined;
  return {
    ...client,
    provider,
    outbox: persistent
      ? {
          flushStorage: () => persistent.flushStorage(),
          stats: () => persistent.stats(),
          clear: () => persistent.clear(),
        }
      : undefined,
    dispose: () => {
      client.dispose();
      persistent?.stop();
    },
    shutdown: () => {
      client.dispose();
      shutdown ??= provider.shutdown();
      return shutdown;
    },
  };
}
