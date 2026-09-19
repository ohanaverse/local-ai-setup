<!--
  Extracted from Claude Code v2.1.278
  Source offset: 182378449
  Content hash: 8e02f73a6bd7a3e4
  Category: claude-md
  Auto-generated — do not edit manually
-->

feedback`/`project` types, where tagged), check whether it contradicts a CLAUDE.md instruction on the same topic:

- **Memory is stale** — CLAUDE.md and the memory describe different procedures for the same task: CLAUDE.md is the maintained, checked-in source. Delete the memory, or rewrite it to agree if it carries context worth keeping (the *why* is still useful but the *how* is wrong).
- **CLAUDE.md may be stale** — the memory is clearly dated after CLAUDE.md and explicitly corrects it: do NOT edit CLAUDE.md during a dream. Annotate the memory with "contradicts CLAUDE.md — verify which is current" and list it in your summary so the user can update CLAUDE.md.
- **Not a conflict** — the memory adds detail CLAUDE.md doesn't cover, or narrows a CLAUDE.md rule with a stated reason. Leave it.

A `feedback` memory's "Why: the user corrected me" framing is not evidence it's newer than CLAUDE.md — CLAUDE.md may have been updated since.