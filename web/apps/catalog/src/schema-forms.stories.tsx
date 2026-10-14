import { useState } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { create } from '@bufbuild/protobuf';
import { SchemaSchema, type Baked } from '@gopherex/schemapb';
import { SchemaForm, SchemaValuesPreview } from '@backplane-labs/backplane-schema-forms';
import { Checkbox, Label } from '@backplane-labs/backplane-ui';

const schema = create(SchemaSchema, {
  id: { namespace: 'catalog', name: 'service', version: '1' },
  defs: { database: { id: { name: 'database' }, fields: [
    { name: 'host', kind: { case: 'string', value: { default: 'localhost', minLen: 1n } } },
  ] } },
  fields: [
    { name: 'name', title: 'Service name', required: true, kind: { case: 'string', value: { default: 'hello', minLen: 2n } } },
    { name: 'sequence', title: 'Sequence', kind: { case: 'uint64', value: { default: 18446744073709551615n } } },
    { name: 'replicas', title: 'Replicas', kind: { case: 'int64', value: { default: 2n, gte: 1n, lte: 10n } } },
    { name: 'capacity', title: 'Capacity', kind: { case: 'computed', value: { expr: 'root.replicas * 128' } }, unit: 'MB' },
    { name: 'advanced', title: 'Advanced', kind: { case: 'bool', value: { default: false } } },
    { name: 'region', title: 'Region', when: 'root.advanced', kind: { case: 'choice', value: {
      default: { kind: { case: 'stringValue', value: 'eu' } }, optionsExpr: 'root.replicas > 5 ? ["us"] : ["eu", "us"]',
    } } },
    { name: 'password', title: 'Access token', secret: true, kind: { case: 'string', value: { default: 'fixture-secret' } } },
    { name: 'database', title: 'Database', kind: { case: 'ref', value: { target: { case: 'name', value: 'database' }, default: {} } } },
    { name: 'labels', title: 'Labels', kind: { case: 'map', value: { valueField: { name: 'value', kind: { case: 'string', value: {} } } } } },
    { name: 'ports', title: 'Ports', kind: { case: 'list', value: { items: [{ kind: { case: 'uint32', value: { gte: 1, lte: 65535 } } }] } } },
    { name: 'destination', title: 'Destination', kind: { case: 'oneOf', value: { discriminator: 'kind', variants: {
      http: { id: { name: 'http' }, fields: [{ name: 'url', kind: { case: 'string', value: { default: 'https://example.test', format: 'url' } } }] },
      queue: { id: { name: 'queue' }, fields: [{ name: 'subject', kind: { case: 'string', value: { default: 'events.hello' } } }] },
    } } } },
    { name: 'metadata', title: 'Metadata', kind: { case: 'json', value: {} }, nullable: true },
  ],
});

function Fixture({ readOnly = false }: { readOnly?: boolean }) {
  const [saved, setSaved] = useState<Baked>();
  const [fail, setFail] = useState(false);
  return <div style={{ display: 'grid', gap: 24 }}>
    <h1>Schema forms</h1>
    <p>Native validation, conditional fields and lossless values.</p>
    {!readOnly && <div style={{ display: 'flex', gap: 8 }}><Checkbox id="simulate-failure" checked={fail} onCheckedChange={(value) => setFail(value === true)} /><Label htmlFor="simulate-failure">Simulate save failure</Label></div>}
    <SchemaForm schema={schema} label="Service configuration" readOnly={readOnly} initialValues={{ labels: { env: 'dev' }, ports: [8080n], destination: { kind: 'http' }, metadata: { enabled: true } }}
      onSubmit={async (baked, signal) => {
        await new Promise((resolve) => setTimeout(resolve, 50));
        if (signal.aborted) return;
        if (fail) throw new Error('Simulated failure');
        setSaved(baked);
      }} />
    {saved && <section><h2>Saved configuration</h2><SchemaValuesPreview schema={schema} values={saved.values} /></section>}
  </div>;
}

export default { title: 'Kit/Schema forms', component: Fixture } satisfies Meta<typeof Fixture>;
type Story = StoryObj<typeof Fixture>;
export const Editable: Story = {};
export const ReadOnly: Story = { args: { readOnly: true } };
