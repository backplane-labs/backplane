import { fromBinary, toJson } from '@bufbuild/protobuf';
import { FileDescriptorSetSchema, type DescriptorProto, type FieldDescriptorProto } from '@bufbuild/protobuf/wkt';
import { parseDocument } from 'yaml';
import { ApiKind, type Manifest, type API } from '@gopherex/backplane-api/backplanepb/v1/manifest_pb';

type ObjectValue = Record<string, unknown>;
export interface APIField { name: string; type: string; required?: boolean; description?: string; references?: string[] }
export interface APIExample { name: string; value: unknown }
export interface APISection { name: string; type?: string; required?: boolean; description?: string; fields?: APIField[]; examples?: APIExample[]; value?: unknown }
export interface APIOperation { id: string; method: string; name: string; title?: string; description?: string; deprecated?: boolean; sections: APISection[] }
export interface APIDocument { title: string; description?: string; format: string; operations: APIOperation[]; schemas: APISection[]; metadata?: unknown; source: string; warnings: string[] }
export type APIResult = { document: APIDocument } | { problem: 'missing' | 'invalid'; detail?: string };
const object = (value: unknown): ObjectValue => value !== null && typeof value === 'object' && !Array.isArray(value) ? value as ObjectValue : {};
const list = (value: unknown): unknown[] => Array.isArray(value) ? value : [];
const string = (value: unknown): string => typeof value === 'string' ? value : '';
const entries = (value: unknown) => Object.entries(object(value));
const decode = (bytes: Uint8Array) => new TextDecoder('utf-8', { fatal: true }).decode(bytes);

/** The catalog carries self-contained documents. Never fetch a schema's URLs. */
export function readAPIDocument(route: API, manifest: Manifest): APIResult {
  try {
    if (route.schema.case === 'openapi') return { document: openAPI(decode(route.schema.value)) };
    if (route.schema.case === 'graphql') return { document: graphQL(decode(route.schema.value)) };
    const bytes = route.schema.case === 'descriptors' ? route.schema.value : manifest.descriptors;
    if ([ApiKind.GRPC, ApiKind.WS_PROTO].includes(route.kind) && bytes.length) {
      return { document: protobuf(bytes, route.services) };
    }
    return { problem: 'missing' };
  } catch (error) { return { problem: 'invalid', detail: error instanceof Error ? error.message : String(error) }; }
}

