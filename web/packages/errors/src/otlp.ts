import {
  LoggerProvider,
  BatchLogRecordProcessor,
  type BatchLogRecordProcessorBrowserOptions,
} from '@opentelemetry/sdk-logs'
import { OTLPLogExporter } from '@opentelemetry/exporter-logs-otlp-proto'
import { resourceFromAttributes } from '@opentelemetry/resources'
import type { Attributes } from '@opentelemetry/api'
import { createClient, type ClientOptions } from './index.js'
import { PersistentLogProcessor, type OutboxPorts } from './outbox.js'
export type { OutboxPorts } from './outbox.js'

export type OtlpClientOptions = Omit<ClientOptions, 'loggerProvider' | 'flush'> & {
  url: string
  headers?: Record<string, string>
  resource?: Attributes
  /** Persist exports in storage bound to this url and account; otherwise batch in memory. */
  outbox?: OutboxPorts
  batch?: Omit<BatchLogRecordProcessorBrowserOptions, 'exporter'>
}

/** Owns only this provider; never installs globals or closes another provider. */
export function createOtlpClient(options: OtlpClientOptions) {
  if (options.outbox && options.batch)
    throw new TypeError('outbox_and_batch_are_mutually_exclusive')
  const exporter = new OTLPLogExporter({
    url: options.url,
    headers: options.headers,
    timeoutMillis: options.outbox ? 10_000 : undefined,
  })
  const persistent = options.outbox
    ? new PersistentLogProcessor(exporter, options.outbox, options.runtime, (code) =>
        options.onDiagnostic?.({ code, stage: 'capture' })
      )
    : undefined
  const provider = new LoggerProvider({
    resource: resourceFromAttributes(options.resource ?? {}),
    logRecordLimits: { attributeValueLengthLimit: Infinity, attributeCountLimit: Infinity },
    processors: [persistent ?? new BatchLogRecordProcessor({ ...options.batch, exporter })],
  })
  let client: ReturnType<typeof createClient>
  try {
    client = createClient({
      ...options,
      loggerProvider: provider,
      flush: () => provider.forceFlush(),
    })
  } catch (error) {
    persistent?.stop()
    void provider.shutdown().catch(() => {})
    throw error
  }
  let shutdown: Promise<void> | undefined
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
      client.dispose()
      persistent?.stop()
    },
    shutdown: () => {
      client.dispose()
      shutdown ??= provider.shutdown()
      return shutdown
    },
  }
}
