import { defineConfig } from '@playwright/test';

// @backplane-labs/backplane-errors in a real browser (Chromium): capture,
// breadcrumbs, React errors and the IndexedDB outbox.
export default defineConfig({
  testDir: '.',
  timeout: 90_000, workers: 1, fullyParallel: false,
  use: { browserName: 'chromium', headless: true, actionTimeout: 10_000 },
  webServer: { command: 'npx vite --config tests/errors-browser/vite.config.ts', url: 'http://127.0.0.1:4181', reuseExistingServer: false, cwd: new URL('../..', import.meta.url).pathname },
});