function openAPI(source: string): APIDocument {
  const parsed = parseDocument(source, { uniqueKeys: true });
  if (parsed.errors.length) throw parsed.errors[0];
  const root = object(parsed.toJS({ maxAliasCount: 50 }));
  const version = string(root.openapi) || string(root.swagger);
  if (!/^3\.(0|1|2)\./.test(version) && version !== '2.0') throw new Error('Expected OpenAPI 3.x or Swagger 2.0.');
  const warnings = new Set<string>();
  const resolve = (value: unknown): ObjectValue => {
    const seen = new Set<string>();
    let current = object(value);
    while (typeof current.$ref === 'string') {
      const ref = current.$ref;
      if (seen.has(ref)) { warnings.add(ref); break; }
      seen.add(ref);
      if (ref !== '#' && !ref.startsWith('#/')) { warnings.add(ref); break; }
      let target: unknown = root;
      for (const part of ref === '#' ? [] : decodeURIComponent(ref.slice(2)).split('/')) {
        const key = part.replace(/~1/g, '/').replace(/~0/g, '~');
        target = Object.hasOwn(object(target), key) ? object(target)[key] : undefined;
      }
      if (target === undefined) { warnings.add(ref); break; }
      const { $ref: _, ...siblings } = current;
      current = { ...object(target), ...siblings };
    }
    return current;
  };
  // Check references without expanding schemas (recursive models remain finite).
  const scan = (value: unknown, ancestors = new Set<unknown>()) => {
    if (!value || typeof value !== 'object' || ancestors.has(value)) return;
    const next = new Set(ancestors); next.add(value);
    if (object(value).$ref) resolve(value);
    for (const child of Object.values(value)) scan(child, next);
  };
  scan(root);
  // YAML object aliases are not an OpenAPI reference mechanism. Reject cycles.
  const normalizedSource = JSON.stringify(root, null, 2);
  const referenceNames = (value: unknown): string[] => {
    const schema = object(value);
    if (typeof schema.$ref === 'string') return [decodeURIComponent(schema.$ref.split('/').at(-1) || schema.$ref).replace(/~1/g, '/').replace(/~0/g, '~')];
    return [...new Set([...(schema.items ? referenceNames(schema.items) : []), ...list(schema.oneOf ?? schema.anyOf ?? schema.allOf).flatMap(referenceNames)])];
  };
  const describe = (value: unknown): string => {
    const schema = resolve(value);
    return [string(schema.description), ...['default', 'enum', 'const', 'minimum', 'maximum', 'minLength', 'maxLength', 'minItems', 'maxItems', 'pattern', 'readOnly', 'writeOnly'].filter((key) => schema[key] !== undefined).map((key) => `${key}: ${JSON.stringify(schema[key])}`)].filter(Boolean).join('\n');
  };
  const fieldsFor = (value: unknown, prefix = '', seen = new Set<unknown>(), depth = 0): APIField[] => {
    const schema = resolve(value);
    if (depth > 4 || seen.has(value)) return [];
    const next = new Set(seen); next.add(value);
    const required = list(schema.required);
    const properties = entries(schema.properties);
    const own = properties.flatMap(([name, field]) => {
      const path = `${prefix}${name}`, resolved = resolve(field);
      const row = { name: path, type: schemaType(field), required: required.includes(name), description: describe(field), references: referenceNames(field) };
      // Named references are navigated, not expanded indefinitely in the table.
      return [row, ...(object(field).$ref ? [] : fieldsFor(resolved.type === 'array' ? resolved.items : field, `${path}${resolved.type === 'array' ? '[]' : ''}.`, next, depth + 1))];
    });
    return [...own, ...list(schema.allOf ?? schema.oneOf ?? schema.anyOf).flatMap((part) => fieldsFor(part, prefix, next, depth + 1))];
  };
  const mediaFields = (content: unknown): APIField[] => entries(content).flatMap(([name, media]) => {
    const schema = object(media).schema;
    return [{ name, type: schemaType(schema), description: describe(schema), references: referenceNames(schema) }, ...fieldsFor(schema, `${name} · `)];
  });
  const examplesFor = (value: unknown, name = 'Example'): APIExample[] => {
    const item = object(value);
    return [...(item.example !== undefined ? [{ name, value: item.example }] : []), ...(Array.isArray(item.examples) ? item.examples.map((value, index) => ({ name: `${name} ${index + 1}`, value })) : entries(item.examples).flatMap(([key, example]) => {
      const resolved = resolve(example);
      return resolved.value !== undefined ? [{ name: string(resolved.summary) || key, value: resolved.value }] : [];
    }))];
  };
  const mediaExamples = (content: unknown) => entries(content).flatMap(([name, media]) => { const own = examplesFor(media, name); return own.length ? own : examplesFor(resolve(object(media).schema), name); });
  const operations: APIOperation[] = [];
  for (const [path, value] of entries(root.paths)) {
    if (!path.startsWith('/')) continue;
    const pathItem = resolve(value);
    for (const method of ['get', 'post', 'put', 'patch', 'delete', 'head', 'options', 'trace']) {
      if (!pathItem[method]) continue;
      const op = resolve(pathItem[method]);
      const parameters = new Map<string, ObjectValue>();
      for (const parameter of [...list(pathItem.parameters), ...list(op.parameters)]) {
        const param = resolve(parameter); parameters.set(`${param.in}:${param.name}`, param);
      }
      const sections: APISection[] = [];
      if (parameters.size) sections.push({ name: 'parameters', fields: [...parameters.values()].map((param) => ({
        name: `${string(param.name)} (${string(param.in)})`, type: schemaType(param.schema ?? param), required: param.required === true || param.in === 'path', description: [string(param.description), describe(param.schema)].filter(Boolean).join('\n'), references: referenceNames(param.schema),
      })), examples: [...parameters.values()].flatMap((param) => examplesFor(param, string(param.name))), value: [...parameters.values()] });
      if (op.requestBody) {
        const body = resolve(op.requestBody);
        sections.push({ name: 'requestBody', required: body.required === true, description: string(body.description), fields: mediaFields(body.content), examples: mediaExamples(body.content), value: body });
      }
      for (const [code, response] of entries(op.responses)) {
        const result = resolve(response);
        const headers = entries(result.headers).map(([name, value]) => { const header = resolve(value); return { name: `${name} (header)`, type: schemaType(header.schema ?? header), description: string(header.description) }; });
        sections.push({ name: code, description: string(result.description), fields: [...(result.content ? mediaFields(result.content) : result.schema ? [{ name: 'body', type: schemaType(result.schema), references: referenceNames(result.schema) }, ...fieldsFor(result.schema)] : []), ...headers], examples: result.content ? mediaExamples(result.content) : entries(result.examples).map(([name, value]) => ({ name, value })), value: result });
      }
      if (op.security !== undefined || root.security !== undefined) {
        const security = op.security ?? root.security, schemes = object(object(root.components).securitySchemes ?? root.securityDefinitions);
        sections.push({ name: 'security', fields: list(security).flatMap((alternative, index) => entries(alternative).map(([name, scopes]) => ({ name, type: string(resolve(schemes[name]).type) || 'security scheme', description: `Alternative ${index + 1}${list(scopes).length ? ` · scopes: ${list(scopes).join(', ')}` : ''}` }))), value: security });
      }
      const codeSamples = list(op['x-codeSamples']).map(object).filter((sample) => typeof sample.source === 'string').map((sample) => ({ name: string(sample.label) || string(sample.lang) || 'Request', value: sample.source }));
      if (codeSamples.length) sections.push({ name: 'call', examples: codeSamples });
      else {
        const quote = (value: string) => `'${value.replace(/'/g, "'\\''")}'`;
        let url = (string(object(list(root.servers)[0]).url) || 'https://<service-host>').replace(/\/$/, '') + path;
        const query: string[] = [], headers: string[] = [];
        for (const param of parameters.values()) {
          const schema = resolve(param.schema ?? param);
          const value = String(param.example ?? schema.example ?? schema.default ?? `<${param.name}>`);
          if (param.in === 'path') url = url.replace(`{${param.name}}`, encodeURIComponent(value));
          if (param.in === 'query' && (param.required || param.example !== undefined || schema.default !== undefined)) query.push(`${encodeURIComponent(string(param.name))}=${encodeURIComponent(value)}`);
          if (param.in === 'header' && (param.required || param.example !== undefined)) headers.push(` --header ${quote(`${param.name}: ${value}`)}`);
        }
        if (query.length) url += `?${query.join('&')}`;
        const body = sections.find((section) => section.name === 'requestBody')?.examples?.[0];
        const media = entries(resolve(op.requestBody).content)[0]?.[0];
        sections.push({ name: 'call', examples: [{ name: 'curl', value: `curl --request ${method.toUpperCase()} ${quote(url)}${headers.join('')}${op.requestBody ? ` --header ${quote(`Content-Type: ${media || 'application/json'}`)} --data ${quote(body ? typeof body.value === 'string' ? body.value : JSON.stringify(body.value) : '<request-body>')}` : ''}` }] });
      }
      operations.push({ id: `${method}:${path}`, method: method.toUpperCase(), name: path, title: string(op.summary) || string(op.operationId),
        description: string(op.description), deprecated: op.deprecated === true, sections });
    }
  }
  const info = object(root.info);
  const schemaEntries = entries(object(root.components).schemas ?? root.definitions);
  for (const [key, documents] of entries(root)) if (key.startsWith('x-backplane-documents')) {
    for (const [file, value] of entries(documents)) {
      const schema = object(value);
      if (schema.type || schema.properties || schema.enum || schema.allOf || schema.oneOf || schema.anyOf) schemaEntries.push([file, value]);
    }
  }
  return { title: string(info.title), description: string(info.description), format: `OpenAPI ${version}`, operations,
    schemas: schemaEntries.map(([name, value]) => {
      const schema = resolve(value);
      return { name, type: schemaType(schema), description: describe(schema), fields: fieldsFor(schema), examples: examplesFor(schema), value: schema };
    }), metadata: { servers: root.servers, host: root.host, basePath: root.basePath, schemes: root.schemes,
      securitySchemes: object(root.components).securitySchemes ?? root.securityDefinitions }, source: normalizedSource, warnings: [...warnings] };
}

