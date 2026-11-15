import * as api from '@backplane-labs/backplane-api';
import { SchemaSchema } from '@backplane-labs/backplane-api/schemapb/schema_pb';
import { create, fromJson, toJson } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';
import { isScalar } from 'yaml';
import { framesOf, indexCatalog, outputShape, scopeAt, scopeOf } from '../packages/platform-ui/src/wiring/catalog';
import { completeCEL, completeYAML, parentKeys } from '../packages/platform-ui/src/wiring/complete';
import {
  addStep,
  editAll,
  exprRange,
  locate,
  nodeAt,
  parseDraft,
  pathAt,
  protoDuration,
  removeStep,
  stepAt,
  stepNameFor,
  stepPathOf,
  templateYAML,
  toYAML,
} from '../packages/platform-ui/src/wiring/document';
import { shapeAt, shapeOf } from '../packages/platform-ui/src/wiring/shape';

/** A parsed draft that must be a valid binding definition. */
function bindingOf(draft: ReturnType<typeof parseDraft>): api.BindingDefinition {
  const definition = draft.definition;
  if (definition?.$typeName !== api.BindingDefinitionSchema.typeName)
    throw new Error(`Expected a binding definition: ${JSON.stringify(draft.problems)}`);
  return definition;
}
/** A parsed draft that must be a valid rule definition. */
function ruleOf(draft: ReturnType<typeof parseDraft>): api.RuleDefinition {
  const definition = draft.definition;
  if (definition?.$typeName !== api.RuleDefinitionSchema.typeName)
    throw new Error(`Expected a rule definition: ${JSON.stringify(draft.problems)}`);
  return definition;
}
/** The completion options of a result that must exist. */
function optionsOf(result: ReturnType<typeof completeYAML> | ReturnType<typeof completeCEL>) {
  if (!result) throw new Error('Expected completions');
  return result.options.map((option) => option.label);
}

const binding = `hook: hello.Greet
description: Greets
steps:
  # the formatter does the work
  format:
    activity: formatter.Format
    input: {name: req.name}
  audit: {activity: formatter.Record, after: [format], input: {name: "'é' + nope", text: format.text}}
result: {text: format.text}
`;

const text = (...names: string[]) =>
  create(SchemaSchema, {
    fields: names.map((name) => ({ name, required: true, kind: { case: 'string' as const, value: {} } })),
  });
const people = create(SchemaSchema, {
  fields: [
    {
      name: 'people',
      kind: { case: 'list', value: { items: [{ kind: { case: 'object', value: { schema: text('name') } } }] } },
    },
  ],
});
const index = indexCatalog([
  create(api.WiringContractSchema, {
    service: 'formatter',
    activities: [
      { name: 'Format', input: text('name'), output: text('text') },
      { name: 'Record', input: text('name', 'text') },
    ],
  }),
  create(api.WiringContractSchema, {
    service: 'hello',
    hooks: [
      { name: 'Greet', input: text('name'), output: text('text') },
      { name: 'Batch', input: people },
    ],
    events: [{ name: 'Greeted', schema: text('name') }],
  }),
]);

