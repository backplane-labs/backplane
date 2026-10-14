import { useCallback, useState } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { useForm } from 'react-hook-form';
import {
  Button, Combobox, TagsInput, Cascader, NumberInput, SecretInput, SearchInput, ConfirmAction, ClipboardButton,
  FileUpload, DataTable, ResourceTree, Grid, Stack, Text, TextLink, StateMessage, DateTimeInput, Toaster, toast,
  TimeRangeControl, RefreshControl, type TimeRangeValue,
  AutoSaveInput, AutoSizeInput, AutoSizeTextarea, FormControl, Input, Toolbar,
  Checkbox, Switch, Select, SelectTrigger, SelectValue, SelectContent, SelectItem, Spinner,
  type ChoiceOption, type DataColumn,
} from '@backplane-labs/backplane-ui';

function ControlsFixture() {
  const [number, setNumber] = useState('9007199254740993.125'), [secret, setSecret] = useState('token'), [query, setQuery] = useState('');
  const [choice, setChoice] = useState<string[]>([]), [tags, setTags] = useState(['production']), [path, setPath] = useState<string[]>([]);
  const [confirmed, setConfirmed] = useState(false);
  const loadOptions = useCallback(async (query: string, signal: AbortSignal): Promise<ChoiceOption[]> => {
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(resolve, query === 'slow' ? 400 : 30);
      signal.addEventListener('abort', () => { clearTimeout(timer); reject(new DOMException('Aborted', 'AbortError')); }, { once: true });
    });
    if (query === 'error') throw new Error('fixture');
    return [{ value: `${query || 'hello'}-1`, label: `${query || 'hello'} first` }, { value: `${query || 'hello'}-2`, label: `${query || 'hello'} second` }];
  }, []);
  return <Stack><h1>Composed controls</h1><Grid>
    <Stack><h2>Precision and secrets</h2><NumberInput label="Exact amount" value={number} onValueChange={setNumber} step="0.001" unit="bytes" /><Text mono>{number}</Text>
      <SecretInput label="Token" value={secret} onValueChange={setSecret} />
      <DateTimeInput label="Calendar date" kind="date" defaultValue="2026-09-29" />
      <SearchInput label="Search services" value={query} onValueChange={setQuery} />
    </Stack>
    <Stack><h2>Selection</h2><Combobox label="Remote service" values={choice} onValuesChange={setChoice} multiple loadOptions={loadOptions} />
      <TagsInput label="Environment" values={tags} onValuesChange={setTags} />
      <Cascader label="Region" values={path} onValuesChange={setPath} nodes={[{ value: 'eu', label: 'Europe', children: [{ value: 'eu-1', label: 'Zone 1' }, { value: 'eu-2', label: 'Zone 2' }] }]} />
    </Stack>
  </Grid><Stack direction="row"><ClipboardButton value={number} />
    <ConfirmAction trigger="Remove source" title="Remove source?" description="This action affects the local fixture." onConfirm={() => setConfirmed(true)} />
    <Button onClick={() => toast('Configuration saved')}>Show notification</Button>
  </Stack>{confirmed && <p role="status">Source removed</p>}
    <StateMessage title="Partial results" tone="partial">Some sources are unavailable. <TextLink href="#details">View details</TextLink></StateMessage>
    <Toaster />
  </Stack>;
}

type Entry = { id: string; name: string; sequence: bigint; state: string; children?: Entry[] };
const records: Entry[] = Array.from({ length: 10000 }, (_, index) => ({ id: `row-${index}`, name: `Service ${index}`, sequence: 9007199254740993n + BigInt(index), state: index % 2 ? 'ready' : 'starting' }));
const columns: DataColumn<Entry>[] = [
  { id: 'name', label: 'Service', value: (row) => row.name, width: 260 },
  { id: 'sequence', label: 'Sequence', value: (row) => row.sequence, width: 260 },
  { id: 'state', label: 'State', value: (row) => row.state },
];
const rowId = (row: Entry) => row.id;
function TableFixture({ paginated = false }: { paginated?: boolean }) {
  const [reverse, setReverse] = useState(false), [selected, setSelected] = useState<string[]>([]);
  const data = reverse ? [...records].reverse() : records;
  return <Stack><h1>Large table</h1><Button onClick={() => setReverse(!reverse)}>Reverse data</Button>
    <DataTable label="Services" data={data} columns={columns} getRowId={rowId} selectable onSelectionChange={setSelected} pageSize={paginated ? 25 : undefined} />
    <output aria-label="Selected IDs">{selected.join(', ')}</output>
  </Stack>;
}
const nodes = [{ id: 'root', label: 'Services', children: records.map((row) => ({ id: row.id, label: row.name })) }];
function TreeFixture() {
  const [selected, setSelected] = useState<string[]>([]);
  return <Stack><h1>Resource tree</h1><ResourceTree label="Resources" nodes={nodes} selected={selected} onSelectionChange={setSelected} defaultExpanded={['root']} />
    <output aria-label="Selected resource">{selected.join(', ')}</output>
  </Stack>;
}
function UploadFixture() {
  return <Stack><h1>File upload</h1><FileUpload label="Configuration files" accept=".json" maxBytes={1024} upload={async (_, { signal, progress }) => {
    for (let percent = 0; percent <= 100; percent += 20) {
      if (signal.aborted) return;
      progress(percent); await new Promise((resolve) => setTimeout(resolve, 60));
    }
  }} /></Stack>;
}

