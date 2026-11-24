import { expect, test } from 'vitest';
import { isServiceTab } from '../apps/console/src/console/service/tabs';

test.each(['automation', 'workflows', 'operations', 'events', 'api'])('does not route service traffic to %s', (tab) => {
  expect(isServiceTab(tab)).toBe(false);
});

test.each(['overview', 'configuration', 'telemetry', 'errors', 'audit'])('keeps the operator view %s', (tab) => {
  expect(isServiceTab(tab)).toBe(true);
});
