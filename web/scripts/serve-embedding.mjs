import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { resolve, extname, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
// Read the actual Go constant so browser acceptance cannot quietly relax CSP.
const go = await readFile(resolve(root, '../internal/console/http.go'), 'utf8');
const declaration = go.match(/contentSecurityPolicy = ((?:"[^"\n]*"\s*\+\s*)*"[^"\n]*")/)[1];
const csp = [...declaration.matchAll(/"([^"]*)"/g)].map((part) => part[1]).join('');
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.json': 'application/json', '.css': 'text/css', '.svg': 'image/svg+xml', '.woff2': 'font/woff2' };
const mounts = [
  ['/backplane/plugins/hello/fixture-v1/', process.env.BACKPLANE_PLUGIN_DIR ?? resolve(root, 'templates/module/dist/plugin')],
  ['/backplane/', process.env.BACKPLANE_HOST_DIR ?? resolve(root, 'tests/fixtures/embedding/dist')],
];
createServer(async (request, response) => {
  response.setHeader('Content-Security-Policy', csp);
  response.setHeader('X-Content-Type-Options', 'nosniff');
  try {
    const path = decodeURIComponent(new URL(request.url, 'http://localhost').pathname);
    const mount = mounts.find(([prefix]) => path.startsWith(prefix));
    if (!mount) { response.writeHead(404).end(); return; }
    const [prefix, directory] = mount;
    const relative = path.slice(prefix.length);
    const file = resolve(directory, relative);
    if (file !== directory && !file.startsWith(directory + sep)) { response.writeHead(400).end(); return; }
    let body, target = file;
    try { body = await readFile(file); } catch {
      if (prefix.includes('/plugins/') || extname(relative)) { response.writeHead(404).end(); return; }
      target = resolve(directory, 'index.html');
      body = await readFile(target);
    }
    response.setHeader('Content-Type', mime[extname(target)] ?? 'application/octet-stream');
    response.end(body);
  } catch { response.writeHead(500).end(); }
}).listen(4180, '127.0.0.1');