function schemaType(value: unknown, depth = 0): string {
  if (depth > 12) return '…';
  if (value === false) return 'never';
  const schema = object(value);
  if (typeof schema.$ref === 'string') return decodeURIComponent(schema.$ref.split('/').at(-1) || schema.$ref).replace(/~1/g, '/').replace(/~0/g, '~') + (schema.nullable ? ' | null' : '');
  if (schema.type === 'array') { const item = schemaType(schema.items, depth + 1); return `${/[|&]/.test(item) ? `(${item})` : item}[]`; }
  if (Array.isArray(schema.enum)) return schema.enum.map((item) => JSON.stringify(item)).join(' | ') || 'never';
  if (schema.const !== undefined) return JSON.stringify(schema.const);
  if (schema.oneOf || schema.anyOf || schema.allOf) return list(schema.oneOf ?? schema.anyOf ?? schema.allOf).map((item) => schemaType(item, depth + 1)).join(schema.allOf ? ' & ' : ' | ');
  if (schema.additionalProperties && !schema.properties) return `map<string, ${schemaType(schema.additionalProperties, depth + 1)}>`;
  return (Array.isArray(schema.type) ? schema.type.join(' | ') : string(schema.type) || (schema.properties ? 'object' : 'any')) + (schema.format ? ` (${schema.format})` : '') + (schema.nullable ? ' | null' : '');
}

