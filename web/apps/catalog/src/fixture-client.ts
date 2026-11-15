import type { ClientConstructor } from '@backplane-labs/backplane-client';
import { type AnyRequest, type AnyResponse, WsTransport } from '@gopherex/ws-proto-transport';

/** The generated client's method for an RPC: ListServices -> listServices. */
const methodName = (rpc: string) => rpc.charAt(0).toLowerCase() + rpc.slice(1);

async function first<T>(input: AsyncIterable<T>): Promise<T> {
  for await (const item of input) return item;
  throw new Error('A streaming call sent no request');
}

/**
 * The generated client the runtime is asked for, answered by a hand-written methods
 * object: the client runs over a paused transport whose interceptor calls the method
 * of the same name with the request and call options. A method the fixture lacks
 * fails loudly.
 */
export function fixtureClient<T extends object>(client: ClientConstructor<T>, methods: object): T {
  const answer = async (request: AnyRequest): Promise<AnyResponse> => {
    const name = methodName(request.method.name);
    const method = Reflect.get(methods, name);
    if (typeof method !== 'function') throw new Error(`No explicit fixture for ${name} (client ${client.name})`);
    const options = { headers: request.header, signal: request.signal, timeoutMs: request.timeoutMs };
    const head = { method: request.method, header: request.header, trailer: {} };
    if (request.stream)
      return {
        ...head,
        stream: true,
        message: Reflect.apply(method, methods, [await first(request.message), options]),
      };
    return { ...head, stream: false, message: await Reflect.apply(method, methods, [request.message, options]) };
  };
  return new client(new WsTransport('wss://fixture.invalid/', { paused: true, interceptors: [() => answer] }));
}
