import { useCallback, useEffect, useRef, useState } from 'react';
import { create } from '@bufbuild/protobuf';
import { CatalogServiceClient, ListPluginsRequestSchema, WatchCatalogRequestSchema } from '@gopherex/backplane-api';
import { useClient, useSnapshotWatch } from '@gopherex/backplane-react';
import { usePlatformQuery } from '@gopherex/backplane-platform-ui';
import { loadPlugin, type LoadedPlugin } from '@gopherex/backplane-plugin-sdk/host';
import { installPluginTranslations } from '@gopherex/backplane-plugin-sdk';
import { useTranslation } from 'react-i18next';

export type ModuleEntry = { plugin?: LoadedPlugin; error?: string };
export function useRegistry() {
  const client = useClient(CatalogServiceClient), { i18n } = useTranslation();
  const catalog = useSnapshotWatch(useCallback((signal: AbortSignal) => client.watchCatalog(create(WatchCatalogRequestSchema), { signal }), [client]));
  const descriptors = usePlatformQuery(`plugins:${catalog.value?.index}`, (signal) => client.listPlugins(create(ListPluginsRequestSchema), { signal }));
  const cache = useRef(new Map<string, LoadedPlugin>());
  const [entries, setEntries] = useState<Record<string, ModuleEntry>>({});
  const [attempt, setAttempt] = useState(0);
  const available = descriptors.value?.plugins.filter((plugin) => plugin.available) ?? [];
  const signature = JSON.stringify(available);
  useEffect(() => {
    const controller = new AbortController();
    const descriptors: typeof available = JSON.parse(signature);
    setEntries(Object.fromEntries(descriptors.map((descriptor) => [JSON.stringify(descriptor), { plugin: cache.current.get(JSON.stringify(descriptor)) }])));
    for (const descriptor of descriptors) {
      const key = JSON.stringify(descriptor);
      if (cache.current.has(key)) continue;
      void loadPlugin(descriptor, { consoleURL: new URL(import.meta.env.BASE_URL, location.origin).href, signal: controller.signal }).then((plugin) => {
        if (controller.signal.aborted) return;
        installPluginTranslations(i18n, plugin.navigation);
        cache.current.set(key, plugin);
        setEntries((old) => ({ ...old, [key]: { plugin } }));
      }, (reason: unknown) => {
        if (!controller.signal.aborted) setEntries((old) => ({ ...old, [key]: { error: reason instanceof Error ? reason.message : String(reason) } }));
      });
    }
    return () => controller.abort();
  }, [signature, i18n, attempt]);
  // Filter synchronously: a departed module cannot render for an extra effect cycle.
  const modules = Object.fromEntries(available.map((descriptor) => [descriptor.service, entries[JSON.stringify(descriptor)] ?? {}]));
  return { catalog, descriptors, modules, retry: () => { descriptors.refresh(); setAttempt((value) => value + 1); } };
}