function protobuf(bytes: Uint8Array, names: string[]): APIDocument {
  const set = fromBinary(FileDescriptorSetSchema, bytes), operations: APIOperation[] = [], schemas: APISection[] = [];
  if (!set.file.length) throw new Error('The descriptor set contains no files.');
  const scalar = ['', 'double', 'float', 'int64', 'uint64', 'int32', 'fixed64', 'fixed32', 'bool', 'string', 'group', 'message', 'bytes', 'uint32', 'enum', 'sfixed32', 'sfixed64', 'sint32', 'sint64'];
  const maps = new Map<string, DescriptorProto>();
  const collectMaps = (items: DescriptorProto[], prefix: string) => { for (const item of items) { const name = `${prefix}${item.name}`; if (item.options?.mapEntry) maps.set(name, item); collectMaps(item.nestedType, `${name}.`); } };
  for (const file of set.file) collectMaps(file.messageType, file.package ? `${file.package}.` : '');
  const baseType = (field: FieldDescriptorProto) => field.typeName?.replace(/^\./, '') || scalar[field.type ?? 0] || '?';
  const fieldType = (field: FieldDescriptorProto) => {
    const map = maps.get(baseType(field));
    return map?.field[0] && map.field[1] ? `map<${baseType(map.field[0])}, ${baseType(map.field[1])}>` : baseType(field) + (field.label === 3 ? '[]' : '');
  };
  for (const file of set.file) {
    const comments = (path: number[]) => file.sourceCodeInfo?.location.find((entry) => entry.path.join('.') === path.join('.'))?.leadingComments?.trim();
    const qualify = (name: string) => [file.package, name].filter(Boolean).join('.');
    const messages = (items: DescriptorProto[], prefix: string, path: number[]) => {
      items.forEach((message, index) => {
        const name = `${prefix}${message.name}`, at = [...path, index];
        if (!message.options?.mapEntry) schemas.push({ name, type: 'message', description: comments(at), fields: message.field.map((field, index) => ({
          name: field.name ?? '', type: fieldType(field), required: field.label === 2, description: comments([...at, 2, index]), references: field.typeName ? [maps.get(baseType(field))?.field[1]?.typeName?.replace(/^\./, '') || baseType(field)] : [],
        })) });
        messages(message.nestedType, `${name}.`, [...at, 3]);
        for (const entry of message.enumType) schemas.push({ name: `${name}.${entry.name}`, type: 'enum', fields: entry.value.map((item) => ({ name: item.name ?? '', type: String(item.number) })) });
      });
    };
    messages(file.messageType, file.package ? `${file.package}.` : '', [4]);
    for (const entry of file.enumType) schemas.push({ name: qualify(entry.name ?? ''), type: 'enum', fields: entry.value.map((item) => ({ name: item.name ?? '', type: String(item.number) })) });
    file.service.forEach((service, serviceIndex) => {
      const name = qualify(service.name ?? '');
      if (names.length && !names.includes(name)) return;
      service.method.forEach((method, methodIndex) => operations.push({ id: `/${name}/${method.name}`, method: 'RPC', name: `/${name}/${method.name}`,
        description: comments([6, serviceIndex, 2, methodIndex]), deprecated: method.options?.deprecated,
        sections: [{ name: 'requestBody', fields: [{ name: method.clientStreaming ? 'stream' : 'message', type: method.inputType?.replace(/^\./, '') ?? '' }] },
          { name: 'response', fields: [{ name: method.serverStreaming ? 'stream' : 'message', type: method.outputType?.replace(/^\./, '') ?? '' }] }],
      }));
    });
  }
  // Only types reachable from this route's methods belong to its public API.
  const reachable = new Set<string>();
  const visit = (name: string) => {
    if (reachable.has(name)) return;
    reachable.add(name);
    const schema = schemas.find((entry) => entry.name === name);
    for (const field of schema?.fields ?? []) for (const name of field.references?.length ? field.references : [field.type.replace(/\[\]$/, '')]) visit(name);
  };
  for (const op of operations) for (const section of op.sections) for (const field of section.fields ?? []) visit(field.type);
  const sampleMessage = (name: string, seen = new Set<string>()): unknown => {
    if (seen.has(name) || seen.size > 4) return {};
    const type = schemas.find((schema) => schema.name === name);
    if (type?.type === 'enum') return type.fields?.[0]?.name;
    const next = new Set(seen); next.add(name);
    return Object.fromEntries((type?.fields ?? []).map((field) => {
      const base = field.type.replace(/\[\]$/, '');
      const value = field.type.startsWith('map<') ? {} : schemas.some((schema) => schema.name === base) ? sampleMessage(base, next) : base === 'string' ? `<${field.name}>` : base === 'bytes' ? '' : base === 'bool' ? false : /64$/.test(base) ? '0' : 0;
      return [field.name, field.type.endsWith('[]') ? [value] : value];
    }));
  };
  for (const op of operations) for (const section of op.sections) {
    const type = section.fields?.[0]?.type;
    if (type && schemas.some((schema) => schema.name === type)) section.examples = [{ name: 'JSON message', value: sampleMessage(type) }];
  }
  return { title: names.join(', '), format: 'Protobuf', operations, schemas: schemas.filter((schema) => reachable.has(schema.name)), source: JSON.stringify(toJson(FileDescriptorSetSchema, set), null, 2), warnings: [] };
}

