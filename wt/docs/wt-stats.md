# `wt stats`

Reports accumulated post-session survey stats: did an agent×model combo
work, how fast, how good. wt collects this data via the post-session
survey prompt (see [wt-agents/README.md#post-run-summary-line](./wt-agents/README.md#post-run-summary-line));
`wt stats` reports it.

```bash
wt stats [--window 1d|7d|30d] [--model <id>] [--agent <name>]
```

- `--window` — defaults to `30d`.
- `--model` — narrow to one model id.
- `--agent` — narrow to one agent.

## Output

One row per model's `(all)`-agents aggregate, plus one row per observed
(agent, model) combo — sorted by model id, `(all)` first, then agent name.
A combo with zero answered and zero skipped surveys in the window is
omitted. An empty store (or a filter matching nothing) prints
`no survey data` and always exits 0 — `wt stats` is a report, never a
gate.

```
$ wt stats
┌────────────────────┬────────┬─────────┬─────────┬───────┬────┬─────────┐
│ MODEL               │ AGENT  │ WORKED% │ QUALITY │ SPEED │ N  │ SKIPPED │
├────────────────────┼────────┼─────────┼─────────┼───────┼────┼─────────┤
│ ollama/gemma4:9b    │ (all)  │ 96%     │ 4.2     │ 3.9   │ 12 │ 2       │
│ ollama/gemma4:9b    │ claude │ 95%     │ 4.3     │ 4.0   │ 8  │ 1       │
│ ollama/gemma4:9b    │ codex  │ 100%    │ 3.8     │ 3.5   │ 4  │ 1       │
└────────────────────┴────────┴─────────┴─────────┴───────┴────┴─────────┘
```

`WORKED%`, `QUALITY`, and `SPEED` render `-` when there is nothing to
average (zero answered surveys, or zero rated surveys for that column).
`WORKED%` is `worked / (worked + failed)` over *answered* surveys only —
skips are tracked in the `SKIPPED` column but excluded from that
percentage.

## Where the data comes from

Every model-driven agent launch (TUI or non-TUI) prompts up to three
questions immediately after the agent exits:

1. **Did it work?** `[y]es / [n]o / [s]kip (Enter=skip)` — `n` records a
   failure and stops; `s`/Enter records a skip and stops.
2. **Speed 1(slow)-5(fast)?** (Enter=skip) — only asked after `y`.
3. **Quality 1(bad)-5(great)?** (Enter=skip) — only asked after `y`.

The prompt is silent (no output at all) when stdin is not a TTY, or when
the launch had no model (command agents like `shell`). There is no config
toggle to disable it — every-exit with a one-keypress skip is the intended
trade-off.

Right after answering, the same accumulated stats `wt stats` reports are
printed for the current model (all agents) and the current agent×model
combo, at the 1d/7d/30d windows.
