import type { BackplaneClient } from '@backplane-labs/backplane-client';
import type { ComponentProps } from 'react';
import { Console } from './Console';
import { useRegistry } from './registry';

// Keep catalog subscriptions outside the shell's Fast Refresh boundary. Refresh
// invalidates hook memoization, which would otherwise clear the catalog briefly
// and unmount module pages (including their unsaved drafts).
export function ConsoleRuntime({
  client,
  ...props
}: Omit<ComponentProps<typeof Console>, 'registry' | 'onLogout'> & { client: BackplaneClient }) {
  const registry = useRegistry(client);
  return <Console {...props} onLogout={() => client.logout()} registry={registry} />;
}
