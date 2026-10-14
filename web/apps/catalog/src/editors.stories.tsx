import { useCallback, useState } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { CodeEditor, DiffViewer, EditorActions, InlineCodeEditor, JSONViewer, QueryEditor, type CodeEditorProps, type QueryLanguage } from '@backplane-labs/backplane-editors';
import { Stack } from '@backplane-labs/backplane-ui';

function Queries({ mode }: { mode: 'dark' | 'light' }) {
  const [value, setValue] = useState('service.name'), [language, setLanguage] = useState<QueryLanguage>('logsql'), [runs, setRuns] = useState(0), [aborts, setAborts] = useState(0);
  const complete = useCallback<NonNullable<CodeEditorProps['complete']>>(async ({ value, signal }) => {
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(resolve, value.includes('slow') ? 800 : 50);
      signal.addEventListener('abort', () => { clearTimeout(timer); setAborts((count) => count + 1); reject(new DOMException('Aborted', 'AbortError')); }, { once: true });
    });
    if (value.includes('unavailable')) throw new Error('fixture');
    return [{ label: value.includes('fast') ? 'fast.source' : 'service.name', type: 'property', detail: 'Stored attribute' }];
  }, []);
  const validate = useCallback<NonNullable<CodeEditorProps['validate']>>(async (value) => value.includes('invalid') ? [{ from: 0, to: 7, severity: 'error', message: 'Unknown field: invalid' }] : [], []);
  return <Stack><h1>Query editors</h1><QueryEditor label="Telemetry query" value={value} onChange={setValue} mode={mode} language={language} onLanguageChange={setLanguage}
    languages={['cel', 'promql', 'metricsql', 'logsql', 'traceql']} complete={complete} validate={validate} onSubmit={() => setRuns((count) => count + 1)} />
    <output aria-label="Query runs">{runs}</output><output aria-label="Cancelled suggestions">{aborts}</output><EditorActions value={value} filename="query.txt" />
    <InlineCodeEditor label="Inline CEL" value="size(events) > 0" language="cel" mode={mode} readOnly />
  </Stack>;
}
function JSONFixture({ mode }: { mode: 'dark' | 'light' }) {
  const [value, setValue] = useState('{"sequence":18446744073709551615}');
  return <Stack><h1>JSON and diff</h1><CodeEditor label="JSON document" value={value} onChange={setValue} language="json" mode={mode} />
    <output aria-label="Raw JSON">{value}</output><JSONViewer label="Original value" value={{ sequence: 18446744073709551615n, text: '<script>alert(1)</script>' }} mode={mode} />
    <DiffViewer label="Configuration diff" before={'{\n  "replicas": 2\n}'} after={'{\n  "replicas": 3\n}'} language="json" mode={mode} />
  </Stack>;
}
export default { title: 'Kit/Editors' } satisfies Meta;
type Story = StoryObj;
export const Query: Story = { render: (_, context) => <Queries mode={context.globals.theme === 'light' ? 'light' : 'dark'} /> };
export const JSONAndDiff: Story = { render: (_, context) => <JSONFixture mode={context.globals.theme === 'light' ? 'light' : 'dark'} /> };
