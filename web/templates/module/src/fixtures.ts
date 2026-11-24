import { ListServicesResponseSchema } from '@backplane-labs/backplane-api';
import type { ClientConstructor, ClientRuntime, ClientState } from '@backplane-labs/backplane-client';
import { create, type Message } from '@bufbuild/protobuf';
import { type AnyResponse, type Interceptor, WsTransport } from '@gopherex/ws-proto-transport';
import { GetStatsResponseSchema } from './gen/proto/hello/console/v1/admin_pb';

const state: ClientState = { connection: 'connected', session: null, connectionId: 1 };

/** Local answers per RPC name; the generated clients reach them through the transport below. */
const answers = new Map<string, () => Message>([
  [
    'GetStats',
    () =>
      create(GetStatsResponseSchema, {
        greetings: 9007199254740993n,
        currentGreeting: 'Hello',
        cachePresent: true,
      }),
  ],
  [
    'ListServices',
    () =>
      create(ListServicesResponseSchema, {
        services: [
          { name: 'hello', instances: 1 },
          { name: 'formatter', instances: 1 },
        ],
      }),
  ],
]);

const answerFixture: Interceptor = (_next) => (request) => {
  const answer = answers.get(request.method.name);
  if (answer === undefined || request.stream) throw new Error(`No fixture for ${request.method.name}`);
  const response: AnyResponse = {
    stream: false,
    method: request.method,
    header: request.header,
    message: answer(),
    trailer: {},
  };
  return Promise.resolve(response);
};

// Paused: it never dials; every call is answered by the interceptor.
const fixtureTransport = new WsTransport('wss://fixture.invalid/', { paused: true, interceptors: [answerFixture] });

/** Explicit local fixture: the real generated clients over local answers. Replace with BackplaneClient or your own adapter. */
export const fixtureClient: ClientRuntime = {
  getSnapshot: () => state,
  subscribe: () => () => {},
  client<T extends object>(clientConstructor: ClientConstructor<T>): T {
    return new clientConstructor(fixtureTransport);
  },
};
