import type { ProxyOptions, ServerOptions } from 'vite';

/** Same-origin browser access to the existing installation, only in Vite serve. */
export function consoleDevServer(base: string, upstream = 'http://127.0.0.1:8081'): ServerOptions {
  const target = new URL(upstream);
  if (!['http:', 'https:'].includes(target.protocol) || target.username || target.password || target.pathname !== '/' || target.search || target.hash) {
    throw new Error('BACKPLANE_DEV_TARGET must be an HTTP(S) origin without credentials or a path');
  }
  if (!base.startsWith('/') || base.startsWith('//') || !base.endsWith('/') || /[?#\\]/.test(base)) {
    throw new Error('BACKPLANE_CONSOLE_BASE must be an absolute path ending in /');
  }
  const prefix = base.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const proxy: ProxyOptions = {
    target: target.origin,
    changeOrigin: true,
    // Vite invokes bypass for both HTTP and WS before contacting the upstream.
    // Validate the browser origin BEFORE rewriting it for the upstream's own
    // CSRF/WS checks. Do not enable an unconditional rewriteWsOrigin.
    bypass(req, res) {
      const origin = req.headers.origin;
      if ((origin && origin !== `http://${req.headers.host}`) || (!res && !origin) || req.headers['sec-fetch-site'] === 'cross-site') return false;
      if (origin) req.headers.origin = target.origin;
    },
  };
  return {
    host: '127.0.0.1', port: 5173, strictPort: true, cors: false,
    hmr: { path: '__vite_hmr' },
    proxy: {
      [`^${prefix}auth(?:/|$)`]: { ...proxy },
      [`^${prefix}plugins(?:/|$)`]: { ...proxy },
      [`^${prefix}ws(?:\\?|$)`]: { ...proxy, ws: true },
    },
  };
}
