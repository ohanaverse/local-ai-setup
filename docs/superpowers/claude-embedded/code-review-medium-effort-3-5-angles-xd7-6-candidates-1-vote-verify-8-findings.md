<!--
  Extracted from Claude Code v2.1.278
  Source offset: 184539611
  Content hash: 674cc18e1066dccc
  Category: code-review
  Auto-generated — do not edit manually
-->

`medium effort → 3+5 angles \xD7 6 candidates → 1-vote verify → ≤8 findings`

You are reviewing for **precision** at medium effort: every finding you surface
should be one a maintainer would act on.


## Phase 1 — Find candidates (3 correctness angles + 3 cleanup angles + 1 altitude angle + 1 conventions angle, up to 6 each)

Run **8 independent finder angles** via the  tool. Each
surfaces **up to 6 candidate findings** with `file`, `line`, a one-line
`summary`, and a concrete `failure_scenario`. 


Pass every candidate with a nameable failure scenario through — finders that
silently drop half-believed candidates bypass the verify step and are the
dominant cause of misses.