describe('wiring YAML', () => {
  it('reads a binding and writes it back in canonical order', () => {
    const draft = parseDraft('binding', binding);
    expect(draft.problems).toEqual([]);
    const definition = bindingOf(draft);
    expect(Object.keys(definition.steps).sort()).toEqual(['audit', 'format']);
    expect(definition.steps.audit?.after).toEqual(['format']);
    const again = parseDraft('binding', toYAML('binding', definition));
    expect(toJson(api.BindingDefinitionSchema, bindingOf(again))).toEqual(
      toJson(api.BindingDefinitionSchema, definition),
    );
    expect(toYAML('binding', definition).indexOf('hook:')).toBe(0);
    expect(toYAML('binding', definition)).toContain('after: [ format ]');
  });

  it('reports YAML and shape problems at their place', () => {
    const syntax = parseDraft('binding', 'hook: [\n');
    expect(syntax.definition).toBeUndefined();
    expect(syntax.problems[0]?.source).toBe('yaml');
    const unknown = parseDraft('binding', 'hook: hello.Greet\nsteps:\n  a: {activty: x.Y}\n');
    expect(unknown.problems).toHaveLength(1);
    expect(unknown.problems[0]).toMatchObject({ source: 'shape', path: '/steps/a/activty' });
    expect(unknown.text.slice(unknown.problems[0]?.from, unknown.problems[0]?.to)).toBe('activty');
    const duration = parseDraft('binding', 'hook: hello.Greet\nsteps:\n  a: {activity: x.Y, startToClose: soon}\n');
    expect(duration.problems[0]?.message).toContain('duration');
  });

  it('takes Go durations', () => {
    expect(protoDuration('1m30s')).toBe('90s');
    expect(protoDuration('500ms')).toBe('0.5s');
    expect(protoDuration('30s')).toBe('30s');
    const draft = parseDraft(
      'binding',
      'hook: hello.Greet\nsteps:\n  a: {activity: x.Y, startToClose: 1m, retry: {initialInterval: 250ms}}\n',
    );
    const step = bindingOf(draft).steps.a;
    expect(step.startToClose?.seconds).toBe(60n);
    expect(step.retry?.initialInterval?.nanos).toBe(250_000_000);
  });

  it('places a violation of the server in the text: node, expression range, missing node', () => {
    const draft = parseDraft('binding', binding);
    const node = locate(draft, '/steps/audit/activity');
    expect(draft.text.slice(node.from, node.to)).toBe('formatter.Record');
    // `nope` is code points 6..10 of `'é' + nope`, inside a double-quoted scalar.
    const expr = locate(draft, '/steps/audit/input/name', { start: 6, end: 10 });
    expect(draft.text.slice(expr.from, expr.to)).toBe('nope');
    // A place that is not written points at the key of its parent.
    const missing = locate(draft, '/steps/audit/undoInput');
    expect(draft.text.slice(missing.from, missing.to)).toBe('audit');
    const plain = nodeAt(draft.doc, '/result/text').node;
    expect(isScalar(plain) && exprRange(draft.text, plain, 7, 11)).toBeTruthy();
  });

  it('maps expression offsets through single quotes and escapes', () => {
    const quoted = parseDraft('binding', `hook: a.B\nresult: {x: 'a ''b'' + c', y: "q\\"r + d"}\n`);
    const single = nodeAt(quoted.doc, '/result/x').node,
      double = nodeAt(quoted.doc, '/result/y').node;
    if (!isScalar(single) || !isScalar(double)) throw new Error('scalars expected');
    const c = exprRange(quoted.text, single, 8, 9),
      d = exprRange(quoted.text, double, 6, 7);
    if (!c || !d) throw new Error('Expected expression ranges');
    expect(quoted.text.slice(c.from, c.to)).toBe('c');
    expect(quoted.text.slice(d.from, d.to)).toBe('d');
  });

  it('knows the path at an offset', () => {
    const draft = parseDraft('binding', binding);
    expect(pathAt(draft.doc, binding.indexOf('req.name') + 2)).toEqual({
      path: '/steps/format/input/name',
      onKey: false,
    });
    expect(pathAt(draft.doc, binding.indexOf('description') + 1).onKey).toBe(true);
  });

  it('edits the document and keeps comments', () => {
    const draft = parseDraft('binding', binding);
    const added = addStep(draft.definition, 'formatter.Format', { x: 10.4, y: 20.6 });
    expect(added.name).toBe('format2');
    const next = editAll(draft, [...added.changes, ...removeStep(draft.definition, 'format')]);
    expect(next).toContain('# the formatter does the work');
    const again = bindingOf(parseDraft('binding', next));
    expect(Object.keys(again.steps).sort()).toEqual(['audit', 'format2']);
    expect(again.steps.audit?.after).toEqual([]);
    expect(again.editor?.nodes.format2).toMatchObject({ x: 10, y: 21 });
    // A template's empty steps and an input given as one expression both become mappings when a field is set.
    const template = parseDraft('rule', templateYAML('rule', 'hello.Greeted'));
    expect(template.problems).toEqual([]);
    const withStep = parseDraft(
      'rule',
      editAll(template, [{ path: ['steps', 'r', 'activity'], value: 'formatter.Record' }]),
    );
    expect(ruleOf(withStep).steps.r?.activity).toBe('formatter.Record');
    const whole = parseDraft('binding', 'hook: a.B\nsteps:\n  s: {activity: x.Y, input: req}\n');
    const fields = parseDraft(
      'binding',
      editAll(whole, [{ path: ['steps', 's', 'input', 'name'], value: 'req.name' }]),
    );
    expect(toJson(api.BindingDefinitionSchema, bindingOf(fields))).toMatchObject({
      steps: { s: { input: { name: 'req.name' } } },
    });
  });

  it('names new steps freely', () => {
    expect(stepNameFor('formatter.Format', [])).toBe('format');
    expect(stepNameFor('x.String', [])).toBe('stringStep');
    expect(stepNameFor('x.Map', ['map', 'mapStep'])).toBe('mapStep2');
  });
});

