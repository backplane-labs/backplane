import {
  AuditExplorer,
  type AuditState,
  ConfigurationPanel,
  defaultAuditState,
  defaultErrorsState,
  ErrorsExplorer,
  type ErrorsState,
  ExplorePanel,
  ServiceCatalog,
  ServiceInspector,
} from '@backplane-labs/backplane-platform-ui';
import { BackplaneProvider } from '@backplane-labs/backplane-react';
import { Button, Checkbox, Stack } from '@backplane-labs/backplane-ui';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { type ReactNode, useEffect, useMemo, useState } from 'react';
import { PlatformFixture } from './platform-fixture';

function Fixture({ children }: { children: ReactNode }) {
  const runtime = useMemo(() => new PlatformFixture(), []),
    [tick, setTick] = useState(0),
    [fail, setFail] = useState(false);
  useEffect(() => {
    const timer = setInterval(() => setTick((tick) => tick + 1), 100);
    return () => clearInterval(timer);
  }, []);
  void tick;
  return (
    <BackplaneProvider client={runtime}>
      <Stack>
        <h1>Platform API compositions</h1>
        <p>Explicit local transport fixtures</p>
        <div style={{ display: 'flex', gap: 8 }}>
          <label htmlFor="fixture-fail-writes" style={{ display: 'flex', gap: 8 }}>
            <Checkbox
              id="fixture-fail-writes"
              checked={fail}
              onCheckedChange={(value) => {
                setFail(value === true);
                runtime.failWrites = value === true;
              }}
            />
            Simulate write failure
          </label>
          <Button onClick={() => runtime.reconnect()}>Reconnect fixture</Button>
          <Button onClick={() => runtime.logout()}>Replace session</Button>
        </div>
        {children}
        <output aria-label="Write attempts">{runtime.writes}</output>
        <output aria-label="Sent input">{runtime.lastInput}</output>
        <output aria-label="Active watches">{runtime.active}</output>
      </Stack>
    </BackplaneProvider>
  );
}
function Services({ mode }: { mode: 'dark' | 'light' }) {
  const [service, setService] = useState('hello');
  return (
    <>
      <ServiceCatalog onSelect={(service) => setService(service.name)} />
      <ServiceInspector service={service} mode={mode} />
    </>
  );
}
export default {
  title: 'Kit/Platform',
  decorators: [
    (Story) => (
      <Fixture>
        <Story />
      </Fixture>
    ),
  ],
} satisfies Meta;
type Story = StoryObj;
const mode = (context: { globals: { theme?: string } }) =>
  context.globals.theme === 'light' ? ('light' as const) : ('dark' as const);
export const ServicesAndHealth: Story = { render: (_, context) => <Services mode={mode(context)} /> };
export const Configuration: Story = {
  render: (_, context) => <ConfigurationPanel service="hello" mode={mode(context)} />,
};
function AuditPage({ mode }: { mode: 'dark' | 'light' }) {
  const [state, setState] = useState<AuditState>({
    ...defaultAuditState,
    range: { from: '2026-09-30T11:55:00Z', to: '2026-09-30T12:10:00Z', timeZone: 'utc' },
    live: 0,
  });
  return (
    <div style={{ height: 820 }}>
      <AuditExplorer mode={mode} state={state} onStateChange={setState} onOpenTrace={() => undefined} />
    </div>
  );
}
export const Audit: Story = { render: (_, context) => <AuditPage mode={mode(context)} /> };
function ErrorsPage({ mode }: { mode: 'dark' | 'light' }) {
  const [state, setState] = useState<ErrorsState>({
    ...defaultErrorsState,
    range: { from: '2026-09-30T11:55:00Z', to: '2026-09-30T12:10:00Z', timeZone: 'utc' },
    live: 0,
  });
  return (
    <div style={{ height: 820 }}>
      <ErrorsExplorer mode={mode} state={state} onStateChange={setState} />
    </div>
  );
}
export const Errors: Story = { render: (_, context) => <ErrorsPage mode={mode(context)} /> };
export const Explore: Story = { render: (_, context) => <ExplorePanel mode={mode(context)} /> };
