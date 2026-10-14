# Charts

`@backplane-labs/backplane-charts` provides `TimeSeriesChart`, `BarChart`, `Histogram`,
`Heatmap`, `Stat`, `BarGauge`, `RadialGauge`, `SeriesTable`,
`VisualizationControls` and `VisualizationPanel`. English resources are exported
as `chartsEnglish` for `backplane.charts`. Renderers load lazily. Data adapters
from platform RPC responses belong to `platform-ui`, not this package.

Time-series x coordinates are epoch milliseconds; histogram x coordinates are
numeric bucket positions. Sample values can be numbers, decimal strings, bigint
or null. Canvas coordinates use finite JavaScript numbers. Original values stay
in the input, hover display and data table; integer conversion loss is explicitly
reported. Sorting/rendering never mutates caller data. Nulls create gaps;
non-finite values are reported and omitted from plotting.

uPlot renders lines/bars. Inputs are bounded to 64 visible series and a configurable
512–20,000 point union (2,000 by default). Bucket sampling retains endpoints and
local extrema, with a representative null per bucket. The UI reports sampling and
partial results. It never implies that sampled data is the complete query result.
The exact-value table shows the same sampled points. Use caller-owned export for
the complete dataset. Series summaries operate on original data. Heatmaps display
at most 10,000 cells (2,000 by default) and expose a virtualized keyboard-accessible
table with selection callbacks.

Legends are keyboard-operable toggle buttons. Range selection supports dragging
and explicit numeric bounds. `onRangeChange` does not issue queries itself.
Both themes, empty/partial states, exact values and interaction are exercised in
Storybook.

Stat/bar gauge, unit/stat/color controls use public pinned Grafana 13.2.3 APIs
through the common theme bridge. The published package contains declarations
for RadialGauge but does not export it. Our radial gauge is an SVG composition
with the same public value/threshold contract; it does not import private Grafana
paths. Values are accompanied by exact accessible text. Gauge min/max/threshold
positions are finite display numbers, not a replacement for exact source values.

Series use the theme's `chart-1…5` colors (canvas plots need concrete colors).
Time axis labels adapt their precision to the visible span. `controls={false}`
hides the series toggles and the data table when the caller renders its own
legend, as Explore does.
