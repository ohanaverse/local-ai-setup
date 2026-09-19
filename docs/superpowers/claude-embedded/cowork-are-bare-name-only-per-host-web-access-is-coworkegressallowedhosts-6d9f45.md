<!--
  Extracted from Claude Code v2.1.278
  Source offset: 193487327
  Content hash: 6d9f451c676df85f
  Category: cowork
  Auto-generated — do not edit manually
-->

")}` are bare-name only: per-host web access is `coworkEgressAllowedHosts`.

Scoped `Bash(…)` rules apply in Code sessions and in VM-sandboxed Cowork sessions (`requireFullVmSandbox`); Cowork's own sandboxed shell honors bare names only. Anchor file patterns with `**/` (`Read(**/secrets/**)`), because in the VM sandbox a host absolute path does not match. Scoped rules need a build that supports them across the whole fleet (`disableAutoUpdates` pins builds): an older build passes a scoped entry to Claude Code unchecked. An entry whose pattern contains `)` followed by a space or comma is enforced only through Claude Code's managed-settings channel, so another Claude Code [managed-settings source](/code#interaction-with-claude-code%E2%80%99s-own-managed-settings) replaces it unless that source sets `parentSettingsBehavior` to `"merge"`; every other entry is enforced either way.

An unusable entry (a lowercase tool name, an unbalanced parenthesis, a scoped `${wc.join("(…)