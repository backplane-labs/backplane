import {
  type AnyRequest,
  type AnyResponse,
  CODE_UNAVAILABLE,
  SUBPROTOCOL,
  type WebSocketLike,
  WsStatusError,
  WsTransport,
} from '@gopherex/ws-proto-transport';

export { WsStatusError } from '@gopherex/ws-proto-transport';
export { type WatchRetryOptions, watchWithRetry } from './watch.js';

export interface Session {
  id: string;
  created_at: string;
  expires_at: string;
  idle_timeout_seconds: number;
}

/** Shape the session endpoints return before validation. */
interface SessionPayload {
  id?: string;
  created_at?: string;
  expires_at?: string;
  idle_timeout_seconds?: number;
}

export type ConnectionState =
  | 'idle'
  | 'checking'
  | 'anonymous'
  | 'connecting'
  | 'connected'
  | 'reconnecting'
  | 'offline'
  | 'disposed';
export interface ClientState {
  readonly connection: ConnectionState;
  readonly session: Session | null;
  /** Increments whenever a fresh WebSocket opens. Snapshot watches refetch. */
  readonly connectionId: number;
  readonly error?: Error;
}

export class SessionHttpError extends Error {
  constructor(
    public readonly status: number,
    public readonly retryAfterMs: number | undefined,
  ) {
    super(`Session request failed (${status})`);
    this.name = 'SessionHttpError';
  }
}

export interface ObservableSocket extends WebSocketLike {
  addEventListener(type: 'open' | 'close' | 'error', listener: () => void): void;
}

/**
 * Adapts the browser WebSocket to ObservableSocket. The DOM handler property
 * types (MessageEvent etc.) are not assignable to the minimal WebSocketLike
 * handler types, so the events are rewrapped at this boundary.
 */
class BrowserSocket implements ObservableSocket {
  private readonly socket: WebSocket;
  onmessage: WebSocketLike['onmessage'] = null;
  onopen: WebSocketLike['onopen'] = null;
  onclose: WebSocketLike['onclose'] = null;
  onerror: WebSocketLike['onerror'] = null;

  constructor(url: string, protocol: string) {
    this.socket = new WebSocket(url, protocol);
    this.socket.onmessage = (event) => {
      this.onmessage?.({ data: event.data });
    };
    this.socket.onopen = () => {
      this.onopen?.(undefined);
    };
    this.socket.onclose = () => {
      this.onclose?.(undefined);
    };
    this.socket.onerror = () => {
      this.onerror?.(undefined);
    };
  }

  get binaryType(): string {
    return this.socket.binaryType;
  }

  set binaryType(value: string) {
    if (value !== 'blob' && value !== 'arraybuffer') throw new Error(`Unsupported binaryType ${value}`);
    this.socket.binaryType = value;
  }

  send(data: ArrayBufferView | ArrayBuffer): void {
    this.socket.send(data);
  }

  close(code?: number, reason?: string): void {
    this.socket.close(code, reason);
  }

  get protocol(): string {
    return this.socket.protocol;
  }

  addEventListener(type: 'open' | 'close' | 'error', listener: () => void): void {
    this.socket.addEventListener(type, () => listener());
  }
}
export interface ClientOptions {
  /** Console base URL including any prefix. Defaults to the current origin. */
  baseURL?: string;
  fetch?: typeof globalThis.fetch;
  createSocket?: (url: string, protocol: string) => ObservableSocket;
}
export type ClientConstructor<T extends object> = new (transport: WsTransport) => T;

/** Also implemented by explicit standalone fixtures supplied by module authors. */
export interface ClientRuntime {
  getSnapshot(): ClientState;
  subscribe(listener: () => void): () => void;
  client<T extends object>(clientConstructor: ClientConstructor<T>): T;
}

/** A session owns a transport. Reconnection never replays a completed/failed RPC. */
export class BackplaneClient {
  private state: ClientState = { connection: 'idle', session: null, connectionId: 0 };
  private readonly listeners = new Set<() => void>();
  private readonly base: URL;
  private readonly fetcher: typeof globalThis.fetch;
  private readonly socketFactory: NonNullable<ClientOptions['createSocket']>;
  private transport?: WsTransport;
  /** Generated clients run over this paused transport; its interceptor sends every call over the current session's one. */
  private readonly forwarding = this.forwarder();
  private epoch = 0;
  private controller = new AbortController();
  private refreshInFlight?: Promise<void>;
  private retry?: ReturnType<typeof setTimeout>;
  private attempt = 0;
  private disposed = false;

