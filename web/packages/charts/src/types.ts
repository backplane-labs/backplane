export type NumericValue = string | number | bigint | null;
/** Epoch milliseconds for time series; numeric bucket positions for histograms. */
export interface ChartPoint { x: number; value: NumericValue }
export interface ChartSeries { id: string; label: string; points: readonly ChartPoint[]; color?: string }
export interface ChartProps {
  label: string; series: readonly ChartSeries[]; mode: 'dark' | 'light'; kind?: 'line' | 'bars' | 'histogram';
  height?: number; maxPoints?: number; timeZone?: string; loading?: boolean; error?: string; partial?: boolean;
  /** Unit of the values (OpenTelemetry's: s, ms, By, or any text): the y axis and the cursor read in it, durations humanized. */
  unit?: string;
  onRangeChange?: (range: { from: number; to: number }) => void;
  /** Series toggles, the data table and the typed range form under the plot (dragging still selects a range); off when the caller renders its own legend and range picker. */
  controls?: boolean;
}
export interface Threshold { value: number; color: string }
export interface ValueProps {
  label: string; value: NumericValue; mode: 'dark' | 'light'; unit?: string;
  min?: number; max?: number; thresholds?: readonly Threshold[]; color?: string; height?: number;
}
export interface HeatCell { id: string; x: string; y: string; value: NumericValue }
export interface HeatmapProps { label: string; cells: readonly HeatCell[]; mode: 'dark' | 'light'; height?: number; maxCells?: number; partial?: boolean; onSelect?: (cell: HeatCell) => void }
export interface VisualizationSettings { unit: string; stats: string[]; color: string; thresholds: Threshold[] }
export interface VisualizationControlsProps { value: VisualizationSettings; onChange: (value: VisualizationSettings) => void; mode: 'dark' | 'light' }
