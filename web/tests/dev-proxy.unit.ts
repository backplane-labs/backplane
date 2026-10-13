import { describe, expect, it } from 'vitest';
import type { IncomingMessage, ServerResponse } from 'node:http';
import type { ProxyOptions } from 'vite';
import { consoleDevServer } from '../apps/embedding/dev-proxy';

function request(headers: IncomingMessage['headers']) { return { headers } as IncomingMessage; }
describe('console development proxy', () => {
  it('routes only backend endpoints and leaves app/HMR paths to Vite', () => {
    const config = consoleDevServer('/backplane/');
    const patterns = Object.keys(config.proxy!).map((key) => new RegExp(key));
    const matches = (path: string) => patterns.some((pattern) => pattern.test(path));
    for (const path of ['/backplane/auth/login', '/backplane/auth/session', '/backplane/plugins/hello/hash/plugin.json', '/backplane/ws?token=opaque']) expect(matches(path)).toBe(true);
    for (const path of ['/backplane/services', '/backplane/s/hello/settings', '/backplane/__vite_hmr', '/backplane/ws-other', '/backplane/src/main.tsx', '/other/auth/login']) expect(matches(path)).toBe(false);
    expect(config.host).toBe('127.0.0.1'); expect(config.cors).toBe(false); expect(config.strictPort).toBe(true);
  });
  it('rejects foreign HTTP and WebSocket origins before any rewriting', () => {
    const config = consoleDevServer('/backplane/');
    const options = Object.values(config.proxy!)[0] as ProxyOptions;
    for (const response of [undefined, {} as ServerResponse]) for (const origin of ['null', 'http://evil.test', 'http://127.0.0.1:8081', 'http://localhost:5173']) {
      const req = request({ host: '127.0.0.1:5173', origin });
      expect(options.bypass!(req, response, options)).toBe(false);
      expect(req.headers.origin).toBe(origin);
    }
    expect(options.bypass!(request({ host: '127.0.0.1:5173' }), undefined, options)).toBe(false);
    expect(options.bypass!(request({ host: '127.0.0.1:5173', 'sec-fetch-site': 'cross-site' }), {} as ServerResponse, options)).toBe(false);
  });
  it('preserves cookies and rewrites a verified origin for backend origin checks', () => {
    const options = Object.values(consoleDevServer('/backplane/', 'https://platform.example').proxy!)[0] as ProxyOptions;
    for (const response of [undefined, {} as ServerResponse]) {
      const req = request({ host: '127.0.0.1:5173', origin: 'http://127.0.0.1:5173', cookie: 'bp_session=fixture', 'sec-websocket-protocol': 'ws-proto' });
      expect(options.bypass!(req, response, options)).toBeUndefined();
      expect(req.headers.origin).toBe('https://platform.example');
      expect(req.headers.cookie).toBe('bp_session=fixture');
      expect(req.headers['sec-websocket-protocol']).toBe('ws-proto');
    }
  });
  it('validates configuration and escapes custom prefixes', () => {
    for (const target of ['file:///tmp', 'http://user:secret@host', 'http://host/path', 'http://host?x=1', 'http://host/#x']) expect(() => consoleDevServer('/backplane/', target)).toThrow();
    for (const base of ['backplane/', '//host/', '/backplane', '/backplane/?x', '/backplane\\/']) expect(() => consoleDevServer(base)).toThrow();
    const pattern = new RegExp(Object.keys(consoleDevServer('/console.v1/').proxy!)[0]);
    expect(pattern.test('/console.v1/auth/login')).toBe(true); expect(pattern.test('/consoleXv1/auth/login')).toBe(false);
  });
});
