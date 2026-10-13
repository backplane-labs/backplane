import { useEffect, useRef, useState, type CSSProperties } from 'react';
import { NavLink, Link, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Box, Boxes, ChevronRight, ChevronsLeft, Compass, History, PanelLeftOpen, Settings2, Workflow, Bug, ServerCog } from 'lucide-react';
import { Button, Input, StatusDot } from '@gopherex/backplane-ui';
import { healthTone } from './services/model';
import type { ServiceSummary } from '@gopherex/backplane-api';
import type { ModuleEntry } from './registry';

const MIN = 224, MAX = 400, DEFAULT = 264, KEY = 'backplane.navigation';
function readPreferences() {
  try {
    const value = JSON.parse(localStorage.getItem(KEY) ?? '{}');
    return { pinned: typeof value.pinned === 'boolean' ? value.pinned : true, width: Number.isFinite(value.width) ? Math.max(MIN, Math.min(MAX, value.width)) : DEFAULT };
  } catch { return { pinned: true, width: DEFAULT }; }
}

function ServiceIcon({ tone }: { tone: Parameters<typeof StatusDot>[0]['tone'] }) {
  return <span className="console-service-icon"><Box size={16} /><StatusDot tone={tone} /></span>;
}

export default function Sidebar({ services, modules, mobile, onClose }: { services: ServiceSummary[]; modules: Record<string, ModuleEntry>; mobile: boolean; onClose: () => void }) {
  const { t, i18n } = useTranslation('console'), location = useLocation();
  const [preferences, setPreferences] = useState(readPreferences), [hover, setHover] = useState(false), [focus, setFocus] = useState(false);
  const [filter, setFilter] = useState(''), [closed, setClosed] = useState<Set<string>>(new Set());
  const aside = useRef<HTMLElement>(null), drag = useRef<{ x: number; width: number } | null>(null);
  const expanded = preferences.pinned || hover || focus || mobile;
  useEffect(() => { try { localStorage.setItem(KEY, JSON.stringify(preferences)); } catch { /* Preferences are optional. */ } }, [preferences]);
  useEffect(() => { onClose(); }, [location.pathname]); // Navigation closes the mobile drawer.
  useEffect(() => {
    if (!mobile) return;
    const previous = document.activeElement as HTMLElement | null;
    aside.current?.querySelector<HTMLElement>('a,button')?.focus();
    return () => previous?.focus();
  }, [mobile]);
  useEffect(() => {
    const service = /^\/s\/([^/]+)/.exec(location.pathname)?.[1];
    if (service) setClosed((old) => { const next = new Set(old); next.delete(decodeURIComponent(service)); return next; });
  }, [location.pathname]);
  const resize = (width: number) => setPreferences((old) => ({ ...old, width: Math.max(MIN, Math.min(MAX, width)), pinned: true }));
  const fixed = [{ to: '/services', key: 'services', icon: Boxes }, { to: '/wiring', key: 'wiring', icon: Workflow }, { to: '/explore', key: 'explore', icon: Compass }, { to: '/errors', key: 'errors', icon: Bug }, { to: '/audit', key: 'audit', icon: History }, { to: '/infrastructure', key: 'infrastructure', icon: ServerCog }];
  return <>
    {mobile && <button className="console-nav-backdrop" aria-label={t('close')} onClick={onClose} />}
    <div className="console-nav-slot" data-pinned={preferences.pinned} data-mobile={mobile} style={{ '--navigation-width': `${preferences.width}px` } as CSSProperties}>
      <aside ref={aside} id="console-navigation" className="console-sidebar" data-expanded={expanded} aria-label={t('navigation')}
        onMouseEnter={() => setHover(true)} onMouseLeave={() => { if (!drag.current) setHover(false); }} onFocus={() => setFocus(true)}
        onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) setFocus(false); }}
        onKeyDown={(event) => {
          if (event.key === 'Escape') { onClose(); setHover(false); document.getElementById('console-nav-toggle')?.focus(); }
          if (mobile && event.key === 'Tab') {
            const elements = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('a,button,input,[tabindex="0"]')).filter((element) => element.getClientRects().length > 0);
            const first = elements[0], last = elements.at(-1);
            if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
            if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
          }
        }}>
        <nav aria-label={t('platform')} className="console-fixed-nav">
          <span className="console-group-caption console-nav-label">{t('platform')}</span>
          {fixed.map(({ to, key, icon: Icon }) => <NavLink key={key} to={to} end className="console-nav-item" aria-label={t(key)} title={!expanded ? t(key) : undefined}><Icon size={16} /><span className="console-nav-label">{t(key)}</span></NavLink>)}
        </nav>
        <div className="console-tree-caption"><span className="console-nav-label">{t('servicePages')}</span><span className="console-count">{services.length}</span></div>
        {expanded && <div className="console-nav-search"><Input aria-label={t('filter')} placeholder={t('filter')} value={filter} onChange={(event) => setFilter(event.target.value)} /></div>}
        <nav className="console-service-tree" aria-label={t('servicePages')}>
          {services.filter((service) => !expanded || service.name.toLowerCase().includes(filter.toLowerCase())).map((service) => {
            const entry = modules[service.name], plugin = entry?.plugin, opened = !closed.has(service.name);
            const active = [`/s/${service.name}`, `/services/${service.name}`].some((prefix) => location.pathname === prefix || location.pathname.startsWith(`${prefix}/`));
            return <div key={service.name} className="console-service-branch" data-active={active}>
              <div className="console-service-row">
                {service.ui ? <button className="console-nav-item console-branch-button" aria-label={service.name} aria-expanded={expanded && opened} onClick={() => { setPreferences((old) => ({ ...old, pinned: true })); setClosed((old) => { const next = new Set(old); if (next.has(service.name)) next.delete(service.name); else next.add(service.name); return next; }); }}>
                  <ServiceIcon tone={healthTone[service.health]} /><span className="console-nav-label">{service.name}</span><ChevronRight size={14} className="console-chevron" data-open={opened} />
                </button> : <NavLink to={`/services/${service.name}`} className="console-nav-item" aria-label={service.name} title={service.name}><ServiceIcon tone={healthTone[service.health]} /><span className="console-nav-label">{service.name}</span></NavLink>}
                {expanded && service.ui && <Link className="console-manage" to={`/services/${service.name}`} aria-label={t('admin', { service: service.name })} title={t('admin', { service: service.name })}><Settings2 size={13} /></Link>}
              </div>
              {expanded && opened && service.ui && <div className="console-leaves">
                {plugin ? plugin.navigation.nav.map((page) => <NavLink key={page.path} to={`/s/${service.name}/${page.path}`} end={page.path === ''} className="console-page-link">{i18n.t(page.labelKey, { ns: `module.${service.name}` })}</NavLink>) : <span className="console-tree-note">{t(entry?.error ? 'failed' : entry ? 'loading' : 'unavailable')}</span>}
                {plugin?.navigation.nav.length === 0 && <span className="console-tree-note">{t('noPages')}</span>}
              </div>}
            </div>;
          })}
          {expanded && !services.some((service) => service.name.toLowerCase().includes(filter.toLowerCase())) && <p className="console-tree-note">{t(services.length ? 'empty' : 'noServices')}</p>}
        </nav>
        <div className="console-sidebar-footer"><Button variant="ghost" size="icon" aria-label={t(preferences.pinned ? 'unpin' : 'pin')} aria-pressed={preferences.pinned} onClick={() => { setPreferences((old) => ({ ...old, pinned: !old.pinned })); setHover(false); setFocus(false); document.getElementById('console-nav-toggle')?.focus(); }}>{preferences.pinned ? <ChevronsLeft size={17} /> : <PanelLeftOpen size={17} />}</Button>{expanded && <span className="console-nav-label">{t(preferences.pinned ? 'unpin' : 'pin')}</span>}</div>
        {expanded && <div className="console-resize" role="separator" aria-label={t('resize')} aria-orientation="vertical" aria-valuemin={MIN} aria-valuemax={MAX} aria-valuenow={preferences.width} tabIndex={0}
          onDoubleClick={() => resize(DEFAULT)} onKeyDown={(event) => { if (['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) { event.preventDefault(); resize(event.key === 'Home' ? MIN : event.key === 'End' ? MAX : preferences.width + (event.key === 'ArrowRight' ? 16 : -16)); } }}
          onPointerDown={(event) => { if (event.button !== 0) return; event.preventDefault(); drag.current = { x: event.clientX, width: preferences.width }; event.currentTarget.setPointerCapture(event.pointerId); }}
          onPointerMove={(event) => { if (drag.current) resize(drag.current.width + event.clientX - drag.current.x); }}
          onPointerUp={(event) => { drag.current = null; event.currentTarget.releasePointerCapture(event.pointerId); }} onLostPointerCapture={() => { drag.current = null; }} />}
      </aside>
    </div>
  </>;
}
