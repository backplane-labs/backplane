import { resolve } from 'node:path';
import { defineConfig } from 'vite';

// A blank page whose tests import @backplane-labs/backplane-errors from source.
export default defineConfig({
  root: resolve(import.meta.dirname),
  optimizeDeps: { include: ['react', 'react-dom/client'] },
  server: { host: '127.0.0.1', port: 4181, strictPort: true, fs: { allow: [resolve(import.meta.dirname, '../..')] } },
});
