import { IncomingMessage, ServerResponse } from 'node:http';
import type { ProxyOptions } from 'vite';
import { describe, expect, it } from 'vitest';
import { consoleDevServer } from '../apps/console/dev-proxy';

// Minimal stands-in for node streams: the dev proxy's bypass reads only the headers
// and passes the response through untouched.
function request(headers: IncomingMessage['headers']): IncomingMessage {
  const req: IncomingMessage = Object.create(IncomingMessage.prototype);
  req.headers = headers;
  return req;
}
function emptyResponse(): ServerResponse {
  return Object.create(ServerResponse.prototype);
}
function proxyOptionsOf(base: string, target?: string): [Record<string, string | ProxyOptions>, ProxyOptions] {
  const proxy = consoleDevServer(base, target).proxy;
  if (!proxy) throw new Error('Expected the dev server to configure a proxy');
  // Vite allows a proxy entry to be a bare target string; the console configures options.
  const options = Object.values(proxy).find((entry): entry is ProxyOptions => typeof entry === 'object');
  if (!options) throw new Error('Expected a configured proxy entry');
  return [proxy, options];
}
describe('console development proxy', () => {
  it('routes only backend endpoints and leaves app/HMR paths to Vite', () => {
    const [proxy] = proxyOptionsOf('/backplane/');
    const patterns = Object.keys(proxy).map((key) => new RegExp(key));
    const matches = (path: string) => patterns.some((pattern) => pattern.test(path));
    for (const path of [
      '/backplane/auth/login',
      '/backplane/auth/session',
      '/backplane/plugins/hello/hash/plugin.json',
      '/backplane/ws?token=opaque',
    ])
      expect(matches(path)).toBe(true);
    for (const path of [
      '/backplane/services',
      '/backplane/s/hello/settings',
      '/backplane/__vite_hmr',
      '/backplane/ws-other',
      '/backplane/src/main.tsx',
      '/other/auth/login',
    ])
      expect(matches(path)).toBe(false);
  });
  it('rejects foreign HTTP and WebSocket origins before any rewriting', () => {
    const config = consoleDevServer('/backplane/');
    if (!config.proxy) throw new Error('Expected the dev server to configure a proxy');
    expect(config.host).toBe('127.0.0.1');
    expect(config.cors).toBe(false);
    expect(config.strictPort).toBe(true);
    const [, options] = proxyOptionsOf('/backplane/');
    const bypass = options.bypass;
    if (!bypass) throw new Error('Expected the proxy entry to install a bypass');
    for (const response of [undefined, emptyResponse()])
      for (const origin of ['null', 'http://evil.test', 'http://127.0.0.1:8081', 'http://localhost:5173']) {
        const req = request({ host: '127.0.0.1:5173', origin });
        expect(bypass(req, response, options)).toBe(false);
        expect(req.headers.origin).toBe(origin);
      }
    expect(bypass(request({ host: '127.0.0.1:5173' }), undefined, options)).toBe(false);
    expect(bypass(request({ host: '127.0.0.1:5173', 'sec-fetch-site': 'cross-site' }), emptyResponse(), options)).toBe(
      false,
    );
  });
  it('preserves cookies and rewrites a verified origin for backend origin checks', () => {
    const [, options] = proxyOptionsOf('/backplane/', 'https://platform.example');
    const bypass = options.bypass;
    if (!bypass) throw new Error('Expected the proxy entry to install a bypass');
    for (const response of [undefined, emptyResponse()]) {
      const req = request({
        host: '127.0.0.1:5173',
        origin: 'http://127.0.0.1:5173',
        cookie: 'bp_session=fixture',
        'sec-websocket-protocol': 'ws-proto',
      });
      expect(bypass(req, response, options)).toBeUndefined();
      expect(req.headers.origin).toBe('https://platform.example');
      expect(req.headers.cookie).toBe('bp_session=fixture');
      expect(req.headers['sec-websocket-protocol']).toBe('ws-proto');
    }
  });
  it('validates configuration and escapes custom prefixes', () => {
    for (const target of [
      'file:///tmp',
      'http://user:secret@host',
      'http://host/path',
      'http://host?x=1',
      'http://host/#x',
    ])
      expect(() => consoleDevServer('/backplane/', target)).toThrow();
    for (const base of ['backplane/', '//host/', '/backplane', '/backplane/?x', '/backplane\\/'])
      expect(() => consoleDevServer(base)).toThrow();
    const [proxy] = proxyOptionsOf('/console.v1/');
    const pattern = new RegExp(Object.keys(proxy)[0] ?? '');
    expect(pattern.test('/console.v1/auth/login')).toBe(true);
    expect(pattern.test('/consoleXv1/auth/login')).toBe(false);
  });
});
