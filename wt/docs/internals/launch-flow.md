# wt launch flow: post-exit, survey, sessions, profiles

Internals reference for `wt/`, reached from [`wt/CLAUDE.md`](../../CLAUDE.md). Backticked paths are relative to `wt/` unless they start with `../`.

## Post-exit flow

After the launched subprocess exits, the TUI and non-TUI paths run the same fixed sequence — order and user-facing behavior are in [docs/wt-agents/README.md#post-exit-order](../wt-agents/README.md#post-exit-order); don't reorder one path without the other. Code notes:

- **Summary line** (`wt: <agent> · <model-id> · <duration>`): formatter `internal/agents.Summary`, the single source for both paths. Printed on success and non-zero exit; never affects the exit code. Duration is measured when the agent exits.
- **Refcount release first.** `survey.ReleaseSession()` (`internal/survey/stop.go`, → `refcount` `Release(pid)`) drops wt's own entry before the stop picker — otherwise the model just used would always count as in use.
- **Stop picker** (`survey.Picker`): offers running local models with refcount zero, a trusted probe, and a stop backend (`lifecycle.CanStop`). Its prompt (`chooseLabeled`, shared with `wt stop`) is one line applied on Enter — space-separated numbers, `a`/`all`, or nothing — with no ticked state and no confirming Enter (#139); an unreadable line rejects the whole line and re-prompts. Deliberately no more than that: whatever it leaves running, `wt stop` stops. Its loop (`stopEntries`, shared with `wt stop`) stops each model with `lifecycle.StopModelDeferred` (route removal written, no restart) and settles from a `defer`, so it reaches `lifecycle.SettleRoutes` on every exit path including Ctrl+C and failures — but only when some stop owed a restart, since a batch that removed nothing must not bounce the proxy. So N stops cost one LiteLLM restart, not N overlapping `kickstart -k`s (#142). Silent for non-TTY stdin, command agents, and native models. Ctrl+C/SIGTERM is caught on the picker's own context: at the prompt the read (on a goroutine raced against the ctx) is abandoned; during a stop the in-flight stop is cancelled and the rest skipped. That reader goroutine prints the prompt and the picker goroutine prints `cancelled`, so both write through one `lockedWriter` — a signal landing mid-prompt is otherwise a data race (on a test's `bytes.Buffer`) and interleaved output on a terminal. Either way it prints `cancelled`, runs the TTY drain, and still reaches the summary.
- **Stale-pricing notice**: printed when the registry's OpenRouter prices are more than 7 days old, or were never refreshed (`wt: token pricing last refreshed <date> — run 'wt cloud-sync'` / `… has never been refreshed …`). Nothing stores the date: `agents.LastPriceRefresh` takes the newest `pricing_updated_at` among the OpenRouter-priced models (`Model.PricingUpdated`), and `wt cloud-sync`'s openrouter flow stamps every model it matches, changed or not, so the newest stamp is the last refresh. A missing or mistyped stamp is skipped, never an error, and so is one more than a day ahead of the clock (a typo such as 2062, which would otherwise silence the notice until then). wt never refreshes on its own (a refresh is a network call, a registry write and possibly a proxy restart); the notice is the only reminder. Skipped for command agents (`m.ID == ""`) and when the catalog has no OpenRouter-priced model (`agents.HasOpenRouterPricedModel`, #151: a model whose `provider_id` is `openrouter`, and no other; ollama cloud models and other cloud providers' models don't count): `wt cloud-sync` would have nothing to refresh. `internal/cloudsync.OpenRouterPriced` is the same rule over registry rows; `docs/contracts/catalog-predicates` pins both.

## Session survey (Go)

> **The survey is switched off** (`survey.Enabled = false`, `internal/survey/prompt.go`; 2026-10-05, #136). `PromptRun` returns `""` at once, so no launch path asks anything or prints an after-survey block. Everything below still describes the code, which is kept so the survey can come back by flipping that one value: the questions, the store, `wt stats` and the picker's SURVEY column (both keep reporting the answers already in `survey.jsonl`, which thin out as they pass the 30-day window). The package's `TestMain` turns `Enabled` on so the kept code stays tested; `TestSurveyIsSwitchedOff` pins the shipped value. The stop picker lives in this package but is not the survey and is unaffected.

`internal/survey` records a post-session verdict (worked? speed? quality? task description) for every launch with a model — skipped for command agents (`m.ID == ""`) and native models (`m.Native`). `survey.PromptRun` is the single implementation for both paths (`cmd/wt/launch.go runAgentCmd`, and `internal/tui` via the same capture-then-emit pattern the summary uses). It no-ops when stdin is not a TTY and **returns** the after-survey stats block rather than printing it, so the caller can place it after the stop picker and summary.

- **Paste drain:** after the last question, `PromptRun` flushes the kernel TTY input queue (`drainTTYInput`: `TIOCFLUSH` darwin / `TCFLSH` linux / no-op elsewhere) — a multi-line paste at the free-text question delivers all its lines at once and anything unconsumed would otherwise execute as shell commands in the parent terminal after wt exits. Targeted at `os.Stdin`, not the injected reader, because the residue lives in the kernel queue for fd 0. Don't remove this to "simplify" the survey.
- **Store:** `~/.config/agent-wt/survey.jsonl`, wt-owned, 30-day retention, pruned on every write (mirrors `internal/usage`).
- **Worked%** = `worked / (worked + failed)` over *answered* surveys; skips are recorded but excluded from the denominator. The model picker's SURVEY segment (`✓<pct> q<quality> s<speed> n<answered>`) shows `⚠` instead of `✓` when `WorkedPct < 70%` and `Answered >= 3`.
- **Model ids:** surveys and usage key on a free-form id and never require a registry entry. Unregistered on-disk models use `config.DiscoveredModelID(family, artifact)` (the probe family — `omlx` for an `omlx-6bit` row); a registry match keeps its registry id.
- Reporting: `wt stats` — see [docs/wt-stats.md](../wt-stats.md).

## Profiles (`internal/profiles`)

Opt-in overlays from `~/.config/agent-wt/profiles.toml` (absent = enabled, zero profiles), resolved per agent × model (`profiles.Resolve`). Operator reference and per-agent mechanisms: [docs/wt-agents/profiles.md](../wt-agents/profiles.md).

- **Where applied:** both launch paths via `applyProfileForLaunch` (`cmd/wt/launch.go`; the TUI reaches it through the `profileApplier` seam set in `main.go`), and `wt smoke` via `applyResolvedProfile` directly. Skipped for command agents and native models.
- **Confirmation:** an interactive launch asks `apply? [Y/n]` on `/dev/tty` (`confirmProfile`); with no TTY it auto-applies (default yes). `wt smoke` never prompts.
- **Apply order** (`applyResolvedProfile`): env/args → `config_content` → wrapper. Smoke's one-shot args are inserted after profile flags but before the wrapper — codex silently drops a `-c model_provider=...` that trails `exec <prompt>`, and the wrapper splices the current argv.
- **`config_content`** backs up and rewrites the agent's config file (claude `<worktree>/.claude/settings.local.json`, codex `~/.codex/agent-wt-profile.config.toml`; opencode merges into `OPENCODE_CONFIG_CONTENT` instead). The cleanup must be called explicitly after the run (not deferred — `runAgentCmd`'s `os.Exit` would skip it). `SelfHeal` restores an orphaned backup from a killed session; it runs on *every* launch of that agent, before profiles.toml is even loaded.
- **Failure posture:** load/validate/apply errors degrade to an unprofiled launch with a stderr warning (a `config_content` failure also reverts env/args already applied). The one fatal case: a wrapper naming a missing binary.
- **CLI:** `wt profile list`, `wt profile show -A <agent> -M <id>` (dry run), `wt profile status`, `wt profile on|off` (refuses to write if profiles.toml failed to parse).

## Sessions (Go)

wt does not manage agent sessions and never resumes one (#198, #204): no lookup, no prompt, no resume flag, on any launch path. `BuildLaunchCmd` appends the user's passthrough args unchanged, so continuing a conversation is the agent's own flags after `--` — in the picker too:

| | Continue the latest | A specific session | Fork it |
|---|---|---|---|
| claude | `claude-wt -- --continue` | `claude-wt -- --resume <id>` | `claude-wt -- --continue --fork-session` |
| opencode | `opencode-wt -- --continue` | `opencode-wt -- --session <id>` | `opencode-wt -- --continue --fork` |

claude's `--continue` means "the most recent conversation in the current directory", and wt starts the agent in the worktree (or, with `--cwd`, the directory the command was typed in), so it finds the sessions for that directory.

> **Don't bring the lookup back.** wt used to find the newest session recorded for the launch directory and add the agent's resume flag itself (the `Resumer` capability, `agents.ResumeSession`, the `internal/session` package, opencode's `opencode db` query, the picker's resume prompt, `--debug-session`). That appended a one-shot run (`-- -p "..."`) to whichever conversation was newest, including one another process was using (#204), failed an opencode launch whose resumed session had stored a different model (#198), and doubled the flag when the user passed their own. All of it is removed; claude's project-directory slug survives only as the unexported `claudeProjectSlug` in `internal/agents/claude.go`, for `StateDir`.

> **Native models are not special-cased.** Whether a resumed session's stored model overrides the chosen one is the agent's behaviour; wt does not guard against it.
