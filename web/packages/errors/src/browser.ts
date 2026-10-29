import { context } from '@opentelemetry/api'
import type { createClient, CaptureOptions } from './index.js'

type Client = ReturnType<typeof createClient>
export interface BrowserBreadcrumbOptions {
  fetch?: boolean
  xhr?: boolean
  navigation?: boolean
  /** Exact endpoints, in addition to the default OTLP /v1/{logs,traces,metrics} exclusion. */
  excludeUrls?: readonly string[]
}

/** Intentionally removes credentials, query and fragment. Paths may still contain personal data. */
export function safeUrl(input: string): string {
  const url = new URL(input, location.href)
  if (!['http:', 'https:'].includes(url.protocol)) throw new TypeError('unsupported_url')
  return `${url.origin}${url.pathname}`
}

const ownerKey = Symbol.for('backplane.errors.browser-breadcrumbs.v1')
/** Explicit opt-in; importing this module does not patch browser APIs. */
export function instrumentBrowser(client: Client, options: BrowserBreadcrumbOptions) {
  // fetch and XHR breadcrumbs work in a worker too; navigation needs a page.
  if (options.navigation && typeof history === 'undefined') throw new Error('navigation_breadcrumbs_need_a_page')
  const host = globalThis as unknown as Record<symbol, unknown>
  if (host[ownerKey]) throw new Error('browser_instrumentation_already_owned')
  const owner = {}
  let closed = false
  const cleanups: (() => void)[] = []
  const excluded = new Set((options.excludeUrls ?? []).map(safeUrl))
  host[ownerKey] = owner
  const metadata = (input: string, method: string) => {
    const url = safeUrl(input)
    if (excluded.has(url) || /\/v1\/(logs|traces|metrics)\/?$/.test(new URL(url).pathname)) return
    return { url, method: method.toUpperCase() }
  }
  const record = (
    name: string,
    data: Parameters<Client['addBreadcrumb']>[1],
    ctx = context.active()
  ) => {
    if (closed) return
    try {
      client.addBreadcrumb(name, data, { context: ctx })
    } catch {
      /* Never break application APIs. */
    }
  }
  let unregister = () => {}
  const dispose = () => {
    if (closed) return
    closed = true
    unregister()
    for (const cleanup of cleanups.reverse()) {
      try {
        cleanup()
      } catch {}
    }
    if (host[ownerKey] === owner) delete host[ownerKey]
  }
  try {
    if (options.fetch) {
      const original = globalThis.fetch
      const wrapped: typeof fetch = function (this: typeof globalThis, input, init) {
        let meta: ReturnType<typeof metadata>
        try {
          meta = metadata(
            input instanceof Request ? input.url : String(input),
            init?.method ?? (input instanceof Request ? input.method : 'GET')
          )
        } catch {}
        const started = performance.now(),
          ctx = context.active()
        const pending = original.call(this, input, init)
        if (!meta) return pending
        const data = meta
        return pending.then(
          (response) => {
            record(
              'http.fetch',
              { ...data, status: response.status, durationMs: performance.now() - started },
              ctx
            )
            return response
          },
          (error: unknown) => {
            record(
              'http.fetch',
              { ...data, outcome: 'failed', durationMs: performance.now() - started },
              ctx
            )
            throw error
          }
        )
      }
      // OTel FetchTransport explicitly bypasses instrumentation via this property.
      Object.defineProperty(wrapped, '__original', {
        value: (original as typeof fetch & { __original?: typeof fetch }).__original ?? original,
      })
      cleanups.push(() => {
        if (globalThis.fetch === wrapped) globalThis.fetch = original
      })
      globalThis.fetch = wrapped
    }
    if (options.xhr) {
      const proto = XMLHttpRequest.prototype,
        originalOpen = proto.open,
        originalSend = proto.send
      const requests = new WeakMap<XMLHttpRequest, ReturnType<typeof metadata>>()
      const listeners = new Map<XMLHttpRequest, () => void>()
      const events = ['load', 'error', 'abort', 'timeout'] as const
      const unlisten = (xhr: XMLHttpRequest, listener: () => void) => {
        for (const event of events) xhr.removeEventListener(event, listener)
      }
      const open = function (this: XMLHttpRequest, ...args: Parameters<typeof originalOpen>) {
        originalOpen.apply(this, args)
        try {
          requests.set(this, metadata(String(args[1]), args[0]))
        } catch {
          requests.delete(this)
        }
      } as typeof originalOpen
      const send: typeof originalSend = function (this: XMLHttpRequest, body) {
        const previous = listeners.get(this)
        if (previous) unlisten(this, previous)
        const meta = requests.get(this),
          started = performance.now(),
          ctx = context.active()
        const done = () => {
          unlisten(this, done)
          listeners.delete(this)
          if (meta)
            record(
              'http.xhr',
              { ...meta, status: this.status, durationMs: performance.now() - started },
              ctx
            )
        }
        if (meta) {
          listeners.set(this, done)
          for (const event of events) this.addEventListener(event, done, { once: true })
        }
        try {
          originalSend.call(this, body)
        } catch (error) {
          unlisten(this, done)
          listeners.delete(this)
          throw error
        }
      }
      cleanups.push(() => {
        if (proto.open === open) proto.open = originalOpen
        if (proto.send === send) proto.send = originalSend
        for (const [xhr, done] of listeners) unlisten(xhr, done)
        listeners.clear()
      })
      proto.open = open
      proto.send = send
    }
    if (options.navigation) {
      const navigate = () => {
        try {
          record('navigation', { url: safeUrl(location.href) })
        } catch {}
      }
      for (const key of ['pushState', 'replaceState'] as const) {
        const original = history[key]
        const wrapped: typeof original = function (this: History, ...args) {
          original.apply(this, args)
          navigate()
        }
        history[key] = wrapped
        cleanups.push(() => {
          if (history[key] === wrapped) history[key] = original
        })
      }
      window.addEventListener('popstate', navigate)
      window.addEventListener('hashchange', navigate)
      cleanups.push(() => {
        window.removeEventListener('popstate', navigate)
        window.removeEventListener('hashchange', navigate)
      })
    }
  } catch (error) {
    dispose()
    throw error
  }
  unregister = client.onDispose(dispose)
  return dispose
}

/** React componentDidCatch / onCaughtError compatible, with no React runtime dependency. */
export function createReactErrorHandler(client: Client, options: CaptureOptions = {}) {
  return (error: unknown, info: { componentStack?: string | null }) => {
    client.captureException(error, {
      ...options,
      handled: true,
      extensions: {
        ...options.extensions,
        'backplane.react': {
          componentStack: typeof info.componentStack === 'string' ? info.componentStack : null,
        },
      },
    })
  }
}
