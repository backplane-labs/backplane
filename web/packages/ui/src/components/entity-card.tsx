import type { ComponentProps, ReactNode } from 'react';
import { cn } from '../lib/utils.js';

/**
 * Card for a list entity. `title` is usually the caller's router link; the
 * whole card is not a link so inner actions stay reachable.
 */
export function EntityCard({ icon, title, subtitle, status, actions, footer, className, children, ...props }: Omit<ComponentProps<'article'>, 'title'> & {
  icon?: ReactNode; title: ReactNode; subtitle?: ReactNode; status?: ReactNode; actions?: ReactNode; footer?: ReactNode;
}) {
  return <article data-slot="entity-card" {...props} className={cn('group/card flex min-w-0 flex-col rounded-lg border border-border bg-card text-card-foreground transition-colors hover:border-border-strong', className)}>
    <header className="flex items-start gap-2.5 px-3 pt-3">
      {icon && <span className="grid size-8 shrink-0 place-items-center rounded-md border border-border bg-raised text-link [&_svg:not([class*=size-])]:size-4">{icon}</span>}
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2"><div className="min-w-0 truncate text-sm font-medium text-foreground" title={typeof title === 'string' ? title : undefined}>{title}</div>{status && <div className="ml-auto shrink-0">{status}</div>}</div>
        {subtitle && <div className="mt-0.5 truncate text-xs text-muted-foreground" title={typeof subtitle === 'string' ? subtitle : undefined}>{subtitle}</div>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-1">{actions}</div>}
    </header>
    <div className="flex min-w-0 flex-1 flex-col gap-3 p-3">{children}</div>
    {footer && <footer className="flex min-h-9 flex-wrap items-center gap-x-3 gap-y-1 border-t border-border px-3 py-1.5 text-xs text-muted-foreground">{footer}</footer>}
  </article>;
}

/** Small labelled figure inside cards: "Routes 3". */
export function Figure({ label, value, className, ...props }: Omit<ComponentProps<'div'>, 'children'> & { label: ReactNode; value: ReactNode }) {
  return <div data-slot="figure" {...props} className={cn('min-w-0', className)}>
    <div className="truncate text-2xs tracking-wide text-muted-foreground uppercase">{label}</div>
    <div className="font-mono text-sm text-foreground tabular-nums">{value}</div>
  </div>;
}
