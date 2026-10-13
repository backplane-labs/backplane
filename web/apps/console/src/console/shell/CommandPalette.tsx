import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { AppWindow, Box, Compass, History, Moon, Search, Sun, Workflow, type LucideIcon, Bug } from 'lucide-react';
import { Command, CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList, CommandShortcut, Kbd } from '@gopherex/backplane-ui';
import type { ServiceSummary } from '@gopherex/backplane-api';
import type { ThemeMode } from '@gopherex/backplane-theme';
import type { ModuleEntry } from '../registry';
import { serviceTabs } from '../service/tabs';

/** ⌘K / Ctrl+K: jump to any service, service tab or module page. */
export function CommandPalette({ services, modules, mode, onThemeChange }: {
  services: ServiceSummary[]; modules: Record<string, ModuleEntry>; mode: ThemeMode; onThemeChange: (mode: ThemeMode) => void;
}) {
  const { t, i18n } = useTranslation('console'), navigate = useNavigate();
  const [open, setOpen] = useState(false);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key.toLowerCase() === 'k' && (event.metaKey || event.ctrlKey)) { event.preventDefault(); setOpen((value) => !value); }
    };
    addEventListener('keydown', onKey);
    return () => removeEventListener('keydown', onKey);
  }, []);
  const go = (path: string) => { setOpen(false); navigate(path); };
  const item = (key: string, path: string, Icon: LucideIcon, label: string, hint?: string) =>
    <CommandItem key={key} value={`${label} ${hint ?? ''} ${key}`} onSelect={() => go(path)}><Icon />{label}{hint && <span className="text-muted-foreground">{hint}</span>}</CommandItem>;
  return <>
    <button type="button" className="console-search" onClick={() => setOpen(true)} aria-keyshortcuts="Control+K Meta+K">
      <Search size={14} aria-hidden="true" /><span>{t('searchPlaceholder')}</span><Kbd className="ml-auto">⌘K</Kbd>
    </button>
    <CommandDialog open={open} onOpenChange={setOpen} title={t('searchTitle')}>
      <Command>
        <CommandInput placeholder={t('searchPlaceholder')} />
        <CommandList>
          <CommandEmpty>{t('searchEmpty')}</CommandEmpty>
          <CommandGroup heading={t('platform')}>
            {item('nav-services', '/services', Box, t('services'))}
            {item('nav-wiring', '/wiring', Workflow, t('wiring'))}
            {item('nav-explore', '/explore', Compass, t('explore'))}
            {item('nav-errors', '/errors', Bug, t('errors'))}
            {item('nav-audit', '/audit', History, t('audit'))}
          </CommandGroup>
          <CommandGroup heading={t('services')}>
            {services.flatMap((service) => serviceTabs.map((tab) => item(`svc-${service.name}-${tab}`, `/services/${service.name}${tab === 'overview' ? '' : `/${tab}`}`, Box, service.name, tab === 'overview' ? undefined : t(tab))))}
          </CommandGroup>
          {services.some((service) => modules[service.name]?.plugin) && <CommandGroup heading={t('servicePages')}>
            {services.flatMap((service) => modules[service.name]?.plugin?.navigation.nav.map((page) =>
              item(`page-${service.name}-${page.path}`, `/s/${service.name}/${page.path}`, AppWindow, i18n.t(page.labelKey, { ns: `module.${service.name}` }), service.name)) ?? [])}
          </CommandGroup>}
          <CommandGroup heading={t('preferences')}>
            <CommandItem value="theme toggle" onSelect={() => { onThemeChange(mode === 'dark' ? 'light' : 'dark'); setOpen(false); }}>
              {mode === 'dark' ? <Sun /> : <Moon />}{t(mode === 'dark' ? 'light' : 'dark')}<CommandShortcut />
            </CommandItem>
          </CommandGroup>
        </CommandList>
      </Command>
    </CommandDialog>
  </>;
}
