import type { ComponentProps, ReactNode } from 'react';
import { cn } from '../lib/utils.js';

/** One status vocabulary for health, phases, run states and applied/rejected. */
export type StatusTone = 'success' | 'warning' | 'danger' | 'info' | 'neutral' | 'accent';

const dotTone: Record<StatusTone, string> = {
  success: 'bg-success',
  warning: 'bg-warning',
  danger: 'bg-destructive',
  info: 'bg-info',
  neutral: 'bg-subtle',
  accent: 'bg-primary',
};
const badgeTone: Record<StatusTone, string> = {
  success: 'border-success/30 bg-success/10 text-success',
  warning: 'border-warning/30 bg-warning/10 text-warning',
  danger: 'border-destructive/30 bg-destructive/10 text-destructive',
  info: 'border-info/30 bg-info/10 text-info',
  neutral: 'border-border-strong bg-raised text-muted-foreground',
  accent: 'border-primary/30 bg-primary/10 text-link',
};

export function StatusDot({
  tone,
  pulse,
  className,
  ...props
}: ComponentProps<'span'> & { tone: StatusTone; pulse?: boolean }) {
  return (
    <span
      data-slot="status-dot"
      data-tone={tone}
      aria-hidden="true"
      {...props}
      className={cn('relative inline-flex size-2 shrink-0 rounded-full', dotTone[tone], className)}
    >
      {pulse && (
        <span
          className={cn('absolute inset-0 animate-ping rounded-full opacity-60 motion-reduce:hidden', dotTone[tone])}
        />
      )}
    </span>
  );
}

export function StatusBadge({
  tone,
  dot = true,
  children,
  className,
  ...props
}: ComponentProps<'span'> & { tone: StatusTone; dot?: boolean; children: ReactNode }) {
  return (
    <span
      data-slot="status-badge"
      data-tone={tone}
      {...props}
      className={cn(
        'inline-flex h-5 shrink-0 items-center gap-1.5 rounded-sm border px-1.5 text-xs font-medium whitespace-nowrap',
        badgeTone[tone],
        className,
      )}
    >
      {dot && <StatusDot tone={tone} className="size-1.5" />}
      {children}
    </span>
  );
}
