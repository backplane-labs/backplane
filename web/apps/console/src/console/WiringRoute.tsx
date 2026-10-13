import { useSearchParams } from 'react-router-dom';
import { WiringWorkspace, type WiringState, type WiringTarget, type WiringView } from '@gopherex/backplane-platform-ui';
import type { ThemeMode } from '@gopherex/backplane-theme';

const views: WiringView[] = ['graph', 'yaml', 'versions', 'test', 'runs'];

/** The URL of Wiring opened on a binding or rule. */
export function wiringLink(target?: WiringTarget, view?: WiringView): string {
  const params = new URLSearchParams();
  if (target?.kind === 'binding') params.set('binding', target.hook);
  else if (target?.kind === 'rule' && target.id) params.set('rule', target.id);
  else if (target?.kind === 'rule' && target.event) params.set('event', target.event);
  if (view) params.set('view', view);
  const query = params.toString();
  return query ? `/wiring?${query}` : '/wiring';
}

/** Wiring with the open binding or rule and the editor view kept in the URL. */
export function WiringRoute({ mode }: { mode: ThemeMode }) {
  const [params, setParams] = useSearchParams();
  const binding = params.get('binding'), rule = params.get('rule'), event = params.get('event'), view = params.get('view');
  const state: WiringState = {
    target: binding ? { kind: 'binding', hook: binding } : rule ? { kind: 'rule', id: rule } : event ? { kind: 'rule', id: '', event } : undefined,
    view: views.includes(view as WiringView) ? view as WiringView : 'graph',
  };
  // Opening another item is a navigation (back returns to it); switching the view replaces the entry.
  const change = (next: WiringState) => {
    const url = new URL(wiringLink(next.target, next.view), location.origin);
    setParams(url.searchParams, { replace: next.target === state.target || JSON.stringify(next.target) === JSON.stringify(state.target) });
  };
  return <WiringWorkspace mode={mode} state={state} onStateChange={change} />;
}
