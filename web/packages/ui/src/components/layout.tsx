import { Slot } from 'radix-ui';
import type { ComponentProps, ReactNode } from 'react';
import { cn } from '../lib/utils.js';
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from './sheet.js';
import { StatusDot, type StatusTone } from './status.js';

/** Page title block: title, description, actions on the right. */
export function PageHeader({
  title,
  description,
  actions,
  meta,
  className,
  ...props
}: Omit<ComponentProps<'header'>, 'title'> & {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  meta?: ReactNode;
}) {
  return (
    <header
      data-slot="page-header"
      {...props}
      className={cn('flex shrink-0 flex-wrap items-start justify-between gap-x-6 gap-y-3 pb-4', className)}
    >
      <div className="min-w-0">
        <h1 className="m-0 text-xl leading-7 font-medium tracking-tight text-foreground">{title}</h1>
        {description && <p className="m-0 mt-1 max-w-3xl text-sm text-muted-foreground">{description}</p>}
        {meta && <div className="mt-2">{meta}</div>}
      </div>
      {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
    </header>
  );
}

/** Entity title block (GitLab/Stroppy style): icon, name, status, meta line, actions and a tab bar. `level` 2 inside a page that has its own title. */
export function EntityHeader({
  icon,
  title,
  status,
  meta,
  actions,
  tabs,
  level = 1,
  className,
  ...props
}: Omit<ComponentProps<'header'>, 'title'> & {
  icon?: ReactNode;
  title: ReactNode;
  status?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  tabs?: ReactNode;
  level?: 1 | 2;
}) {
  const Heading = level === 1 ? 'h1' : 'h2';
  return (
    <header data-slot="entity-header" {...props} className={cn('mb-4 shrink-0', className)}>
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        <div className="flex min-w-0 items-start gap-3">
          {icon && (
            <span className="grid size-10 shrink-0 place-items-center rounded-lg border border-border bg-card text-link [&_svg:not([class*='size-'])]:size-5">
              {icon}
            </span>
          )}
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <Heading className="m-0 truncate text-xl leading-7 font-medium tracking-tight text-foreground">
                {title}
              </Heading>
              {status}
            </div>
            {meta && <div className="mt-1">{meta}</div>}
          </div>
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </div>
      {tabs && <div className="mt-4">{tabs}</div>}
    </header>
  );
}

/** Horizontal navigation tabs; children are TabItem links (router-agnostic, active via aria-current). */
export function TabBar({ className, ...props }: ComponentProps<'nav'>) {
  return (
    <nav
      data-slot="tab-bar"
      {...props}
      className={cn('flex gap-1 overflow-x-auto border-b border-border [scrollbar-width:none]', className)}
    />
  );
}
export function TabItem({ asChild, className, ...props }: ComponentProps<'a'> & { asChild?: boolean }) {
  const Component = asChild ? Slot.Root : 'a';
  return (
    <Component
      data-slot="tab-item"
      {...props}
      className={cn(
        'relative -mb-px inline-flex h-9 shrink-0 items-center gap-1.5 border-b-2 border-transparent px-2.5 text-sm whitespace-nowrap text-muted-foreground no-underline transition-colors hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring',
        'aria-[current=page]:border-primary aria-[current=page]:font-medium aria-[current=page]:text-foreground [&_svg:not([class*=size-])]:size-3.5',
        className,
      )}
    />
  );
}

/**
 * A titled section. `flush` removes body padding for tables and lists. The
 * body scrolls itself: `fill` stretches the panel over the remaining height
 * of a flex/grid parent, `maxBodyHeight` caps a panel inside a scrolling page.
 * Header and footer stay in place.
 */
export function Panel({
  title,
  description,
  count,
  actions,
  footer,
  flush,
  fill,
  maxBodyHeight,
  className,
  children,
  ...props
}: Omit<ComponentProps<'section'>, 'title'> & {
  title?: ReactNode;
  description?: ReactNode;
  count?: ReactNode;
  actions?: ReactNode;
  footer?: ReactNode;
  flush?: boolean;
  fill?: boolean;
  maxBodyHeight?: number;
}) {
  return (
    <section
      data-slot="panel"
      {...props}
      className={cn(
        'flex min-w-0 flex-col overflow-hidden rounded-lg border border-border bg-card text-card-foreground',
        fill ? 'min-h-0 flex-1' : 'shrink-0',
        className,
      )}
    >
      {(title || actions) && (
        <header className="flex min-h-10 shrink-0 items-center gap-2 border-b border-border px-3 py-1.5">
          <div className="min-w-0">
            {title && (
              <h2 className="m-0 flex items-center gap-2 text-sm font-medium text-foreground">
                {title}
                {count !== undefined && <Count>{count}</Count>}
              </h2>
            )}
            {description && <p className="m-0 text-xs text-muted-foreground">{description}</p>}
          </div>
          {actions && <div className="ml-auto flex shrink-0 items-center gap-1">{actions}</div>}
        </header>
      )}
      {/* A body with its own scroll is a keyboard scroll region. */}
      <div
        data-slot="panel-body"
        tabIndex={fill || maxBodyHeight ? 0 : undefined}
        className={cn(
          'min-h-0 min-w-0 flex-1 overflow-auto focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring',
          !flush && 'p-3',
        )}
        style={maxBodyHeight ? { maxHeight: maxBodyHeight } : undefined}
      >
        {children}
      </div>
      {footer && (
        <footer className="flex shrink-0 items-center gap-2 border-t border-border px-3 py-2 text-xs text-muted-foreground">
          {footer}
        </footer>
      )}
    </section>
  );
}

export function Count({ className, ...props }: ComponentProps<'span'>) {
  return (
    <span
      data-slot="count"
      {...props}
      className={cn(
        'inline-flex h-4.5 min-w-4.5 items-center justify-center rounded-sm bg-raised px-1 font-mono text-2xs font-normal text-muted-foreground tabular-nums',
        className,
      )}
    />
  );
}

/** Uppercase group caption (navigation groups, card sections). */
export function SectionLabel({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      data-slot="section-label"
      {...props}
      className={cn('text-2xs font-semibold tracking-wider text-muted-foreground uppercase', className)}
    />
  );
}

const valueTone: Record<StatusTone, string> = {
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-destructive',
  info: 'text-info',
  neutral: 'text-foreground',
  accent: 'text-link',
};
export function StatRow({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      data-slot="stat-row"
      {...props}
      className={cn('grid grid-cols-[repeat(auto-fit,minmax(150px,1fr))] gap-3', className)}
    />
  );
}
/** One metric: label, value, optional hint line. */
export function StatTile({
  label,
  value,
  hint,
  icon,
  tone = 'neutral',
  className,
  ...props
}: Omit<ComponentProps<'div'>, 'children'> & {
  label: ReactNode;
  value: ReactNode;
  hint?: ReactNode;
  icon?: ReactNode;
  tone?: StatusTone;
}) {
  return (
    <div
      data-slot="stat-tile"
      {...props}
      className={cn('min-w-0 rounded-lg border border-border bg-card px-3 py-2.5', className)}
    >
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground [&_svg:not([class*=size-])]:size-3.5">
        {icon}
        <span className="truncate">{label}</span>
      </div>
      <div className={cn('mt-1 text-2xl leading-8 font-normal tabular-nums', valueTone[tone])}>{value}</div>
      {hint && <div className="truncate text-xs text-muted-foreground">{hint}</div>}
    </div>
  );
}

/** Inline meta line: version, instances, uptime… */
export function MetaList({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      data-slot="meta-list"
      {...props}
      className={cn('flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground', className)}
    />
  );
}
export function MetaItem({
  icon,
  label,
  mono,
  className,
  children,
  ...props
}: ComponentProps<'span'> & { icon?: ReactNode; label?: string; mono?: boolean }) {
  return (
    <span
      data-slot="meta-item"
      title={label}
      {...props}
      className={cn(
        'inline-flex min-w-0 items-center gap-1.5 [&_svg:not([class*=size-])]:size-3.5 [&_svg]:shrink-0',
        mono && 'font-mono',
        className,
      )}
    >
      {icon}
      {label && <span className="sr-only">{label}: </span>}
      <span className="truncate">{children}</span>
    </span>
  );
}

export interface KeyValueItem {
  key?: string;
  label: ReactNode;
  value: ReactNode;
  mono?: boolean;
}
/** Metadata list (entity sidebar, detail drawers). */
export function KeyValueList({ items, className, ...props }: ComponentProps<'dl'> & { items: KeyValueItem[] }) {
  return (
    <dl
      data-slot="key-value-list"
      {...props}
      className={cn('m-0 grid grid-cols-[minmax(96px,max-content)_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm', className)}
    >
      {items.map((item, index) => (
        <div key={item.key ?? index} className="contents">
          <dt className="text-muted-foreground">{item.label}</dt>
          <dd
            className={cn(
              'm-0 min-w-0 overflow-hidden break-words text-foreground [overflow-wrap:anywhere]',
              item.mono && 'font-mono text-xs leading-5',
            )}
          >
            {item.value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

/** Empty or not-yet-available content inside a panel or page. */
export function EmptyState({
  icon,
  title,
  description,
  action,
  className,
  ...props
}: Omit<ComponentProps<'div'>, 'title'> & {
  icon?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div
      data-slot="empty-state"
      {...props}
      className={cn('flex flex-col items-center justify-center gap-2 px-6 py-10 text-center', className)}
    >
      {icon && (
        <span className="mb-1 grid size-10 place-items-center rounded-full bg-raised text-muted-foreground [&_svg:not([class*=size-])]:size-5">
          {icon}
        </span>
      )}
      <div className="text-sm font-medium text-foreground">{title}</div>
      {description && <p className="m-0 max-w-sm text-xs text-muted-foreground">{description}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}

/** Segmented switch between views (cards ⇄ table). */
export function ViewToggle<T extends string>({
  label,
  value,
  options,
  onValueChange,
  className,
}: {
  label: string;
  value: T;
  options: { value: T; label: string; icon?: ReactNode }[];
  onValueChange: (value: T) => void;
  className?: string;
}) {
  return (
    <div
      role="group"
      aria-label={label}
      data-slot="view-toggle"
      className={cn('inline-flex h-8 items-center gap-0.5 rounded-md border border-border bg-muted p-0.5', className)}
    >
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          aria-pressed={value === option.value}
          title={option.label}
          onClick={() => onValueChange(option.value)}
          className="inline-flex h-6 items-center gap-1.5 rounded-sm px-2 text-xs text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring aria-pressed:bg-raised aria-pressed:text-foreground [&_svg]:size-3.5"
        >
          {option.icon}
          <span className={cn(option.icon && 'sr-only sm:not-sr-only')}>{option.label}</span>
        </button>
      ))}
    </div>
  );
}

/** Thin proportion bar (healthy / total). */
export function Meter({
  value,
  max,
  tone = 'success',
  label,
  className,
}: {
  value: number;
  max: number;
  tone?: StatusTone;
  label: string;
  className?: string;
}) {
  const ratio = max > 0 ? Math.min(1, Math.max(0, value / max)) : 0;
  const fill: Record<StatusTone, string> = {
    success: 'bg-success',
    warning: 'bg-warning',
    danger: 'bg-destructive',
    info: 'bg-info',
    neutral: 'bg-subtle',
    accent: 'bg-primary',
  };
  return (
    <div
      role="meter"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={max}
      aria-valuenow={value}
      className={cn('h-1 w-full overflow-hidden rounded-full bg-raised', className)}
    >
      <div className={cn('h-full rounded-full transition-[width]', fill[tone])} style={{ width: `${ratio * 100}%` }} />
    </div>
  );
}

/** Health vocabulary line: dot + label. */
export function StatusText({ tone, className, children, ...props }: ComponentProps<'span'> & { tone: StatusTone }) {
  return (
    <span {...props} className={cn('inline-flex items-center gap-1.5 text-sm', className)}>
      <StatusDot tone={tone} />
      {children}
    </span>
  );
}

/** Right-side detail panel for an entry, run, instance or log line. */
export function DetailDrawer({
  open,
  onOpenChange,
  title,
  description,
  actions,
  size = 'md',
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  size?: 'md' | 'lg' | 'xl' | 'full';
  children: ReactNode;
}) {
  const width = {
    md: 'data-[side=right]:sm:max-w-[520px]',
    lg: 'data-[side=right]:sm:max-w-[760px]',
    xl: 'data-[side=right]:sm:max-w-[1040px]',
    full: 'data-[side=right]:sm:max-w-[min(1600px,94vw)]',
  }[size];
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className={cn('gap-0 border-border bg-chrome p-0 data-[side=right]:w-full', width)}>
        <SheetHeader className="gap-1 border-b border-border px-4 py-3 pr-12">
          <SheetTitle className="truncate text-base font-medium">{title}</SheetTitle>
          {description ? (
            <SheetDescription className="text-xs">{description}</SheetDescription>
          ) : (
            <SheetDescription className="sr-only">{title}</SheetDescription>
          )}
          {actions && <div className="mt-2 flex flex-wrap gap-2">{actions}</div>}
        </SheetHeader>
        <div className="min-h-0 flex-1 overflow-y-auto p-4">{children}</div>
      </SheetContent>
    </Sheet>
  );
}
