# Follow-up issues

Collected at final review of the user-guide set, 2026-08-29. Items are also
documented in context in `docs/guides/` (references below); this file tracks
the actions. Check off and add notes as items close.

---

## 1. Rotate the LiteLLM master key (security) — OPEN

`sk-litellm-ui-master-key-4000` in `docs/reference/litellm-admin-ui-setup.md`
(§"what was actually set") **is the live proxy master key** — verified against
the plist's `LITELLM_MASTER_KEY` during final review. Committed since the
initial commit; present in git history. Localhost-bound, but anyone with repo
read access can control the proxy.

- [ ] Rotate: new key value in `~/Library/LaunchAgents/local.litellm.proxy.plist`
      (`LITELLM_MASTER_KEY`; also `UI_PASSWORD`/`LITELLM_SALT_KEY` worth rotating),
      then `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`
- [ ] Redact `docs/reference/litellm-admin-ui-setup.md` (placeholders, not real values)
- [ ] Decide on history: rewrite (git filter-repo) or accept (localhost exposure)
- [ ] Audit other docs copied from that session for the same class of leak

Secret-handling rules live in: `docs/guides/00-config-map.md` (Gotchas,
"Secrets on disk"), `docs/guides/01-initial-setup.md:509`, `04-litellm-config.md`
(literal api_key gotcha).

## 2. Fix agent-worktree README drift — OPEN

Three spots in `~/github/ohanaverse/agent-worktree/README.md` are stale vs
wt 0.1.0 (both the installed binary and repo source at `bf98d14`):

- `:75` — `--cwd` claims "skip TUI and session resume"; actually skips only the
  interactive prompt — a prior session still auto-resumes (`cmd/wt/launch.go:28-44`,
  resume flag appended in `internal/agents/agents.go:287-288`)
- `:119` — `d` "toggle between code and design tag groups" has no handler in any
  TUI source version; picker footer has no `[d]`
- `:120` — rotation state is the single global `rotation.state`; per-tag
  `rotation-<tag>.state` files are legacy migration inputs (deleted after)
- also `:47`/`:74` — `-w` short form removed; `-W` is the real flag

- [ ] Fix the README lines in the agent-worktree repo
- [ ] Then prune the corresponding caveats: `docs/guides/03-model-families.md:233`,
      `docs/guides/06-wt-agents-and-models.md:36, :71, :130, :171`

## 3. Converge `litellm_exposed` bookkeeping — OPEN

All 11 `model_list` entries in `~/.config/litellm/config.yaml` predate
modelman's flag bookkeeping; `modelman.toml` has zero `litellm_exposed = true`.
The guides treat `config.yaml` as routing truth and document the drift
(`02:264`, `04:286`, `08:176`).

- [ ] Re-register the 9 non-registry models in modelman (or decide they stay
      hand-managed) — currently `registry.toml` carries only the `ollama` provider
- [ ] `uv run modelman expose <id>` for each in-registry model (`ollama/qwen3.8:27b-mlx`,
      `ollama/ornith-1.5:35b`) so flag state matches disk
- [ ] Downshift the drift notes in guides 02/04/08 to a historical footnote

## Minor / already handled notes

- wt PATH shadowing (stale `~/.local/bin/wt` hides GOPATH builds): documented in
  `docs/guides/08` §4 — close by rebuilding over the PATH copy or aliasing.
- Machine-specific expected outputs (model counts, PIDs, checkout SHAs) in the
  guides are dated stamps and hedged as drift-aware — accepted format property.