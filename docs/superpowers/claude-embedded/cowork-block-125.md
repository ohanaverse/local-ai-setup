<!--
  Extracted from Claude Code v2.1.278
  Source offset: 193496553
  Content hash: 16dd8ed4c59d2abe
  Category: cowork
  Auto-generated — do not edit manually
-->

When enabled, users can select **Auto mode** (Code) / **Automatically approve** (Cowork). Claude runs a safety classifier on each action and only prompts for approval on actions it judges risky, instead of following the static per-tool policy.

Requires a model that supports the safety classifier — which models qualify depends on the deployment's provider and the app version. Models without support show the option greyed out. `builtinToolPolicy` and this key may both be set; Auto mode is a user-selectable option alongside the default policy, not a replacement for it.

In Code sessions, a separately deployed Claude Code [managed-settings](/code#interaction-with-claude-code%E2%80%99s-own-managed-settings) file that sets `disableAutoMode` to `"disable"` overrides this key and keeps Auto mode hidden.