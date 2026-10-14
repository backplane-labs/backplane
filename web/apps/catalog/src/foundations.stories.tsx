import { useRef, useState } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { CircleCheck } from 'lucide-react';
import { tokens } from '@backplane-labs/backplane-theme';
import { Button, ErrorBoundary, FeatureBadge, FilterPill, Icon, Segment, Stack, UserAvatar, UsersIndicator, useClickOutside, useDelayedSwitch } from '@backplane-labs/backplane-ui';
function Failure({ fail }: { fail: boolean }) { if (fail) throw new Error('Expected catalog boundary failure'); return <p>Recovered content</p>; }
function Utilities() {
  const [filter, setFilter] = useState(true), [selection, setSelection] = useState<string[]>([]), [fail, setFail] = useState(false), [outside, setOutside] = useState(0);
  const ref = useRef<HTMLDivElement>(null), pending = useDelayedSwitch(fail);
  useClickOutside([ref], () => setOutside((count) => count + 1));
  return <Stack><h1>Kit utilities</h1><div ref={ref}><UserAvatar name="Alex Operator" /><UsersIndicator label="Collaborators" users={Array.from({ length: 6 }, (_, id) => ({ id: String(id), name: `Operator ${id}` }))} max={3} />
    <Icon icon={CircleCheck} label="Ready" /><FeatureBadge>Preview</FeatureBadge>
    {filter && <FilterPill label="service.name" value="hello" onRemove={() => setFilter(false)} />}
    <Segment label="Source segment" options={[{ value: 'hello', label: 'Hello' }, { value: 'kratos', label: 'Kratos' }]} values={selection} onValuesChange={setSelection} />
  </div><output aria-label="Outside clicks">{outside}</output>
    <Button onClick={() => setFail(true)}>Fail subtree</Button>
    <ErrorBoundary resetKey={fail} fallback={(_error, reset) => <div role="alert">The module could not render. <Button onClick={() => { setFail(false); reset(); }}>Recover</Button></div>}><Failure fail={fail} /></ErrorBoundary>
    {pending && <span role="status">Delayed failure indicator</span>}
  </Stack>;
}
export default { title: 'Kit/Foundations' } satisfies Meta;
type Story = StoryObj;
export const UtilitiesAndRecovery: Story = { render: () => <Utilities /> };
export const TokensAndTypography: Story = { render: (_, context) => <Stack><h1>Typography and tokens</h1><p>Dense operational interfaces use IBM Plex Sans, with IBM Plex Mono for identifiers and data.</p>
  <p style={{ fontFamily: 'var(--font-mono)' }}>trace_id=0123456789abcdef</p><div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 8 }}>{Object.entries(tokens[context.globals.theme === 'light' ? 'light' : 'dark']).map(([key, value]) => <div key={key} style={{ border: '1px solid var(--border)', borderRadius: 4, padding: 8 }}><span aria-hidden="true" style={{ display: 'inline-block', width: 24, height: 24, background: value }} /> <span>{key}: {value}</span></div>)}</div>
  <p>Use native keyboard interactions, visible focus and descriptive labels. State descriptions use plain English and name the next available action. Color always has a text or structural counterpart.</p>
</Stack> };
