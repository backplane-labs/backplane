import type { NavEntry } from '@backplane-labs/backplane-plugin-sdk';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@backplane-labs/backplane-ui';
import { ChevronRight } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { matchPath, NavLink, useLocation } from 'react-router-dom';

function pagePath(service: string, path: string) {
  return path ? `/s/${service}/${path}` : `/s/${service}/`;
}

function Group({ service, label, entries }: { service: string; label: string; entries: readonly NavEntry[] }) {
  const { t } = useTranslation(`module.${service}`);
  const { pathname } = useLocation();
  const activePath = entries.some((entry) =>
    matchPath({ path: pagePath(service, entry.path), end: !entry.path }, pathname),
  )
    ? pathname
    : undefined;
  const [open, setOpen] = useState(activePath !== undefined);

  // Reveal an active group on navigation but allow the user to collapse it on the current page.
  useEffect(() => {
    if (activePath !== undefined) setOpen(true);
  }, [activePath]);

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger className="console-page-link console-module-group-trigger">
        <ChevronRight size={14} className="console-chevron" data-open={open} aria-hidden="true" />
        {t(label)}
      </CollapsibleTrigger>
      <CollapsibleContent className="console-leaves console-module-group-items">
        {entries.map((entry) => (
          <PageLink key={entry.path} service={service} entry={entry} />
        ))}
      </CollapsibleContent>
    </Collapsible>
  );
}

function PageLink({ service, entry }: { service: string; entry: NavEntry }) {
  const { t } = useTranslation(`module.${service}`);
  return (
    <NavLink to={pagePath(service, entry.path)} end={!entry.path} className="console-page-link">
      {t(entry.labelKey)}
    </NavLink>
  );
}

export function ModuleNavigation({ service, entries }: { service: string; entries: readonly NavEntry[] }) {
  const main: NavEntry[] = [];
  const groups = new Map<string, NavEntry[]>();
  for (const entry of entries) {
    if (!entry.groupLabelKey) {
      main.push(entry);
      continue;
    }
    const group = groups.get(entry.groupLabelKey);
    if (group) group.push(entry);
    else groups.set(entry.groupLabelKey, [entry]);
  }

  return (
    <>
      {main.map((entry) => (
        <PageLink key={entry.path} service={service} entry={entry} />
      ))}
      {Array.from(groups, ([label, pages]) => (
        <Group key={label} service={service} label={label} entries={pages} />
      ))}
    </>
  );
}
