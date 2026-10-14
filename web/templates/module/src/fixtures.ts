import { GetStatsResponseSchema } from './gen/proto/hello/console/v1/admin_pb';
import { create } from '@bufbuild/protobuf';
import { CatalogServiceClient, ListServicesResponseSchema } from '@backplane-labs/backplane-api';
import type { ClientRuntime, ClientConstructor, ClientState } from '@backplane-labs/backplane-client';

const state: ClientState = { connection: 'connected', session: null, connectionId: 1 };
/** Explicit local fixture. Replace with BackplaneClient or your own adapter. */
export const fixtureClient: ClientRuntime = {
  getSnapshot: () => state, subscribe: () => () => {},
  client<T extends object>(constructor: ClientConstructor<T>): T {
    if ('getStats' in constructor.prototype) return { async getStats() { return create(GetStatsResponseSchema, { greetings: 9007199254740993n, audited: 9007199254740993n, currentGreeting: 'Hello', cachePresent: true }); } } as unknown as T;
    if (constructor !== CatalogServiceClient) throw new Error(`No fixture for ${constructor.name}`);
    return { async listServices() { return create(ListServicesResponseSchema, { services: [
      { name: 'hello', instances: 1 }, { name: 'formatter', instances: 1 },
    ] }); } } as unknown as T;
  },
};
