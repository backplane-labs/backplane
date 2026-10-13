import { create, toBinary } from '@bufbuild/protobuf';
import { FileDescriptorSetSchema } from '@bufbuild/protobuf/wkt';
import { SchemaFormat, ManifestSchema, ApiKind } from '@gopherex/backplane-api/backplanepb/v1/manifest_pb';

const bytes = (value: unknown) => new TextEncoder().encode(typeof value === 'string' ? value : JSON.stringify(value));
const bundleFiles: Record<string, unknown> = {
  'api.yaml': { openapi: '3.1.0', info: { title: 'Multi-file visitor API' }, paths: { '/bundle/visitor': { $ref: './paths/visitor.yaml' } } },
  'paths/visitor.yaml': { get: { summary: 'Read from a file bundle', responses: { '200': { description: 'A visitor', content: { 'application/json': { schema: { $ref: '../schemas/visitor.yaml' } } } } } } },
  'schemas/visitor.yaml': { type: 'object', properties: { name: { type: 'string' } } },
};
export async function readAPIFixtureFile(_hash: string, file: string): Promise<Uint8Array> {
  if (!Object.hasOwn(bundleFiles, file)) throw new Error(`No fixture file: ${file}`);
  return bytes(bundleFiles[file]);
}
export const publicDescriptors = toBinary(FileDescriptorSetSchema, create(FileDescriptorSetSchema, { file: [
  { name: 'types.proto', package: 'public.v1', syntax: 'proto3', messageType: [
    { name: 'Request', field: [{ name: 'name', number: 1, type: 9 }] },
    { name: 'Reply', field: [{ name: 'text', number: 1, type: 9 }] },
  ] },
  { name: 'service.proto', package: 'public.v1', syntax: 'proto3', dependency: ['types.proto'], service: [
    { name: 'Greeter', method: [{ name: 'Watch', inputType: '.public.v1.Request', outputType: '.public.v1.Reply', serverStreaming: true }] },
    { name: 'Internal', method: [{ name: 'Secret', inputType: '.public.v1.Request', outputType: '.public.v1.Reply' }] },
  ] },
] }));
export const externalAPIManifest = () => create(ManifestSchema, {
  service: 'hello', version: '1.0.0', descriptors: publicDescriptors,
  apis: [
    { kind: ApiKind.HTTP, prefix: '/api/', schema: { case: 'openapi', value: bytes({ openapi: '3.0.3', info: { title: 'Visitor API', version: '1.0.0' },
      paths: { '/api/visitors/{id}': { parameters: [{ $ref: '#/components/parameters/ID' }], get: { summary: 'Read a visitor', responses: { '200': { description: 'The visitor', content: { 'application/json': { schema: { $ref: '#/components/schemas/Visitor' } } } } } }, delete: { summary: 'Remove a visitor', responses: { '204': { description: 'Removed' } } } } },
      components: { parameters: { ID: { name: 'id', in: 'path', required: true, schema: { type: 'string' } } }, schemas: { Visitor: { type: 'object', required: ['name'], example: { name: 'Ada', roles: ['reader'], preferences: { timezone: 'UTC' } }, properties: { name: { type: 'string', minLength: 1, description: 'Display name' }, roles: { type: 'array', items: { type: 'string', enum: ['reader', 'editor'] } }, preferences: { type: 'object', properties: { timezone: { type: 'string', default: 'UTC' }, alerts: { type: 'boolean', default: true } } }, friend: { $ref: '#/components/schemas/Visitor' } } } } },
    }) } },
    { kind: ApiKind.GRPC, prefix: '/public.v1.Greeter/', services: ['public.v1.Greeter'] },
    { kind: ApiKind.WS_PROTO, prefix: '/ws/', services: ['public.v1.Greeter'], schema: { case: 'descriptors', value: publicDescriptors } },
    { kind: ApiKind.GRAPHQL, prefix: '/graphql/', schema: { case: 'graphql', value: bytes({ data: { __schema: { queryType: { name: 'Query' }, types: [
      { name: 'Query', kind: 'OBJECT', fields: [{ name: 'greeting', description: 'A personalized greeting', args: [{ name: 'name', type: { kind: 'NON_NULL', ofType: { kind: 'SCALAR', name: 'String' } } }], type: { kind: 'SCALAR', name: 'String' } }] },
    ] } } }) } },
    { kind: ApiKind.HTTP, prefix: '/legacy/' },
    { kind: ApiKind.HTTP, prefix: '/broken/', schema: { case: 'openapi', value: bytes('not an API document') } },
    { kind: ApiKind.HTTP, prefix: '/bundle/', schema: { case: 'bundle', value: { hash: 'a'.repeat(64), entry: 'api.yaml', format: SchemaFormat.OPENAPI } } },
  ],
});
