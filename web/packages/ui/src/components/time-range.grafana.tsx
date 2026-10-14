import { useId, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { dateTime, rangeUtil, type TimeRange } from '@grafana/data';
import { ThemeContext, TimeRangePicker, WeekStartPicker } from '@grafana/ui';
import { createGrafanaTheme } from '@backplane-labs/backplane-theme';
import type { TimeRangeControlProps } from './time-range.js';

export default function GrafanaTimeRange({ value, onChange, mode, label, weekStartPicker }: TimeRangeControlProps) {
  const { t } = useTranslation('backplane.ui');
  const weekId = useId();
  const theme = useMemo(() => createGrafanaTheme(mode), [mode]);
  let range: TimeRange;
  try {
    range = rangeUtil.convertRawToRange({ from: value.from, to: value.to }, value.timeZone);
    if (!range.from.isValid() || !range.to.isValid() || range.from.valueOf() >= range.to.valueOf()) throw new Error('Invalid range');
  } catch { return <p role="alert">{t('invalidTimeRange')}</p>; }
  const change = (range: TimeRange) => onChange({ ...value,
    from: typeof range.raw.from === 'string' ? range.raw.from : range.raw.from.toISOString(),
    to: typeof range.raw.to === 'string' ? range.raw.to : range.raw.to.toISOString(),
  });
  const move = (factor: number) => {
    const width = range.to.valueOf() - range.from.valueOf();
    const from = dateTime(range.from.valueOf() + width * factor), to = dateTime(range.to.valueOf() + width * factor);
    change({ from, to, raw: { from, to } });
  };
  return <ThemeContext.Provider value={theme}><section aria-label={label} className="flex flex-wrap items-center gap-3">
    <TimeRangePicker value={range} timeZone={value.timeZone} weekStart={value.weekStart} onChange={change}
      onChangeTimeZone={(timeZone) => onChange({ ...value, timeZone })} onMoveBackward={() => move(-1)} onMoveForward={() => move(1)}
      onZoom={() => { const span = range.to.valueOf() - range.from.valueOf(); const from = dateTime(range.from.valueOf() - span / 2), to = dateTime(range.to.valueOf() + span / 2); change({ from, to, raw: { from, to } }); }} />
    {weekStartPicker && <><label htmlFor={weekId}>{t('weekStart')}</label><WeekStartPicker inputId={weekId} value={value.weekStart ?? 'monday'} onChange={(weekStart) => onChange({ ...value, weekStart })} /></>}
  </section></ThemeContext.Provider>;
}
