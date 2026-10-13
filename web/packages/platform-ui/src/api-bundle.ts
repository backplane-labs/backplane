import { create } from '@bufbuild/protobuf';
import { parseDocument } from 'yaml';
import { SchemaFormat, ManifestSchema, ApiKind, APISchema, type APISchemaBundle } from '@gopherex/backplane-api/backplanepb/v1/manifest_pb';
import { readAPIDocument, type APIResult } from './api-document.js';

export type APIFileReader = (hash: string, file: string, signal: AbortSignal) => Promise<Uint8Array>;

/** Resolve only files from this authenticated, content-addressed snapshot. */
export async function readAPIBundle(bundle: APISchemaBundle, read: APIFileReader, signal: AbortSignal): Promise<APIResult> {
  if (bundle.format === SchemaFormat.GRAPHQL) {
    const value = await read(bundle.hash, bundle.entry, signal);
    return readAPIDocument(create(APISchema, { kind: ApiKind.GRAPHQL, schema: { case: 'graphql', value } }), create(ManifestSchema));
  }
  if (bundle.format !== SchemaFormat.OPENAPI) return { problem: 'invalid', detail: 'Unsupported API bundle format.' };
  const source = await bundleOpenAPI(bundle.entry, (file) => read(bundle.hash, file, signal));
  return readAPIDocument(create(APISchema, { kind: ApiKind.HTTP, schema: { case: 'openapi', value: new TextEncoder().encode(source) } }), create(ManifestSchema));
}

/** Rewrite file references to internal JSON pointers, preserving recursive refs. */
export async function bundleOpenAPI(entry: string, read: (file: string) => Promise<Uint8Array>): Promise<string> {
  const files = new Map<string, unknown>(), queue = [relativeFile('', entry)], queued = new Set(queue);
  const rootFile = queue[0]!;
  let total = 0, extension = 'x-backplane-documents';
  const pointer = (file: string) => file === rootFile ? '' : `/${extension}/${file.replace(/~/g, '~0').replace(/\//g, '~1')}`;
  while (queue.length) {
    const file = queue.shift()!, bytes = await read(file);
    total += bytes.length;
    if (total > 32 * 1024 * 1024 || queued.size > 256) throw new Error('API bundle exceeds its file or size limit.');
    const parsed = parseDocument(new TextDecoder('utf-8', { fatal: true }).decode(bytes), { uniqueKeys: true });
    if (parsed.errors.length) throw parsed.errors[0];
    const value: unknown = JSON.parse(JSON.stringify(parsed.toJS({ maxAliasCount: 50 })));
    if (file === rootFile && value && typeof value === 'object') while (Object.hasOwn(value, extension)) extension += '-files';
    files.set(file, value);
    const rewrite = (node: unknown) => {
      if (!node || typeof node !== 'object') return;
      const object = node as Record<string, unknown>;
      if (typeof object.$ref === 'string') {
        const ref = object.$ref, at = ref.indexOf('#');
        const location = at < 0 ? ref : ref.slice(0, at), fragment = at < 0 ? '' : ref.slice(at + 1);
        if (fragment && !fragment.startsWith('/')) throw new Error(`Only JSON-pointer references are supported: ${ref}`);
        const target = location ? relativeFile(file, location) : file;
        if (!queued.has(target)) {
          if (queued.size >= 256) throw new Error('API bundle exceeds its file limit.');
          queued.add(target); queue.push(target);
        }
        object.$ref = `#${pointer(target)}${fragment}`;
      }
      for (const child of Object.values(object)) rewrite(child);
    };
    rewrite(value);
  }
  const root = files.get(rootFile);
  if (!root || typeof root !== 'object' || Array.isArray(root)) throw new Error('Expected an OpenAPI root document.');
  const documents = Object.fromEntries([...files].filter(([file]) => file !== rootFile));
  return JSON.stringify({ ...root, ...(Object.keys(documents).length ? { [extension]: documents } : {}) });
}

function relativeFile(parent: string, reference: string): string {
  const decoded = decodeURIComponent(reference);
  if (!decoded || /^[\/\\]|^[a-z][a-z0-9+.-]*:/i.test(decoded) || /[?\\\0]/.test(decoded)) throw new Error(`External API reference is not a bundle file: ${reference}`);
  const parts = parent.split('/').slice(0, -1);
  for (const part of decoded.split('/')) {
    if (!part || part === '.') continue;
    if (part === '..') { if (!parts.length) throw new Error(`API reference escapes its bundle: ${reference}`); parts.pop(); }
    else parts.push(part);
  }
  if (!parts.length) throw new Error('API entry must name a file.');
  return parts.join('/');
}
