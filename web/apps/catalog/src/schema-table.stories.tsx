import { useState } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { create } from '@bufbuild/protobuf';
import { timestampFromMs } from '@bufbuild/protobuf/wkt';
import { SchemaSchema, type NativeStruct } from '@gopherex/schemapb';
import { SchemaTable, useCursorPages, type CursorPage } from '@backplane-labs/backplane-schema-forms';
import { Button } from '@backplane-labs/backplane-ui';

/** The item of a list method's response: one device. */
const schema = create(SchemaSchema, {
  id: { namespace: 'catalog', name: 'device', version: '1' },
  fields: [
    { name: 'id', title: 'ID', kind: { case: 'bytes', value: {} } },
    { name: 'name', title: 'Name', kind: { case: 'string', value: {} } },
    { name: 'status', title: 'Status', kind: { case: 'choice', value: { options: [
      { value: { kind: { case: 'stringValue', value: 'STATUS_ACTIVE' } }, label: 'Active' },
      { value: { kind: { case: 'stringValue', value: 'STATUS_REVOKED' } }, label: 'Revoked' },
    ] } } },
    { name: 'sessions', title: 'Sessions', kind: { case: 'uint64', value: {} } },
    { name: 'trusted', title: 'Trusted', kind: { case: 'bool', value: {} } },
    { name: 'seen_at', title: 'Last seen', kind: { case: 'timestamp', value: {} } },
    { name: 'push_token', title: 'Push token', secret: true, kind: { case: 'string', value: {} } },
    { name: 'labels', title: 'Labels', kind: { case: 'map', value: {} } },
  ],
});

function device(n: number): NativeStruct {
  const id = new Uint8Array(16); id[15] = n; id[0] = 0xde;
  return {
    id, name: `Device ${n}`, status: n % 3 === 0 ? 'STATUS_REVOKED' : 'STATUS_ACTIVE',
    sessions: 18446744073709551615n - BigInt(n), trusted: n % 2 === 0, seen_at: timestampFromMs(Date.UTC(2026, 9, 1, 12, n)),
    push_token: `token-${n}`, labels: { os: n % 2 ? 'ios' : 'android' },
  };
}

/** A fake list method with cursors: 3 pages of 4. */
async function list(cursor: string | undefined): Promise<CursorPage<NativeStruct>> {
  await new Promise((resolve) => setTimeout(resolve, 30));
  const page = cursor ? Number(cursor) : 0;
  return { items: Array.from({ length: 4 }, (_, i) => device(page * 4 + i + 1)), nextCursor: page < 2 ? String(page + 1) : '' };
}

function Fixture() {
  const pages = useCursorPages((cursor) => list(cursor), []);
  const [opened, setOpened] = useState<string>();
  return <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: 12 }}>
    <SchemaTable schema={schema} rows={pages.rows} label="Devices" loading={pages.loading && !pages.rows.length} error={pages.error} height={320}
      columns={['name', 'status', { id: 'os', label: 'OS', value: (row) => String((row.labels as NativeStruct | undefined)?.os ?? '') }, 'sessions', 'trusted', 'seen_at', 'id', 'push_token']}
      actions={(row) => <Button type="button" size="sm" variant="ghost" onClick={() => setOpened(String(row.name))}>Open</Button>}
      more={{ hasMore: pages.hasMore, loading: pages.loading && pages.rows.length > 0, onMore: pages.more }} />
    <p role="status">{opened ? `Opened ${opened}` : 'Nothing opened'}</p>
  </div>;
}

export default { title: 'Kit/Schema table', component: Fixture } satisfies Meta<typeof Fixture>;
type Story = StoryObj<typeof Fixture>;
export const Paginated: Story = {};
