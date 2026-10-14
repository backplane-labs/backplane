import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { create, toBinary } from '@bufbuild/protobuf';
import { FileDescriptorSetSchema } from '@bufbuild/protobuf/wkt';
import { ManifestSchema, APISchema, ApiKind } from '@backplane-labs/backplane-api/backplanepb/v1/manifest_pb';
import { readAPIDocument, type APIDocument } from '../packages/platform-ui/src/api-document';
import { externalAPIManifest } from '../apps/catalog/src/api-fixture';
import { bundleOpenAPI } from '../packages/platform-ui/src/api-bundle';

const manifest = externalAPIManifest();
function document(index: number): APIDocument {
  const result = readAPIDocument(manifest.apis[index]!, manifest);
  if (!('document' in result)) throw new Error(result.detail ?? result.problem);
  return result.document;
}
function openAPI(value: unknown) {
  const route = create(APISchema, { kind: ApiKind.HTTP, schema: { case: 'openapi', value: new TextEncoder().encode(typeof value === 'string' ? value : JSON.stringify(value)) } });
  return readAPIDocument(route, create(ManifestSchema));
}

describe('external service API documents', () => {
  it('resolves relative files and cyclic cross-file references once each', async () => {
    const files: Record<string, unknown> = {
      'api/root.yaml': { openapi: '3.0.3', paths: { '/x': { $ref: '../paths/x.yaml' } }, components: { schemas: { Visitor: { $ref: '../types/visitor.yaml' } } } },
      'paths/x.yaml': { get: { responses: { '200': { content: { 'application/json': { schema: { $ref: '../types/visitor.yaml' } } } } } } },
      'types/visitor.yaml': { type: 'object', properties: { child: { $ref: './visitor.yaml' } } },
    };
    const calls: string[] = [];
    const source = await bundleOpenAPI('api/root.yaml', async (file) => { calls.push(file); return new TextEncoder().encode(JSON.stringify(files[file])); });
    const result = openAPI(source);
    expect(calls).toEqual(['api/root.yaml', 'paths/x.yaml', 'types/visitor.yaml']);
    expect('document' in result && result.document.operations[0]?.name).toBe('/x');
    expect('document' in result && result.document.warnings).toEqual([]);
  });
  it.each(['https://outside.example/api.json', '//outside.example/api.json', '../../outside.yaml', '/etc/passwd'])('refuses references outside the bundle: %s', async (ref) => {
    const calls: string[] = [];
    await expect(bundleOpenAPI('api.yaml', async (file) => { calls.push(file); return new TextEncoder().encode(JSON.stringify({ openapi: '3.0.3', paths: { '/x': { $ref: ref } } })); })).rejects.toThrow();
    expect(calls).toEqual(['api.yaml']);
  });
  it('resolves shared path parameters and keeps recursive schemas finite', () => {
    const doc = document(0);
    expect(doc.operations.map((op) => op.method)).toEqual(['GET', 'DELETE']);
    expect(doc.operations[0]?.sections[0]?.fields).toMatchObject([{ name: 'id (path)', type: 'string', required: true, description: '' }]);
    expect(doc.schemas[0]?.fields?.find((field) => field.name === 'friend')?.type).toBe('Visitor');
    expect(doc.warnings).toEqual([]);
  });
  it('lets operation-level parameters override path-level parameters', () => {
    const result = openAPI({ openapi: '3.1.0', paths: { '/x': { parameters: [{ name: 'q', in: 'query', schema: { type: 'string' } }], get: { parameters: [{ name: 'q', in: 'query', required: true, schema: { type: 'integer' } }] } } } });
    expect('document' in result && result.document.operations[0]?.sections[0]?.fields).toMatchObject([{ name: 'q (query)', type: 'integer', required: true, description: '' }]);
  });
  it('reads the real embedded multi-file hello API', async () => {
    const source = await bundleOpenAPI('openapi.yaml', async (file) => new Uint8Array(readFileSync(new URL(`../../examples/hello/internal/web/${file}`, import.meta.url))));
    const result = openAPI(source);
    expect('document' in result && result.document.operations[0]?.name).toBe('/hello/');
    expect('document' in result && result.document.warnings).toEqual([]);
    expect('document' in result && result.document.schemas[0]).toMatchObject({ name: 'schemas/greeting.yaml', type: 'string' });
    expect('document' in result && result.document.operations[0]?.sections.find((section) => section.name === '200')?.examples).toHaveLength(2);
    expect('document' in result && result.document.operations[0]?.sections.find((section) => section.name === 'call')?.examples?.[0]?.value).toContain('name=Developer');
  });
  it('exposes nested properties, constraints, array enums and examples without unrolling recursive references', () => {
    const visitor = document(0).schemas[0]!;
    expect(visitor.fields).toEqual(expect.arrayContaining([
      expect.objectContaining({ name: 'preferences.timezone', description: 'default: "UTC"' }),
      expect.objectContaining({ name: 'roles', type: '("reader" | "editor")[]' }),
      expect.objectContaining({ name: 'name', required: true, description: 'Display name\nminLength: 1' }),
      expect.objectContaining({ name: 'friend', type: 'Visitor', references: ['Visitor'] }),
    ]));
    expect(visitor.fields?.some((field) => field.name.startsWith('friend.'))).toBe(false);
    expect(visitor.examples?.[0]?.value).toMatchObject({ name: 'Ada' });
  });
  it('reads the formatter specification and its real JSON counter fields', () => {
    const result = openAPI(readFileSync(new URL('../../examples/formatter/internal/formatter/openapi.yaml', import.meta.url), 'utf8'));
    expect(result).toMatchObject({ document: { title: 'Formatter statistics API', warnings: [], schemas: [{ name: 'Statistics', fields: [
      { name: 'formatted', type: 'integer (uint64)' }, { name: 'recorded' }, { name: 'observed' }, { name: 'last_text' },
    ] }] } });
  });
  it('documents the separately owned legacy listener', () => {
    const result = openAPI(readFileSync(new URL('../../examples/hello/internal/legacy/openapi.yaml', import.meta.url), 'utf8'));
    expect(result).toMatchObject({ document: { title: 'Legacy listener example API', operations: [{ method: 'GET', name: '/legacy/' }], warnings: [] } });
    expect('document' in result && result.document.operations[0]?.sections.find((section) => section.name === '200')?.examples).toMatchObject([{ value: "served by hello's own listener\n" }]);
  });
  it('retains transitive protobuf messages and enums, renders maps, and excludes unrelated admin types', () => {
    const bytes = toBinary(FileDescriptorSetSchema, create(FileDescriptorSetSchema, { file: [{ package: 'public', messageType: [
      { name: 'Request', field: [{ name: 'labels', type: 11, typeName: '.public.Request.LabelsEntry', label: 3 }], nestedType: [{ name: 'LabelsEntry', options: { mapEntry: true }, field: [{ name: 'key', type: 9 }, { name: 'value', type: 11, typeName: '.public.Reply' }] }] },
      { name: 'Reply', field: [{ name: 'status', type: 14, typeName: '.public.Status' }] }, { name: 'AdminSecret' },
    ], enumType: [{ name: 'Status', value: [{ name: 'OK', number: 0 }] }], service: [{ name: 'API', method: [{ name: 'Read', inputType: '.public.Request', outputType: '.public.Reply' }] }] }] }));
    const result = readAPIDocument(create(APISchema, { kind: ApiKind.GRPC, services: ['public.API'], schema: { case: 'descriptors', value: bytes } }), create(ManifestSchema));
    expect('document' in result && result.document.schemas.map((schema) => schema.name)).toEqual(['public.Request', 'public.Reply', 'public.Status']);
    expect('document' in result && result.document.schemas[0]?.fields?.[0]?.type).toBe('map<string, public.Reply>');
  });
  it('supports Swagger 2.0 definitions and responses', () => {
    const result = openAPI({ swagger: '2.0', paths: { '/x': { get: { responses: { '200': { $ref: '#/responses/OK' } } } } }, responses: { OK: { description: 'Good' } }, definitions: { Result: { type: 'string' } } });
    expect('document' in result && result.document.operations[0]?.sections[0]?.description).toBe('Good');
    expect('document' in result && result.document.schemas[0]?.name).toBe('Result');
  });
  it('reports external, missing and cyclic reference chains without fetching them', () => {
    const result = openAPI({ openapi: '3.0.3', paths: { '/x': { $ref: './paths.yaml#/x' }, '/y': { $ref: '#/missing' }, '/z': { $ref: '#/paths/~1z' } } });
    expect('document' in result && result.document.warnings).toEqual(['./paths.yaml#/x', '#/missing', '#/paths/~1z']);
  });
  it('decodes escaped JSON pointer tokens', () => {
    const result = openAPI({ openapi: '3.0.3', paths: { '/x': { $ref: '#/components/pathItems/a~1b~0c' } }, components: { pathItems: { 'a/b~c': { get: { summary: 'Resolved' } } } } });
    expect('document' in result && result.document.operations[0]?.title).toBe('Resolved');
  });
  it('uses shared descriptors and filters out internal RPC services', () => {
    const doc = document(1);
    expect(doc.operations.map((op) => op.name)).toEqual(['/public.v1.Greeter/Watch']);
    expect(doc.operations[0]?.sections[1]?.fields).toEqual([{ name: 'stream', type: 'public.v1.Reply' }]);
    expect(doc.schemas.map((schema) => schema.name)).toEqual(['public.v1.Request', 'public.v1.Reply']);
  });
  it('uses route-local descriptor sets for ws-proto', () => {
    expect(document(2).operations).toEqual(document(1).operations);
  });
  it('renders GraphQL root operations and required argument types', () => {
    const doc = document(3);
    expect(doc.operations[0]?.method).toBe('QUERY');
    expect(doc.operations[0]?.sections[0]?.fields).toEqual([{ name: 'name', type: 'String!', required: true, description: '' }]);
    const route = create(APISchema, { kind: ApiKind.GRAPHQL, schema: { case: 'graphql', value: new TextEncoder().encode(JSON.stringify(JSON.parse(doc.source).data)) } });
    expect(readAPIDocument(route, manifest)).toMatchObject({ document: { operations: doc.operations } });
  });
  it('distinguishes missing documents from malformed documents', () => {
    expect(readAPIDocument(manifest.apis[4]!, manifest)).toEqual({ problem: 'missing' });
    expect(readAPIDocument(manifest.apis[5]!, manifest)).toMatchObject({ problem: 'invalid' });
    expect(openAPI('openapi: 3.0.3\npaths: [')).toMatchObject({ problem: 'invalid' });
    expect(openAPI('openapi: 3.0.3\npaths: &paths { self: *paths }')).toMatchObject({ problem: 'invalid' });
  });
});
