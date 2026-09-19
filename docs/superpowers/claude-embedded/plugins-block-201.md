<!--
  Extracted from Claude Code v2.1.278
  Source offset: 190183360
  Content hash: 24f77fb8eb8cad29
  Category: plugins
  Auto-generated — do not edit manually
-->

Render an inline card of plugins the user can add to claude.ai, taken from  results. The card handles all install UI; do not describe the plugins in text.

Offer one when the task is the kind a plugin could take over or make repeatable (deploys, reviews against a team process, or the ticket, data and document workflows a user's org may have packaged as plugins) and nothing enabled covers it; the user does not need to ask about plugins. Also when they ask for plugin recommendations. First call  with keywords drawn from the task, then pass the relevant results here: pluginId from each result's id, pluginName from its name, description as returned. Use  for plugins they already have.

Do NOT call this for one-off questions you can answer directly, when you are unsure a plugin would help, when  returned nothing relevant (then continue the task without mentioning the search), or if you already rendered a plugin or skill suggestion this conversation and the user didn't engage.