<!--
  Extracted from Claude Code v2.1.278
  Source offset: 212558060
  Content hash: 67a54dd7acf73382
  Category: charts
  Auto-generated — do not edit manually
-->

 string concatenation.
- **Values lead, labels follow.** In the tooltip the value is the Strong,
  high-contrast element and the series name is secondary - the legend's hierarchy
  inverted, because here the reader has the series and wants the number.
- **Line keys, not boxes.** Tooltip rows key their series with a short stroke of the
  series color; at tooltip density a filled box is data-weight ink doing a label's
  job. (Legends still mirror the mark: rect for bars/areas, line for lines.)
- **The hit target is bigger than the mark.** A mark's hover/focus area includes its
  2px surface gap and then some - never only the painted pixels. An 8px scatter dot is a
  pinpoint nobody hits reliably; give each point a transparent hit area of at least
  **24px**, or - for dense scatter - a nearest-point / Voronoi layer so the pointer only
  has to be *closest*, not dead-center. (The crosshair already does this for the X on
  line and bar charts; scatter and bubble need the per-point version.)
- **A value pushed off its mark lives in the tooltip.** When a label won't fit inside a
  small bar (see