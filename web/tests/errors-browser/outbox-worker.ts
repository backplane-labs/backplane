// A dedicated worker running the SDK as an application's worker does: no window, no document.
import { createOtlpClient } from '../../packages/errors/src/otlp'

const diagnostics: string[] = []

try {
  const client = createOtlpClient({
    url: '/v1/logs',
    resource: { 'service.name': 'worker' },
    outbox: { name: `worker-${crypto.randomUUID()}` },
    captureUnhandled: true,
    onDiagnostic: (diagnostic) => diagnostics.push(diagnostic.code),
  })
  const captured = client.captureException(new Error('worker error')).status
  void Promise.reject(new Error('unhandled in worker'))
  setTimeout(() => {
    postMessage({ captured, emitted: client.stats().emitted, diagnostics })
    client.dispose()
  }, 100)
} catch (error) {
  postMessage({ failed: String(error) })
}
