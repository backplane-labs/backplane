import { afterEach, expect, test, vi } from 'vitest';
import { create, toBinary } from '@bufbuild/protobuf';
import { CatalogServiceClient, ListServicesRequestSchema, ListServicesResponseSchema, RevisionSchema } from '@backplane-labs/backplane-api';
import { BackplaneClient, SessionHttpError, type ObservableSocket } from '../packages/client/src/index';
import { FakeSocket, Kind } from '@gopherex/ws-proto-transport';

class Socket extends FakeSocket implements ObservableSocket {
  private events = new Map<string, Array<() => void>>();
  constructor() { super({ autoOpen: false }); }
  addEventListener(type: string, listener: () => void) {
    this.events.set(type, [...this.events.get(type) ?? [], listener]);
  }
  override open() { super.open(); this.events.get('open')?.forEach((listener) => listener()); }
  override drop() { super.drop(); this.events.get('close')?.forEach((listener) => listener()); }
}

const session = { id: 'session-1', created_at: '2026-09-29T00:00:00Z', expires_at: '2026-09-30T00:00:00Z', idle_timeout_seconds: 3600 };
const clients: BackplaneClient[] = [];
afterEach(() => { clients.forEach((client) => client.dispose()); clients.length = 0; vi.useRealTimers(); });
function setup(fetcher = vi.fn<typeof fetch>().mockImplementation(async () => Response.json(session))) {
  const sockets: Socket[] = [];
  const urls: string[] = [];
  const client = new BackplaneClient({
    baseURL: 'https://console.example/backplane', fetch: fetcher,
    createSocket: (url, protocol) => {
      expect(protocol).toBe('wsrpc.v1'); urls.push(url);
      const socket = new Socket(); sockets.push(socket); return socket;
    },
  });
  clients.push(client);
  return { client, sockets, urls, fetcher };
}

test('API files use the console prefix, cookies and abort signal', async () => {
  const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response('openapi: 3.0.3'));
  const { client } = setup(fetcher), signal = new AbortController().signal, hash = 'a'.repeat(64);
  expect(new TextDecoder().decode(await client.readSchemaFile('hello', hash, 'schemas/a b.yaml', signal))).toBe('openapi: 3.0.3');
  expect(String(fetcher.mock.calls[0]?.[0])).toBe(`https://console.example/backplane/schemas/hello/${hash}/schemas/a%20b.yaml`);
  expect(fetcher.mock.calls[0]?.[1]).toMatchObject({ credentials: 'include', redirect: 'error', cache: 'no-store', signal });
  await expect(client.readSchemaFile('hello', hash, '../secret', signal)).rejects.toThrow('Invalid API bundle path');
  expect(fetcher).toHaveBeenCalledTimes(1);
});

test('login uses cookie endpoint under console prefix; logout closes active calls', async () => {
  const { client, sockets, urls, fetcher } = setup();
  await client.login('operator-token'); sockets[0].open();
  expect(urls).toEqual(['wss://console.example/backplane/ws']);
  expect(String(fetcher.mock.calls[0][0])).toBe('https://console.example/backplane/auth/login');
  expect(fetcher.mock.calls[0][1]).toMatchObject({ method: 'POST', credentials: 'same-origin', body: '{"token":"operator-token"}' });
  const api = client.client(CatalogServiceClient);
  const pending = api.listServices(create(ListServicesRequestSchema));
  const rejected = expect(pending).rejects.toBeDefined();
  fetcher.mockResolvedValueOnce(new Response(null, { status: 204 }));
  await client.logout(); await rejected;
  expect(client.getSnapshot()).toMatchObject({ connection: 'anonymous', session: null });
  expect(() => api.listServices(create(ListServicesRequestSchema))).toThrow('Console is not connected');
});

test.each([429, 500, 503])('HTTP %i preserves session and honors Retry-After', async (status) => {
  vi.useFakeTimers();
  const { client, sockets, fetcher } = setup();
  await client.start(); sockets[0].open();
  fetcher.mockResolvedValueOnce(new Response(null, { status, headers: { 'retry-after': '60' } }));
  await client.start();
  expect(client.getSnapshot().session?.id).toBe(session.id);
  expect(client.getSnapshot().connection).toBe('connected');
  await vi.advanceTimersByTimeAsync(59_999);
  expect(fetcher).toHaveBeenCalledTimes(2);
  await vi.advanceTimersByTimeAsync(2);
  expect(fetcher).toHaveBeenCalledTimes(3);
});

test('network failure retains session; only confirmed 401 clears it', async () => {
  const { client, sockets, fetcher } = setup();
  await client.start(); sockets[0].open();
  fetcher.mockRejectedValueOnce(new TypeError('network unavailable'));
  await client.start();
  expect(client.getSnapshot().session?.id).toBe(session.id);
  fetcher.mockResolvedValueOnce(new Response(null, { status: 401 }));
  await client.start();
  expect(client.getSnapshot()).toMatchObject({ session: null, connection: 'anonymous' });
});

test('dropped calls fail once and are not replayed after reconnect', async () => {
  vi.useFakeTimers();
  const { client, sockets } = setup();
  await client.start(); sockets[0].open();
  const api = client.client(CatalogServiceClient);
  const first = api.listServices(create(ListServicesRequestSchema));
  const rejection = expect(first).rejects.toBeDefined();
  sockets[0].drop(); await rejection;
  await vi.advanceTimersByTimeAsync(31_000);
  expect(sockets).toHaveLength(2); sockets[1].open();
  expect(sockets[1].sent).toHaveLength(0);
  const next = api.listServices(create(ListServicesRequestSchema));
  const open = sockets[1].sent.find((frame) => frame.kind === Kind.KIND_OPEN)!;
  sockets[1].inject({ streamId: open.streamId, kind: Kind.KIND_MSG, payload: toBinary(ListServicesResponseSchema, create(ListServicesResponseSchema)) });
  sockets[1].inject({ streamId: open.streamId, kind: Kind.KIND_END });
  await expect(next).resolves.toMatchObject({ services: [] });
  expect(client.getSnapshot().connectionId).toBe(2);
});

test('failed logout does not falsely report that the server session was revoked', async () => {
  const { client, fetcher } = setup();
  await client.start(); fetcher.mockResolvedValueOnce(new Response(null, { status: 503 }));
  await expect(client.logout()).rejects.toBeInstanceOf(SessionHttpError);
  expect(client.getSnapshot().session?.id).toBe(session.id);
});

test('a late session response cannot resurrect a disposed client', async () => {
  let resolve!: (response: Response) => void;
  const fetcher = vi.fn<typeof fetch>(() => new Promise((done) => { resolve = done; }));
  const { client, sockets } = setup(fetcher);
  const pending = client.start(); client.dispose(); resolve(Response.json(session));
  await pending;
  expect(client.getSnapshot().connection).toBe('disposed'); expect(sockets).toHaveLength(0);
});

test('generated API preserves revision numbers beyond the JavaScript safe range', async () => {
  const { fromBinary, toJson } = await import('@bufbuild/protobuf');
  const value = create(RevisionSchema, { service: 'hello', revision: 9_007_199_254_740_993n });
  expect(fromBinary(RevisionSchema, toBinary(RevisionSchema, value))).toEqual(value);
  expect(toJson(RevisionSchema, value)).toMatchObject({ revision: '9007199254740993' });
});