function graphQL(source: string): APIDocument {
  const root = object(JSON.parse(source)), schema = object(root.__schema ?? object(root.data).__schema);
  if (!Array.isArray(schema.types)) throw new Error('Expected a GraphQL introspection result with __schema.types.');
  const types = schema.types.map(object);
  const typeName = (value: unknown, depth = 0): string => {
    if (depth > 12) return '…';
    const ref = object(value);
    return ref.kind === 'NON_NULL' ? `${typeName(ref.ofType, depth + 1)}!` : ref.kind === 'LIST' ? `[${typeName(ref.ofType, depth + 1)}]` : string(ref.name);
  };
  const fields = (value: unknown): APIField[] => list(value).map(object).map((field) => ({
    name: string(field.name), type: typeName(field.type), required: object(field.type).kind === 'NON_NULL', description: [string(field.description), ...(field.defaultValue != null ? [`default: ${field.defaultValue}`] : []), ...(field.isDeprecated ? [`Deprecated: ${string(field.deprecationReason)}`] : [])].filter(Boolean).join('\n'),
  }));
  const operations: APIOperation[] = [];
  const unwrapped = (value: unknown): ObjectValue => { const ref = object(value); return ref.kind === 'NON_NULL' || ref.kind === 'LIST' ? unwrapped(ref.ofType) : ref; };
  const sampleValue = (value: unknown, name: string, depth = 0): string | undefined => {
    if (depth > 4) return undefined;
    const ref = object(value);
    if (ref.kind === 'NON_NULL') return sampleValue(ref.ofType, name, depth + 1);
    if (ref.kind === 'LIST') { const item = sampleValue(ref.ofType, name, depth + 1); return item === undefined ? undefined : `[${item}]`; }
    if (ref.name === 'Boolean') return 'false';
    if (ref.name === 'Int' || ref.name === 'Float') return '0';
    if (ref.name === 'String' || ref.name === 'ID') return JSON.stringify(`<${name}>`);
    const type = types.find((entry) => entry.name === ref.name);
    if (type?.kind === 'ENUM') return string(object(list(type.enumValues)[0]).name) || undefined;
    if (type?.kind === 'INPUT_OBJECT') {
      const fields = list(type.inputFields).map(object).filter((field) => object(field.type).kind === 'NON_NULL');
      const values = fields.map((field) => { const sample = sampleValue(field.type, string(field.name), depth + 1); return sample === undefined ? undefined : `${field.name}: ${sample}`; });
      return values.some((value) => value === undefined) ? undefined : `{ ${values.join(', ')} }`;
    }
    return undefined;
  };
  const selection = (value: unknown): string | undefined => {
    const ref = unwrapped(value), type = types.find((entry) => entry.name === ref.name);
    if (ref.kind === 'SCALAR' || ref.kind === 'ENUM') return '';
    if (!type) return undefined;
    const leaf = list(type.fields).map(object).find((field) => ['SCALAR', 'ENUM'].includes(string(unwrapped(field.type).kind)) && !list(field.args).map(object).some((arg) => object(arg.type).kind === 'NON_NULL'));
    return leaf ? ` { ${leaf.name} }` : ' { __typename }';
  };
  for (const kind of ['query', 'mutation', 'subscription']) {
    const rootName = string(object(schema[`${kind}Type`]).name), type = types.find((entry) => entry.name === rootName);
    for (const field of list(type?.fields).map(object)) {
      const args = list(field.args).map(object).filter((arg) => object(arg.type).kind === 'NON_NULL' && arg.defaultValue == null);
      const values = args.map((arg) => { const value = sampleValue(arg.type, string(arg.name)); return value === undefined ? undefined : `${arg.name}: ${value}`; });
      const suffix = selection(field.type);
      operations.push({ id: `${kind}:${field.name}`, method: kind.toUpperCase(), name: string(field.name),
      description: string(field.description), deprecated: field.isDeprecated === true,
      sections: [{ name: 'parameters', fields: fields(field.args) }, { name: 'response', fields: [{ name: string(field.name), type: typeName(field.type) }] }, ...(values.every((value) => value !== undefined) && suffix !== undefined ? [{ name: 'call', examples: [{ name: 'GraphQL', value: `${kind} { ${field.name}${values.length ? `(${values.join(', ')})` : ''}${suffix} }` }] }] : [])],
    }); }
  }
  return { title: 'GraphQL', format: 'GraphQL', operations, schemas: types.filter((type) => !string(type.name).startsWith('__')).map((type) => ({
    name: string(type.name), type: string(type.kind).toLowerCase(), description: string(type.description), fields: type.enumValues ? list(type.enumValues).map(object).map((value) => ({ name: string(value.name), type: 'enum', description: string(value.description) })) : fields(type.fields ?? type.inputFields), value: type,
  })), source: JSON.stringify(root, null, 2), warnings: [] };
}
