import {
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
  readonly error?: unknown;
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
  client<T extends object>(constructor: ClientConstructor<T>): T;
  /** Optional for standalone fixtures that do not provide API file bundles. */
  readSchemaFile?(service: string, hash: string, file: string, signal?: AbortSignal): Promise<Uint8Array>;
}

/** A session owns a transport. Reconnection never replays a completed/failed RPC. */
export class BackplaneClient {
  /** Fetch through the authenticated console base, including its deployment prefix. */
  async readSchemaFile(service: string, hash: string, file: string, signal?: AbortSignal): Promise<Uint8Array> {
    if (!/^[a-f0-9]{64}$/.test(hash) || !file || file.split('/').some((part) => !part || part === '.' || part === '..'))
      throw new Error('Invalid API bundle path');
    const path = ['schemas', service, hash, ...file.split('/')].map(encodeURIComponent).join('/');
    const response = await this.fetcher(new URL(path, this.base), {
      credentials: 'include',
      redirect: 'error',
      cache: 'no-store',
      signal,
    });
    if (!response.ok) throw new SessionHttpError(response.status, undefined);
    const bytes = new Uint8Array(await response.arrayBuffer());
    if (bytes.length > 32 * 1024 * 1024) throw new Error('API document is too large');
    return bytes;
  }
  private state: ClientState = { connection: 'idle', session: null, connectionId: 0 };
  private readonly listeners = new Set<() => void>();
  private readonly base: URL;
  private readonly fetcher: typeof globalThis.fetch;
  private readonly socketFactory: NonNullable<ClientOptions['createSocket']>;
  private transport?: WsTransport;
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
    this.socketFactory =
      options.createSocket ?? ((url, protocol) => new WebSocket(url, protocol) as unknown as ObservableSocket);
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

  private parseSession(value: unknown): Session {
    const session = value as Partial<Session> | null;
    if (
      !session ||
      typeof session.id !== 'string' ||
      !session.id ||
      typeof session.created_at !== 'string' ||
      typeof session.expires_at !== 'string' ||
      typeof session.idle_timeout_seconds !== 'number'
    ) {
      throw new Error('Invalid session response');
    }
    return session as Session;
  }

  /** Rechecks cookies; an outage preserves the previous session and retries. */
  start(): Promise<void> {
    this.assertActive();
    if (this.refreshInFlight) return this.refreshInFlight;
    const epoch = this.epoch;
    if (!this.state.session) this.update({ connection: 'checking', error: undefined });
    const work = this.refresh(epoch);
    this.refreshInFlight = work;
    void work.finally(() => {
      if (this.refreshInFlight === work) this.refreshInFlight = undefined;
    });
    return work;
  }

  private async refresh(epoch: number) {
    try {
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
    } catch (error) {
      if (this.epoch !== epoch || this.disposed) return;
      if (error instanceof SessionHttpError && error.status === 401) {
        this.resetSession();
        this.update({ connection: 'anonymous', session: null, error: undefined });
      } else {
        this.update({ connection: this.state.connection === 'connected' ? 'connected' : 'offline', error });
        if (!(error instanceof SessionHttpError) || error.status === 429 || error.status >= 500) {
          this.scheduleRefresh(error);
        }
      }
    }
  }

  private scheduleRefresh(error: unknown) {
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

  /** Stable generated-client facade; each new call uses the current session. */
  client<T extends object>(constructor: ClientConstructor<T>): T {
    let transport: WsTransport | undefined;
    let instance: T | undefined;
    return new Proxy({} as T, {
      get: (_target, key) => {
        if (key === 'then') return undefined;
        return (...args: unknown[]) => {
          this.assertActive();
          if (!this.transport || this.state.connection !== 'connected')
            throw new WsStatusError(CODE_UNAVAILABLE, 'Console is not connected');
          if (transport !== this.transport) {
            transport = this.transport;
            instance = new constructor(transport);
          }
          const method = Reflect.get(instance!, key);
          if (typeof method !== 'function') throw new TypeError(`Unknown client method ${String(key)}`);
          return Reflect.apply(method, instance, args);
        };
      },
    });
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
  }
}
