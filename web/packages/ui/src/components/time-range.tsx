import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from './button.js';
import { SelectControl } from './select-control.js';

export interface TimeRangeValue {
  /** Grafana date math (now-15m) or an ISO instant. */
  from: string;
  to: string;
  timeZone: string;
  weekStart?: 'monday' | 'saturday' | 'sunday';
}
export interface TimeRangeControlProps {
  value: TimeRangeValue;
  onChange: (value: TimeRangeValue) => void;
  mode: 'dark' | 'light';
  label: string;
  /** Also pick the week's first day (calendar views); off by default. */
  weekStartPicker?: boolean;
}
const Picker = lazy(() => import('./time-range.grafana.js').then((module) => ({ default: module.GrafanaTimeRange })));
export function TimeRangeControl(props: TimeRangeControlProps) {
  const { t } = useTranslation('backplane.ui');
  return (
    <Suspense fallback={<span role="status">{t('loading')}</span>}>
      <Picker {...props} />
    </Suspense>
  );
}

export function RefreshControl({
  interval,
  onIntervalChange,
  onRefresh,
  disabled,
}: {
  interval: number;
  onIntervalChange: (milliseconds: number) => void;
  onRefresh: (signal: AbortSignal) => void | Promise<void>;
  disabled?: boolean;
}) {
  const { t } = useTranslation('backplane.ui');
  const [pending, setPending] = useState(false),
    [failed, setFailed] = useState(false);
  const active = useRef<AbortController | null>(null);
  const refreshRef = useRef(onRefresh);
  refreshRef.current = onRefresh;
  const refresh = async () => {
    if (disabled || active.current) return;
    const controller = new AbortController();
    active.current = controller;
    setPending(true);
    setFailed(false);
    try {
      await refreshRef.current(controller.signal);
    } catch {
      if (!controller.signal.aborted) setFailed(true);
    } finally {
      if (active.current === controller) {
        active.current = null;
        setPending(false);
      }
    }
  };
  const callback = useRef(refresh);
  callback.current = refresh;
  useEffect(() => {
    if (disabled || interval < 1000) return;
    const timer = setInterval(() => void callback.current(), interval);
    return () => clearInterval(timer);
  }, [interval, disabled]);
  useEffect(
    () => () => {
      active.current?.abort();
      active.current = null;
    },
    [],
  );
  return (
    <div className="flex items-center gap-2">
      <Button type="button" variant="outline" disabled={disabled || pending} onClick={() => void refresh()}>
        {t(pending ? 'loading' : 'refresh')}
      </Button>
      <SelectControl
        aria-label={t('refreshInterval')}
        disabled={disabled}
        value={String(interval)}
        onValueChange={(value) => onIntervalChange(Number(value))}
        options={[
          { value: '0', label: t('off') },
          ...[5000, 10000, 30000, 60000].map((value) => ({ value: String(value), label: `${value / 1000} s` })),
        ]}
      />
      {failed && <span role="alert">{t('refreshFailed')}</span>}
    </div>
  );
}

const units: Record<string, number> = { s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000, w: 604_800_000 };

/**
 * A Grafana date-math instant (now, now-15m, now-1d/d, now/w) or an ISO
 * instant as epoch milliseconds; roundUp ends a /unit at the unit's end (for
 * a range's to). Rounds in UTC for the utc time zone, else locally.
 */
export function dateMath(text: string, now: number, roundUp = false, utc = false): number | undefined {
  const value = text.trim();
  if (!value.startsWith('now')) {
    const at = Date.parse(value);
    return Number.isNaN(at) ? undefined : at;
  }
  let at = new Date(now),
    rest = value.slice(3);
  while (rest) {
    const step = /^([+-])(\d+)([smhdwMy])/.exec(rest);
    if (step) {
      const n = Number(step[2]) * (step[1] === '-' ? -1 : 1),
        unit = step[3];
      if (unit === 'M' || unit === 'y') {
        const d = new Date(at);
        if (utc) d.setUTCMonth(d.getUTCMonth() + n * (unit === 'y' ? 12 : 1));
        else d.setMonth(d.getMonth() + n * (unit === 'y' ? 12 : 1));
        at = d;
      } else at = new Date(at.getTime() + n * units[unit]);
      rest = rest.slice(step[0].length);
      continue;
    }
    const round = /^\/([smhdwMy])/.exec(rest);
    if (!round) return undefined;
    at = roundTo(at, round[1], roundUp, utc);
    rest = rest.slice(round[0].length);
  }
  return at.getTime();
}

type Field = 'FullYear' | 'Month' | 'Date' | 'Hours' | 'Minutes' | 'Seconds' | 'Milliseconds';
const roundOrder = ['y', 'M', 'w', 'd', 'h', 'm', 's'];
const unitField: Record<string, Field> = {
  y: 'FullYear',
  M: 'Month',
  w: 'Date',
  d: 'Date',
  h: 'Hours',
  m: 'Minutes',
  s: 'Seconds',
};

/** at rounded down to the start of unit, or up to its last millisecond. */
function roundTo(at: Date, unit: string, up: boolean, utc: boolean): Date {
  const d = new Date(at);
  const get = (field: Field | 'Day') => (utc ? d[`getUTC${field}`] : d[`get${field}`]).call(d);
  const set = (field: Field, v: number) => {
    (utc ? d[`setUTC${field}`] : d[`set${field}`]).call(d, v);
  };
  const level = roundOrder.indexOf(unit);
  if (level <= 0) set('Month', 0);
  if (level <= 1) set('Date', 1);
  if (unit === 'w') set('Date', get('Date') - ((get('Day') + 6) % 7)); // weeks start on Monday
  if (level <= 3) set('Hours', 0);
  if (level <= 4) set('Minutes', 0);
  if (level <= 5) set('Seconds', 0);
  set('Milliseconds', 0);
  if (!up) return d;
  // unit comes from the same character class as unitField's keys.
  const field = unitField[unit];
  set(field, get(field) + (unit === 'w' ? 7 : 1));
  return new Date(d.getTime() - 1);
}

/** A time range as epoch milliseconds; undefined when it does not parse or is empty. */
export function resolveTimeRange(value: TimeRangeValue, now = Date.now()): { from: number; to: number } | undefined {
  const utc = value.timeZone === 'utc',
    from = dateMath(value.from, now, false, utc),
    to = dateMath(value.to, now, true, utc);
  return from !== undefined && to !== undefined && from < to ? { from, to } : undefined;
}