  constructor(options: ClientOptions = {}) {
    const base = options.baseURL ?? (typeof location !== 'undefined' ? location.origin : undefined);
    if (!base) throw new Error('baseURL is required outside a browser');
    this.base = new URL(base.endsWith('/') ? base : `${base}/`);
    if (
      !['http:', 'https:'].includes(this.base.protocol) ||
      this.base.username ||
      this.base.password ||
      this.base.search ||
      this.base.hash
    ) {
      throw new Error('baseURL must be an HTTP(S) console URL without credentials, query or fragment');
    }
    this.fetcher = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.socketFactory = options.createSocket ?? ((url, protocol) => new BrowserSocket(url, protocol));
  }

  getSnapshot = (): ClientState => this.state;
  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  private update(patch: Partial<ClientState>) {
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener();
  }

  private assertActive() {
    if (this.disposed) throw new Error('BackplaneClient is disposed');
  }

  private async request(path: string, init: RequestInit = {}) {
    const response = await this.fetcher(new URL(path, this.base), {
      ...init,
      credentials: 'same-origin',
      cache: 'no-store',
      signal: this.controller.signal,
    });
    if (!response.ok) {
      const retry = response.headers.get('retry-after');
      const seconds = retry && /^\d+(?:\.\d+)?$/.test(retry) ? Number(retry) * 1000 : undefined;
      const date = retry ? Date.parse(retry) - Date.now() : NaN;
      throw new SessionHttpError(response.status, seconds ?? (Number.isFinite(date) ? Math.max(0, date) : undefined));
    }
    return response;
  }

  private parseSession(value: SessionPayload | null): Session {
    if (
      !value ||
      typeof value.id !== 'string' ||
      !value.id ||
      typeof value.created_at !== 'string' ||
      typeof value.expires_at !== 'string' ||
      typeof value.idle_timeout_seconds !== 'number'
    ) {
      throw new Error('Invalid session response');
    }
    return {
      id: value.id,
      created_at: value.created_at,
      expires_at: value.expires_at,
      idle_timeout_seconds: value.idle_timeout_seconds,
    };
  }

  /** Rechecks cookies; an outage preserves the previous session and retries. */
  start(): Promise<void> {
    this.assertActive();
    if (this.refreshInFlight !== undefined) return this.refreshInFlight;
    const epoch = this.epoch;
    if (!this.state.session) this.update({ connection: 'checking', error: undefined });
    const work = this.refresh(epoch);
    this.refreshInFlight = work;
    void work.finally(() => {
      if (this.refreshInFlight === work) this.refreshInFlight = undefined;
    });
    return work;
  }

  private refresh(epoch: number): Promise<void> {
    // The rejection handler types the caught value; a try/catch would leave it untyped.
    return (async () => {
      const session = this.parseSession(await (await this.request('auth/session')).json());
      if (this.epoch !== epoch || this.disposed) return;
      this.attempt = 0;
      if (this.retry) {
        clearTimeout(this.retry);
        this.retry = undefined;
      }
      // Cookie replacement in another tab must terminate old in-flight work.
      if (this.state.session && this.state.session.id !== session.id) this.closeTransport();
      this.update({ session, error: undefined });
      this.connect();
    })().catch((cause) => {
      if (this.epoch !== epoch || this.disposed) return;
      const error = cause instanceof Error ? cause : new Error(String(cause));
      if (error instanceof SessionHttpError && error.status === 401) {
        this.resetSession();
        this.update({ connection: 'anonymous', session: null, error: undefined });
      } else {
        this.update({ connection: this.state.connection === 'connected' ? 'connected' : 'offline', error });
        if (!(error instanceof SessionHttpError) || error.status === 429 || error.status >= 500) {
          this.scheduleRefresh(error instanceof SessionHttpError ? error : undefined);
        }
      }
    });
  }

