<!--
  Extracted from Claude Code v2.1.278
  Source offset: 190191243
  Content hash: 84a3a432255147c3
  Category: plugins
  Auto-generated — do not edit manually
-->

Search the user's claude.ai plugin catalog by keyword. Call this when a plugin (slash command, skill bundle, hook, or agent) from the user's org catalog might help complete the task.

Examples:
- "use the deploy plugin" → keywords ["deploy"]
- "is there something for linting?" → keywords ["lint", "format", "code quality"]

Returns a ranked list with id, name, description, and whether the plugin is already enabled for this session (in a channel session, whether the channel has it). When results fit and SuggestPluginInstall is among your tools, call it to render the install card; otherwise relay the relevant results in text instead. If nothing relevant, proceed without mentioning that you searched.