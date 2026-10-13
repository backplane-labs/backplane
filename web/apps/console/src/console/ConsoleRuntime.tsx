import type { ComponentProps } from 'react';
import Console from './Console';
import { useRegistry } from './registry';

// Keep catalog subscriptions outside the shell's Fast Refresh boundary. Refresh
// invalidates hook memoization, which would otherwise clear the catalog briefly
// and unmount module pages (including their unsaved drafts).
export default function ConsoleRuntime(props: Omit<ComponentProps<typeof Console>, 'registry'>) {
  const registry = useRegistry();
  return <Console {...props} registry={registry} />;
}
