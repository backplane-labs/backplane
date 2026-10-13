# UI component matrix

This is the target coverage contract, not a list of completed components.
See [package and runtime boundaries](frontend-contract.md).
Rows distinguish available implementations from pending compositions. A row is complete only with exported
implementation, English resources, dark/light stories and the relevant checks
below. Several upstream stories can map to one public platform component.

The kit exports 47 shadcn primitive modules plus [base compositions](ui-kit.md)
for actions, precision inputs, async selection, forms, files, time ranges,
virtualized tables and trees. [Schema forms](schema-forms.md) use the published
schemapb engine. Storybook includes primitives, composed controls, error/readonly
states and schema fixtures in both themes. Editors, charts, observability and API-aware compositions are exported from
their [separate packages](frontend-contract.md). The inventory below uses **available** for an
existing public primitive/composition; it does not imply that every upstream
storybook example has been reproduced verbatim.

## Sources and adaptation policy

- [shadcn components](https://ui.shadcn.com/docs/components) and
  [semantic tokens](https://ui.shadcn.com/docs/theming): primitive source owned by
  the kit, obtained through the pinned official CLI. No second public primitive
  vocabulary from Grafana.
- [Grafana catalog](https://developers.grafana.com/ui/latest/index.html): breadth
  reference, snapshot 2026-09-29. Stable `@grafana/ui` inspected: 13.2.3. The
  `latest` documentation may include newer source; adapters must verify that the
  pinned published package actually exports each selected component.
- [HyperDX theme](https://github.com/hyperdxio/hyperdx/tree/main/packages/app/src/theme/themes/hyperdx):
  color, density and typography reference. Selectively port useful log/trace
  presentation code with notices; do not import its application or Mantine.
- The HyperDX adaptation behind the console's Errors is a behavioral
  reference for structured JSON, precise values and timelines.

Grafana types stay behind adapters where practical. First try published public
exports; tightly coupled code needs a deliberate source port and provenance.
No deep imports into unstable package internals simply to make a demo compile.

## Public families

| Family | Required coverage | Basis | Package |
| --- | --- | --- | --- |
| Foundations | text/headings, links, icon, avatar, spacing, borders, focus, status/series palettes | tokens + shadcn | theme/ui |
| Actions | button, icon button, groups, split/dropdown action, loading, clipboard, confirm | shadcn + composition | ui |
| Text inputs | text, textarea, search, password/secret, clearable/autosize, input group | shadcn + composition | ui |
| Choice | checkbox, switch, radio, select, async/multi combobox, segmented, tags, cascader | shadcn + composition | ui |
| Numeric | number input, slider/range, units, precision, bounds | shadcn + Grafana adapters | ui |
| Forms | label, description, error, fieldset/array, inline field/row, submit state, dirty guard | shadcn + React Hook Form | ui |
| Schema forms | schemapb scalar/object/array/enum/union, defaults, secret masking, field paths, JSON fallback | own | schema-forms |
| Date/time | calendar, date/time, relative/absolute range, timezone, week start, refresh/live | shadcn date + Grafana range adapters | ui |
| Files | upload, dropzone, list/progress, rejection, cancellation | composition | ui |
| Layout | box, stack, grid, divider, card, collapsible/accordion, resizable panels, scrolling | shadcn + small layout primitives | ui |
| Navigation | tabs, breadcrumb, pagination, toolbar, command menu, sidebar entries, links | shadcn + router adapter | ui/plugin-sdk |
| Overlays | dialog, confirmation, drawer/sheet, popover, tooltip, hover/toggletip, context/dropdown menu | shadcn | ui |
| Feedback | alert, badge/tag, toast, progress, skeleton/spinner, empty/error/offline/partial | shadcn + composition | ui |
| Tables | sorting, filtering, pagination, resize/reorder/hide/pin columns, selection, expansion, keyboard, virtual rows | shadcn + TanStack | ui |
| Trees | expandable hierarchy, selection, keyboard, large resource trees | own composition | ui |
| Editors | code, inline code, JSON validation/viewer, diff, copy/download, readonly | CodeMirror + own/HyperDX adaptations | editors |
| Query editors | CEL, MetricsQL/PromQL, LogsQL, TraceQL; syntax, completions, diagnostic ranges, cancellation | CodeMirror language adapters | editors |
| Visualization controls | units, value/stat/series color, thresholds, legend, series table, time-range selection | Grafana adapters | charts |
| Charts | time series, bars, histogram, heatmap, stat, bar/radial gauge, tooltip, empty/partial states | Grafana/uPlot adapters + compositions | charts |
| Log presentation | virtual rows, columns, structured body, severity, attributes, search highlighting, context, trace links | HyperDX ideas + own components | observability-ui |
| Trace presentation | trace list, waterfall, span tree/detail, events/links, attributes, duration, errors | HyperDX adaptations + own | observability-ui |
| Error presentation | stack trace, source context, nested causes, structured payload; no grouping engine | own/HyperDX adaptations | observability-ui |
| Service/config | instances, health reason, node tree, sources, schema forms, revisions/diff | platform compositions | platform-ui |
| Automation | hook/activity/event selectors, binding/rule editor, validation, run/history/status, schedules, DLQ | platform compositions | platform-ui |
| Audit | actor/action/subject, outcomes, details, filter/list/stream, intent/result | platform compositions | platform-ui |
| Explore | discovered sources, resource filters, language selector, query/results, correlation | platform compositions | platform-ui |
| Runtime states | exception boundary, connection state, auth expiry, plugin unavailable/incompatible, missing route | own | react/platform-ui/plugin-sdk |

## Completion checks

Every exported interactive component has keyboard operation, visible focus,
accessible name, disabled/readonly/error/loading/empty states where applicable,
and dark/light examples. Check text contrast and charts without relying on color
alone. Menus, editor completions and dialogs must agree on portal placement and
z-index, support Escape and return focus. Do not replace upstream keyboard
behavior with ad hoc click handlers.

Data-heavy components demonstrate virtualized large inputs, stable row identity,
selection across updates, timezone handling and values above JavaScript's safe
integer range. Log and trace displays preserve source data and timestamp
precision; raw strings are not rendered as executable HTML. Graph samples use
bounded downsampling and expose incomplete results. Editors/charts load lazily.

Storybook documents public props and composition, with real interaction stories
and fixtures. The compatibility fixture checks a shadcn table and form beside
Grafana visualization/time controls and a CodeMirror query editor in both modes.
The module template exercises the same components standalone and embedded.

The following are excluded from this phase: Grafana datasource administration,
plugin signatures, Grafana user management, alert-rule/policy/contact-point
management, dashboard builder, deprecated duplicate APIs. Generic equivalents
(status, badge, avatar, safe text, panel layout) remain in the kit.

## Grafana reference inventory

Snapshot: 145 unique titles from the official Storybook `index.json`, 2026-09-29.
The mapping accounts for every title, including documentation and deprecated
components. Mappings name the shipped public equivalent; deprecated duplicates and
Grafana-specific documentation/performance harnesses are intentionally excluded.

| Upstream title | Platform disposition | Status |
| --- | --- | --- |
| `Alerting/Contact Points/ContactPointSelector` | Grafana alerting domain excluded; generic status/badges covered | excluded |
| `Alerting/Intro` | Grafana alerting domain excluded; generic status/badges covered | excluded |
| `Alerting/Notification Policies/RoutingTreeSelector` | Grafana alerting domain excluded; generic status/badges covered | excluded |
| `Alerting/Rules/AlertLabel` | Grafana alerting domain excluded; generic status/badges covered | excluded |
| `Alerting/Rules/AlertLabels` | Grafana alerting domain excluded; generic status/badges covered | excluded |
| `Alerting/Rules/StateIcon` | Grafana alerting domain excluded; generic status/badges covered | excluded |
| `Alerting/Rules/StateText` | Grafana alerting domain excluded; generic status/badges covered | excluded |
| `Date time pickers/DatePicker` | Shared shadcn primitive or platform composition | available |
| `Date time pickers/DatePickerWithInput` | Shared shadcn primitive or platform composition | available |
| `Date time pickers/DateTimePicker` | Shared shadcn primitive or platform composition | available |
| `Date time pickers/RelativeTimeRangePicker` | Grafana adapter in platform tokens | available |
| `Date time pickers/TimeOfDayPicker` | Grafana adapter in platform tokens | available |
| `Date time pickers/TimeRangeInput` | Grafana adapter in platform tokens | available |
| `Date time pickers/TimeRangePicker` | Grafana adapter in platform tokens | available |
| `Date time pickers/TimeZonePicker` | Grafana adapter in platform tokens | available |
| `Date time pickers/WeekStartPicker` | Grafana adapter in platform tokens | available |
| `Developers/Border radius` | Foundations stories and ui-kit.md design/accessibility/English guidance | available |
| `Developers/Emotion perf test` | Grafana-specific Emotion benchmark excluded; browser acceptance covers our adapters | excluded |
| `Developers/Select Perf` | Async selection cancellation/debounce; bounded options and virtual data fixtures | available |
| `Developers/Typography` | Foundations stories and ui-kit.md design/accessibility/English guidance | available |
| `Docs Overview/Accessibility` | Foundations stories and ui-kit.md design/accessibility/English guidance | available |
| `Docs Overview/Design Principles` | Foundations stories and ui-kit.md design/accessibility/English guidance | available |
| `Docs Overview/Intro` | Foundations stories and ui-kit.md design/accessibility/English guidance | available |
| `Docs Overview/Voice and Tone` | Foundations stories and ui-kit.md design/accessibility/English guidance | available |
| `Forms/Deprecated/Form` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Forms/Deprecated/FormField` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Forms/Deprecated/SecretFormField` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Forms/Field` | Shared shadcn primitive or platform composition | available |
| `Forms/FieldArray` | Shared shadcn primitive or platform composition | available |
| `Forms/FieldSet` | Shared shadcn primitive or platform composition | available |
| `Forms/FieldValidationMessage` | Shared shadcn primitive or platform composition | available |
| `Forms/InlineField` | Shared shadcn primitive or platform composition | available |
| `Forms/InlineFieldRow` | Shared shadcn primitive or platform composition | available |
| `Forms/InlineLabel` | Shared shadcn primitive or platform composition | available |
| `Forms/Label` | Shared shadcn primitive or platform composition | available |
| `Forms/Legend` | Shared shadcn primitive or platform composition | available |
| `Foundations/Text` | Shared shadcn primitive or platform composition | available |
| `Foundations/TextLink` | Shared shadcn primitive or platform composition | available |
| `Foundations/Theme` | Shared shadcn primitive or platform composition | available |
| `Iconography/Avatar` | Shared shadcn primitive or platform composition | available |
| `Iconography/Icon` | Shared shadcn primitive or platform composition | available |
| `Iconography/UserIcon` | Shared shadcn primitive or platform composition | available |
| `Iconography/UsersIndicator` | Platform composition using shared primitives | available |
| `Information/Alert` | Shared shadcn primitive or platform composition | available |
| `Information/Badge` | Shared shadcn primitive or platform composition | available |
| `Information/Deprecated/CallToActionCard` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Information/Deprecated/EmptySearchResult` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Information/Deprecated/InfoBox` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Information/EmptyState` | Shared shadcn primitive or platform composition | available |
| `Information/FeatureBadge` | Shared shadcn primitive or platform composition | available |
| `Information/InlineToast` | Shared shadcn primitive or platform composition | available |
| `Information/LoadingBar` | Shared shadcn primitive or platform composition | available |
| `Information/LoadingPlaceholder` | Shared shadcn primitive or platform composition | available |
| `Information/PageLoader` | Shared shadcn primitive or platform composition | available |
| `Information/PluginSignatureBadge` | Grafana signatures excluded; own plugin compatibility status | excluded |
| `Information/Spinner` | Shared shadcn primitive or platform composition | available |
| `Information/Tag` | Shared shadcn primitive or platform composition | available |
| `Information/TagList` | Platform composition using shared primitives | available |
| `Inputs/AutoSaveField` | Platform composition using shared primitives | available |
| `Inputs/AutoSizeInput` | Platform composition using shared primitives | available |
| `Inputs/Button` | Shared shadcn primitive or platform composition | available |
| `Inputs/ButtonCascader` | Platform composition using shared primitives | available |
| `Inputs/Cascader` | Platform composition using shared primitives | available |
| `Inputs/Checkbox` | Shared shadcn primitive or platform composition | available |
| `Inputs/ClipboardButton` | Platform composition using shared primitives | available |
| `Inputs/CodeEditor` | Platform CodeMirror editor family | available |
| `Inputs/CodeMirrorEditor` | Platform CodeMirror editor family | available |
| `Inputs/CodeMirrorInlineInput` | Platform CodeMirror editor family | available |
| `Inputs/Combobox` | Shared shadcn primitive or platform composition | available |
| `Inputs/ConfirmButton` | Platform composition using shared primitives | available |
| `Inputs/Deprecated/ButtonSelect` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Inputs/Deprecated/QueryField` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Inputs/Deprecated/Select` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Inputs/Deprecated/TableInputCSV` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Inputs/FileDropzone` | Platform composition using shared primitives | available |
| `Inputs/FileListItem` | Platform composition using shared primitives | available |
| `Inputs/FileUpload` | Platform composition using shared primitives | available |
| `Inputs/FilterPill` | Platform composition using shared primitives | available |
| `Inputs/IconButton` | Shared shadcn primitive or platform composition | available |
| `Inputs/Input` | Shared shadcn primitive or platform composition | available |
| `Inputs/MultiCombobox` | Shared shadcn primitive or platform composition | available |
| `Inputs/QueryInput` | Platform CodeMirror editor family | available |
| `Inputs/RadioButtonGroup` | Shared shadcn primitive or platform composition | available |
| `Inputs/RadioButtonList` | Shared shadcn primitive or platform composition | available |
| `Inputs/RangeSlider` | Shared shadcn primitive or platform composition | available |
| `Inputs/SecretInput` | Platform composition using shared primitives | available |
| `Inputs/SecretTextArea` | Platform composition using shared primitives | available |
| `Inputs/Segment` | Platform composition using shared primitives | available |
| `Inputs/SegmentAsync` | Platform composition using shared primitives | available |
| `Inputs/SegmentInput` | Platform composition using shared primitives | available |
| `Inputs/Slider` | Shared shadcn primitive or platform composition | available |
| `Inputs/Switch` | Shared shadcn primitive or platform composition | available |
| `Inputs/TagsInput` | Platform composition using shared primitives | available |
| `Inputs/TextArea` | Shared shadcn primitive or platform composition | available |
| `Layout/Box` | Shared shadcn primitive or platform composition | available |
| `Layout/Card` | Shared shadcn primitive or platform composition | available |
| `Layout/CollapsableSection` | Shared shadcn primitive or platform composition | available |
| `Layout/Collapse` | Shared shadcn primitive or platform composition | available |
| `Layout/Deprecated/Groups` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Layout/Deprecated/List` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Layout/Deprecated/PanelContainer` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Layout/Divider` | Shared shadcn primitive or platform composition | available |
| `Layout/Grid` | Shared shadcn primitive or platform composition | available |
| `Layout/InteractiveTable` | Platform composition using shared primitives | available |
| `Layout/ScrollContainer` | Shared shadcn primitive or platform composition | available |
| `Layout/Space` | Shared shadcn primitive or platform composition | available |
| `Layout/Stack` | Shared shadcn primitive or platform composition | available |
| `Navigation/Deprecated/PageToolbar` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Navigation/Pagination` | Shared shadcn primitive or platform composition | available |
| `Navigation/Tabs` | Shared shadcn primitive or platform composition | available |
| `Navigation/ToolbarButton` | Shared shadcn primitive or platform composition | available |
| `Navigation/ToolbarButtonRow` | Shared shadcn primitive or platform composition | available |
| `Overlays/Carousel` | Shared shadcn primitive or platform composition | available |
| `Overlays/ConfirmModal` | Shared shadcn primitive or platform composition | available |
| `Overlays/ContextMenu` | Shared shadcn primitive or platform composition | available |
| `Overlays/Deprecated/InfoTooltip` | Deprecated duplicate excluded; use current kit APIs | excluded |
| `Overlays/Drawer` | Shared shadcn primitive or platform composition | available |
| `Overlays/Dropdown` | Shared shadcn primitive or platform composition | available |
| `Overlays/Menu` | Shared shadcn primitive or platform composition | available |
| `Overlays/Modal` | Shared shadcn primitive or platform composition | available |
| `Overlays/Sidebar` | Shared shadcn primitive or platform composition | available |
| `Overlays/Toggletip` | Shared shadcn primitive or platform composition | available |
| `Overlays/Tooltip` | Shared shadcn primitive or platform composition | available |
| `Pickers/ColorPicker` | Grafana adapter in platform tokens | available |
| `Pickers/ColorPickerInput` | Grafana adapter in platform tokens | available |
| `Pickers/RefreshPicker` | Grafana adapter in platform tokens | available |
| `Pickers/SeriesColorPicker` | Grafana adapter in platform tokens | available |
| `Pickers/StatsPicker` | Grafana adapter in platform tokens | available |
| `Pickers/UnitPicker` | Grafana adapter in platform tokens | available |
| `Pickers/ValuePicker` | Combobox / Segment | available |
| `Plugins/BarGauge` | Grafana adapter in platform tokens | available |
| `Plugins/BigValue` | Grafana adapter in platform tokens | available |
| `Plugins/DataSourceHttpSettings` | Grafana datasource administration excluded; deployment configuration | excluded |
| `Plugins/FormattedValueDisplay` | Grafana adapter in platform tokens | available |
| `Plugins/PanelChrome` | VisualizationPanel | available |
| `Plugins/RadialGauge` | Own SVG gauge; pinned Grafana version has no public RadialGauge export | available |
| `Plugins/SeriesTable` | Grafana adapter in platform tokens | available |
| `Plugins/Table` | Platform panel/table composition | available |
| `Plugins/VizLayout` | VisualizationPanel + shared layout | available |
| `Plugins/VizLegend` | TimeSeriesChart keyboard legend and SeriesTable | available |
| `Utilities/ClickOutsideWrapper` | Platform runtime/layout utility | available |
| `Utilities/ErrorBoundary` | Platform runtime/layout utility | available |
| `Utilities/RenderUserContentAsHTML` | No raw HTML surface; safe text/structured-data presentation | excluded |
| `Utilities/useDelayedSwitch` | Platform runtime/layout utility | available |
| `Utilities/useSplitter` | ResizablePanelGroup and ResizableHandle | available |
