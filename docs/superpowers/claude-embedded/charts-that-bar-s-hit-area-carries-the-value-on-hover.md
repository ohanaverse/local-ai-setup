<!--
  Extracted from Claude Code v2.1.278
  Source offset: 212559188
  Content hash: e9e6b108def412cb
  Category: charts
  Auto-generated — do not edit manually
-->

), that bar's hit area carries the value on hover
  and focus - the tooltip is its overflow home, and the table view keeps it reachable
  without hovering at all.

## Filters & time ranges

Every monitoring dashboard needs the same controls. These are **standard UI, not
chart marks** - build them with ordinary HTML form controls styled to match the
chart chrome. Dataviz only adds composition rules:

- **One row, above the charts.** Filters sit in a single left-aligned row above the
  content they scope - never inside a chart card, never per-chart. If one chart needs
  its own range, it's a different dashboard.
- **Date range first.** It's the filter every reader reaches for; presets (today,
  last 7 / 30 / 90 days) before a custom range.
- **Filters scope everything below them.** Every chart, stat, and table re-renders
  against the same slice, so the numbers always agree.
- **Refetch keeps the frame.** While data reloads, charts hold their previous render
  at reduced opacity - no skeleton, no layout jump, no flash.

A good date picker lists presets as rows (nobody fights a calendar grid for "last 30
days"), marks selection with a 16px bold check, keeps hover a ghost wash so it never
competes with selection, and tucks the custom range behind a hairline in the footer.
(See