import { type ComponentProps, useSyncExternalStore } from 'react';
import { useTranslation } from 'react-i18next';
import { cn } from '../lib/utils.js';

// One shared ticker keeps every relative time on screen consistent.
const listeners = new Set<() => void>();
let now = Date.now(),
  timer: ReturnType<typeof setInterval> | undefined;
const TICK = 15_000;
function subscribe(listener: () => void) {
  listeners.add(listener);
  if (!timer) {
    // The ticker stops while nothing shows a time: resume from the real now, not the last tick.
    now = Date.now();
    timer = setInterval(() => {
      now = Date.now();
      listeners.forEach((notify) => notify());
    }, TICK);
  }
  return () => {
    listeners.delete(listener);
    if (!listeners.size && timer) {
      clearInterval(timer);
      timer = undefined;
    }
  };
}
function snapshot(): number {
  // Stable between ticks (a snapshot must not change on every read), never staler than one tick.
  if (Date.now() - now > TICK) now = Date.now();
  return now;
}
export function useNow(): number {
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}

/** Converts unix nanoseconds to a Date; sub-millisecond precision stays in the source value. */
export function nanosToDate(nanos: bigint): Date {
  return new Date(Number(nanos / 1_000_000n));
}

const steps: [Intl.RelativeTimeFormatUnit, number][] = [
  ['second', 60],
  ['minute', 60],
  ['hour', 24],
  ['day', 30],
  ['month', 12],
  ['year', Infinity],
];
export function formatRelative(date: Date, reference: number, language = 'en'): string {
  let value = (date.getTime() - reference) / 1000;
  // A moment just ahead of the reference is clock skew or a tick not yet taken: it is now.
  if (value > -10 && value < TICK / 1000 + 5)
    return new Intl.RelativeTimeFormat(language, { numeric: 'auto' }).format(0, 'second');
  for (const [unit, size] of steps) {
    if (Math.abs(value) < size)
      return new Intl.RelativeTimeFormat(language, { numeric: 'auto', style: 'short' }).format(Math.round(value), unit);
    value /= size;
  }
  return date.toISOString();
}
export function formatAbsolute(date: Date, language = 'en'): string {
  return new Intl.DateTimeFormat(language, {
    year: 'numeric',
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
    timeZoneName: 'short',
  }).format(date);
}
/** Compact duration: 45s, 12m 3s, 5h 20m, 3d 4h. */
export function formatDuration(milliseconds: number): string {
  const seconds = Math.max(0, Math.floor(milliseconds / 1000));
  const parts: [number, string][] = [
    [Math.floor(seconds / 86400), 'd'],
    [Math.floor(seconds / 3600) % 24, 'h'],
    [Math.floor(seconds / 60) % 60, 'm'],
    [seconds % 60, 's'],
  ];
  const first = parts.findIndex(([value]) => value > 0);
  if (first < 0) return milliseconds > 0 && milliseconds < 1000 ? `${Math.round(milliseconds)}ms` : '0s';
  return parts
    .slice(first, first + 2)
    .filter(([value]) => value > 0)
    .map(([value, unit]) => `${value}${unit}`)
    .join(' ');
}

/** Relative time with the absolute value in a tooltip; `absolute` renders the full timestamp. */
export function Timestamp({
  value,
  absolute,
  className,
  ...props
}: Omit<ComponentProps<'time'>, 'children'> & { value?: Date; absolute?: boolean }) {
  const { i18n } = useTranslation('backplane.ui'),
    reference = useNow();
  if (!value || Number.isNaN(value.getTime())) return <span className={cn('text-muted-foreground', className)}>—</span>;
  const full = formatAbsolute(value, i18n.language);
  return (
    <time
      dateTime={value.toISOString()}
      title={full}
      {...props}
      className={cn('whitespace-nowrap tabular-nums', className)}
    >
      {absolute ? full : formatRelative(value, reference, i18n.language)}
    </time>
  );
}

/** Elapsed time since `since` (live), or a fixed `milliseconds` span. */
export function Duration({
  since,
  milliseconds,
  className,
  ...props
}: ComponentProps<'span'> & { since?: Date; milliseconds?: number }) {
  const reference = useNow();
  const span = milliseconds ?? (since ? reference - since.getTime() : undefined);
  return (
    <span {...props} className={cn('whitespace-nowrap tabular-nums', className)}>
      {span === undefined ? '—' : formatDuration(span)}
    </span>
  );
}