  private scheduleRefresh(error: SessionHttpError | undefined) {
    if (this.retry) clearTimeout(this.retry);
    const jitter = 250 + Math.random() * Math.min(30_000, 500 * 2 ** Math.min(this.attempt++, 8));
    const retryAfter = error instanceof SessionHttpError ? (error.retryAfterMs ?? 0) : 0;
    this.retry = setTimeout(
      () => {
        this.retry = undefined;
        void this.start();
      },
      Math.min(2_147_483_647, Math.max(jitter, retryAfter)),
    );
  }

  async login(token: string): Promise<void> {
    this.assertActive();
    const epoch = this.epoch;
    const response = await this.request('auth/login', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ token }),
    });
    const session = this.parseSession(await response.json());
    if (this.epoch !== epoch || this.disposed) return;
    this.resetSession();
    this.update({ session, connection: 'connecting', error: undefined });
    this.connect();
  }

  async logout(): Promise<void> {
    this.assertActive();
    const epoch = this.epoch;
    try {
      await this.request('auth/logout', { method: 'POST' });
    } catch (error) {
      if (!(error instanceof SessionHttpError && error.status === 401)) throw error;
    }
    if (this.epoch !== epoch || this.disposed) return;
    this.resetSession();
    this.update({ session: null, connection: 'anonymous', error: undefined });
  }

  private connect() {
    if (this.transport || !this.state.session || this.disposed) return;
    const epoch = this.epoch;
    const url = new URL('ws', this.base);
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
    this.update({ connection: 'connecting' });
    this.transport = new WsTransport(url.href, {
      reconnect: true,
      maxFrameBytes: 16 * 1024 * 1024,
      createSocket: (address) => {
        const socket = this.socketFactory(address, SUBPROTOCOL);
        socket.addEventListener('open', () => {
          if (this.epoch === epoch && !this.disposed)
            this.update({ connection: 'connected', connectionId: this.state.connectionId + 1, error: undefined });
        });
        let dropped = false;
        const drop = () => {
          if (dropped || this.epoch !== epoch || this.disposed) return;
          dropped = true;
          this.update({ connection: 'reconnecting' });
          void this.start();
        };
        socket.addEventListener('close', drop);
        socket.addEventListener('error', drop);
        return socket;
      },
    });
  }

  /** Stable generated client; each new call uses the current session. */
  client<T extends object>(clientConstructor: ClientConstructor<T>): T {
    return new clientConstructor(this.forwarding);
  }

  /** Calls from this loaded module use its bundle as a replica preference, never as authorization. */
  forBundle(service: string, hash: string): ClientRuntime {
    const forwarding = this.forwarder(`${service}/${hash}`);
    return {
      getSnapshot: this.getSnapshot,
      subscribe: this.subscribe,
      client: (clientConstructor) => new clientConstructor(forwarding),
    };
  }

  private forwarder(bundle?: string): WsTransport {
    return new WsTransport('wss://forwarding.invalid/', {
      paused: true,
      interceptors: [() => (request) => this.forward(request, bundle)],
    });
  }

  private forward(request: AnyRequest, bundle?: string): Promise<AnyResponse> {
    this.assertActive();
    const current = this.transport;
    if (!current || this.state.connection !== 'connected')
      return Promise.reject(new WsStatusError(CODE_UNAVAILABLE, 'Console is not connected'));
    const headers =
      bundle && !request.method.typeName.startsWith('backplane.')
        ? { ...request.header, 'x-backplane-ui-bundle': bundle }
        : request.header;
    const call = { headers, signal: request.signal, timeoutMs: request.timeoutMs };
    return request.stream
      ? current.stream(request.method, request.message, call)
      : current.unary(request.method, request.message, call);
  }

  private closeTransport() {
    // Invalidate socket listeners before close fires.
    this.epoch++;
    this.transport?.close();
    this.transport = undefined;
  }

  private resetSession() {
    this.closeTransport();
    this.controller.abort();
    this.controller = new AbortController();
    this.refreshInFlight = undefined;
    if (this.retry) clearTimeout(this.retry);
    this.retry = undefined;
    this.attempt = 0;
  }

  dispose() {
    if (this.disposed) return;
    this.disposed = true;
    this.resetSession();
    this.update({ connection: 'disposed', session: null, error: undefined });
    this.listeners.clear();
    this.forwarding.close();
  }
}
