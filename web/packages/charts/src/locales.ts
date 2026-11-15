import { useTranslation } from 'react-i18next';
export const chartsEnglish = {
  loading: 'Loading visualization…',
  empty: 'No data',
  partial: 'Partial results',
  sampled: 'Showing a bounded sample of the data.',
  series: 'Series',
  value: 'Value',
  samples: 'Samples',
  last: 'Last value',
  showData: 'Show data',
  hideData: 'Hide data',
  unit: 'Unit',
  stats: 'Statistics',
  color: 'Series color',
  thresholds: 'Thresholds',
  addThreshold: 'Add threshold',
  removeThreshold: 'Remove threshold',
  thresholdValue: 'Threshold value',
  thresholdColor: 'Threshold color',
  rangeStart: 'Range start',
  rangeEnd: 'Range end',
  applyRange: 'Apply range',
  invalidRange: 'Enter a finite, increasing range.',
  approximate: 'Plot coordinates are approximate; data values retain their original precision.',
  invalidData: 'Some non-finite samples cannot be plotted.',
} as const;
export function useChartText() {
  const { t } = useTranslation('backplane.charts');
  return (key: keyof typeof chartsEnglish) => t(key, { defaultValue: chartsEnglish[key] });
}