describe('wiring completion', () => {
  const definition = fromJson(api.BindingDefinitionSchema, {
    hook: 'hello.Greet',
    steps: { format: { activity: 'formatter.Format' }, audit: { activity: 'formatter.Record' } },
  });

  it('finds the keys above a line by indentation', () => {
    const source = 'steps:\n  format:\n    input:\n      na';
    expect(parentKeys(source, source.lastIndexOf('\n') + 1, 6)).toEqual(['steps', 'format', 'input']);
  });

  it('suggests keys, contract names and schema fields', () => {
    const at = (source: string) =>
      optionsOf(completeYAML({ kind: 'binding', index, definition, text: source, position: source.length }));
    expect(at('ho')).toContain('hook');
    expect(at('steps:\n  format:\n    act')).toContain('activity');
    expect(at('steps:\n  format:\n    activity: form')).toEqual(['formatter.Format', 'formatter.Record']);
    expect(at('steps:\n  audit:\n    input:\n      ')).toEqual(['name', 'text']);
    expect(at('steps:\n  audit:\n    input:\n      text: format.')).toEqual(['text']);
    expect(at('result:\n  text: req.')).toEqual(['name']);
  });

  it('completes CEL variables and fields from the scope', () => {
    const scope = scopeOf(index, { hook: 'hello.Greet' }, definition.steps);
    expect(optionsOf(completeCEL('st', 2, scope))).toContain('steps');
    expect(optionsOf(completeCEL('steps.format.', 13, scope))).toEqual(['skipped']);
    const record = index.activities.get('formatter.Record');
    if (!record) throw new Error('Expected the Record activity in the catalog');
    expect(shapeAt(shapeOf(record.value.input), ['text'])).toEqual({ kind: 'string' });
  });
});

describe('wiring for-each', () => {
  const batch = `hook: hello.Batch
steps:
  each:
    forEach: req.people
    as: person
    steps:
      fmt: {activity: formatter.Format, input: {name: person.name}}
      rec: {activity: formatter.Record, after: [fmt], input: {name: person.name, text: fmt.text}}
    result: fmt.text
  greet: {activity: formatter.Format, forEach: req.people, input: {name: item.name}}
`;
  const draft = parseDraft('binding', batch),
    definition = bindingOf(draft);
  const labels = (result: ReturnType<typeof completeCEL>) => result?.options.map((option) => option.label);

  it('reads bodies and finds steps by id', () => {
    expect(draft.problems).toEqual([]);
    expect(stepAt(definition, 'each/rec').step?.after).toEqual(['fmt']);
    expect(Object.keys(stepAt(definition, 'each/rec').siblings).sort()).toEqual(['fmt', 'rec']);
    expect(stepPathOf('each/rec')).toEqual(['steps', 'each', 'steps', 'rec']);
    expect(framesOf(['steps', 'each', 'steps', 'fmt', 'input', 'name'], definition.steps)).toMatchObject({
      chain: [{ name: 'each' }, { name: 'fmt' }],
      rest: ['input', 'name'],
    });
    expect(toYAML('binding', definition).indexOf('forEach:')).toBeLessThan(
      toYAML('binding', definition).indexOf('steps:\n      fmt'),
    );
  });

  it('scopes the item and the body by place', () => {
    const scopeFor = (...path: string[]) => scopeAt(index, { hook: 'hello.Batch' }, definition.steps, path, 'binding');
    const inside = scopeFor('steps', 'each', 'steps', 'rec', 'input', 'text');
    expect(labels(completeCEL('person.', 7, inside))).toEqual(['name']);
    expect(labels(completeCEL('fmt.', 4, inside))).toEqual(['text']);
    expect(inside.variables.get('personIndex')?.shape.kind).toBe('int');
    // The list does not see the item; the result sees the body.
    expect(scopeFor('steps', 'each', 'forEach').variables.has('person')).toBe(false);
    expect(scopeFor('steps', 'each', 'result').variables.has('fmt')).toBe(true);
    expect(scopeFor('steps', 'greet', 'input', 'name').variables.get('item')?.shape.kind).toBe('object');
    // Outside, a for-each step is a list: of its activity's outputs, of dyn for a body.
    expect(outputShape(index, definition.steps.greet)).toMatchObject({ kind: 'list', elem: { kind: 'object' } });
    expect(outputShape(index, definition.steps.each)).toEqual({ kind: 'list', elem: { kind: 'dyn' } });
    expect(labels(completeCEL('steps.greet.', 12, scopeFor('result')))).toEqual(['skipped', 'failed', 'errors']);
  });

  it('completes keys and fields inside a body', () => {
    const at = (source: string) =>
      completeYAML({ kind: 'binding', index, definition, text: source, position: source.length })?.options.map(
        (option) => option.label,
      );
    expect(
      at(
        'steps:\n  each:\n    forEach: req.people\n    as: person\n    steps:\n      fmt:\n        input:\n          name: person.',
      ),
    ).toEqual(['name']);
    expect(at('steps:\n  each:\n    forEach: req.people\n    con')).toContain('concurrency');
    expect(at('steps:\n  greet:\n    onError: ')).toEqual(['fail', 'continue']);
  });

  it('adds and removes steps of a body', () => {
    const added = addStep(definition, 'formatter.Format', { x: 1, y: 2 }, 'each');
    expect(added).toMatchObject({ name: 'format', id: 'each/format' });
    const next = bindingOf(
      parseDraft('binding', editAll(draft, [...added.changes, ...removeStep(definition, 'each/fmt')])),
    );
    expect(Object.keys(next.steps.each?.steps ?? {}).sort()).toEqual(['format', 'rec']);
    expect(next.steps.each?.steps.rec?.after).toEqual([]);
    expect(next.editor?.nodes['each/format']).toMatchObject({ x: 1, y: 2 });
  });
});