function TimeFixture({ mode }: { mode: 'dark' | 'light' }) {
  const [value, setValue] = useState<TimeRangeValue>({ from: 'now-15m', to: 'now', timeZone: 'utc', weekStart: 'monday' });
  const [interval, setInterval] = useState(0), [refreshes, setRefreshes] = useState(0);
  return <Stack><h1>Time controls</h1><TimeRangeControl label="Time range" value={value} onChange={setValue} mode={mode} />
    <RefreshControl interval={interval} onIntervalChange={setInterval} onRefresh={() => setRefreshes((value) => value + 1)} />
    <output aria-label="Refresh count">{refreshes}</output><pre aria-label="Selected time range">{JSON.stringify(value, null, 2)}</pre>
  </Stack>;
}

function FormsFixture() {
  const form = useForm({ defaultValues: { name: '' }, mode: 'onChange' });
  const [saved, setSaved] = useState(''), [description, setDescription] = useState('One line'), [attempt, setAttempt] = useState(0);
  return <Stack><h1>Form compositions</h1>
    <form onSubmit={form.handleSubmit((values) => setSaved(values.name))} style={{ display: 'grid', gap: 12 }}>
      <FormControl control={form.control} name="name" label="Name" rules={{ required: 'A name is required', minLength: { value: 3, message: 'Use at least three characters' } }}>{(field, accessibility) => <Input {...field} {...accessibility} />}</FormControl>
      <Button type="submit">Save name</Button><output aria-label="Saved name">{saved}</output>
    </form>
    <AutoSaveInput label="Automatic save" defaultValue="hello" onSave={async (value) => { setAttempt((old) => old + 1); if (attempt === 0) throw new Error('fixture'); setSaved(value); }} />
    <AutoSizeInput aria-label="Compact name" value={saved} onChange={(event) => setSaved(event.target.value)} />
    <AutoSizeTextarea aria-label="Description" value={description} onChange={(event) => setDescription(event.target.value)} />
    <Toolbar label="Form actions"><Button type="button">First action</Button><Button type="button">Second action</Button></Toolbar>
  </Stack>;
}

function StatesFixture() {
  const noop = () => {};
  return <Stack><h1>Control states</h1><Grid>
    <Stack><h2>Unavailable and read-only</h2><Button disabled>Disabled action</Button><Button disabled><Spinner />Saving</Button>
      <Input aria-label="Disabled field" disabled value="Unavailable" /><Input aria-label="Read-only field" readOnly value="Read only" />
      <NumberInput label="Fixed amount" value="9007199254740993" onValueChange={noop} readOnly />
      <SecretInput label="Read-only token" value="should-never-appear" onValueChange={noop} readOnly />
      <Checkbox aria-label="Disabled checkbox" disabled checked /><Switch aria-label="Disabled switch" disabled checked />
      <Select disabled defaultValue="logs"><SelectTrigger aria-label="Disabled signal"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="logs">Logs</SelectItem></SelectContent></Select>
      <Combobox label="Disabled choices" options={[]} values={[]} onValuesChange={noop} disabled />
      <TagsInput label="Read-only tags" values={['production']} onValuesChange={noop} readOnly />
    </Stack>
    <Stack><h2>Empty and failure</h2><Combobox label="Empty choices" options={[]} values={[]} onValuesChange={noop} />
      <StateMessage title="Offline" tone="offline" onRetry={noop}>The last known values are retained.</StateMessage>
      <StateMessage title="Request failed" tone="error" onRetry={noop}>Try again when the service is available.</StateMessage>
      <DataTable label="Empty services" data={[]} columns={columns} getRowId={rowId} height={160} partial />
      <ResourceTree label="Disabled resource" nodes={[{ id: 'disabled', label: 'Unavailable service', disabled: true }]} selected={[]} onSelectionChange={noop} height={100} />
      <FileUpload label="Disabled upload" disabled upload={async () => {}} />
    </Stack>
  </Grid></Stack>;
}

export default { title: 'Kit/Compositions' } satisfies Meta;
type Story = StoryObj;
export const Controls: Story = { render: () => <ControlsFixture /> };
export const LargeTable: Story = { render: () => <TableFixture /> };
export const PaginatedTable: Story = { render: () => <TableFixture paginated /> };
export const Tree: Story = { render: () => <TreeFixture /> };
export const Upload: Story = { render: () => <UploadFixture /> };
export const Time: Story = { render: (_, context) => <TimeFixture mode={context.globals.theme === 'light' ? 'light' : 'dark'} /> };
export const Forms: Story = { render: () => <FormsFixture /> };
export const States: Story = { render: () => <StatesFixture /> };
