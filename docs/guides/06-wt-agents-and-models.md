# wt — pick a worktree, agent, and model, then launch

> Use this to: launch claude-wt (or any `*-wt` shim) in a git worktree on a chosen model, understand what the picker offers, where launch history lives (`last-picked.state`, `usage.jsonl`), and which flags skip which screens.
>
> Verified against: wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29 · revised 2026-10 (commands checked against `wt --help` and `llmbench --help`, not re-run live; the Sushi agent notes in §6 are from the code and the design, and no agent was launched on a Sushi pack for this guide)

## Prerequisites

- **Registry has models.** `~/.config/local-ai/registry.toml` is wt's model source for providers, cloud models and local-model metadata; which local models are offered comes from wt's live inventory of what is on disk or running. If it is empty or missing, create it first — see [02-providers-and-models](02-providers-and-models.md).
- **`wt` built and on PATH** (`~/.local/bin/wt`):

  ```bash
  make -C ~/github/ohanaverse/local-ai-setup/wt install
  ```

  **Check the binary:** if `wt --help` lacks a flag or command this guide names (`--root`, `--new`, `wt worktrees`, `wt rescue`), the `wt` on PATH is older than the guide. The rebuild command above is the fix: `make -C wt install` builds `wt-session`, `wt` (the same binary under two names) and `wt-model`, re-seals each and installs them and the shims over the PATH copies in `~/.local/bin/` (a bare `go build` would refresh only one of them); a GOPATH build would be shadowed (see [08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md) §4).
- **Shims on PATH** (`claude-wt` … `shell-wt` in `~/.local/bin/`). Missing? `make install` from `~/github/ohanaverse/local-ai-setup/wt` — verified real (copies `bin/*-wt` to `~/.local/bin/` and re-signs on macOS to dodge AMFI `SIGKILL`/exit 137). All 7 shims present here:

  ```
  /Users/user_name/.local/bin/claude-wt  /Users/user_name/.local/bin/codex-wt
  /Users/user_name/.local/bin/pi-wt      /Users/user_name/.local/bin/shell-wt
  /Users/user_name/.local/bin/agy-wt     /Users/user_name/.local/bin/copilot-wt
  /Users/user_name/.local/bin/opencode-wt
  ```
- **Know the model shapes** the picker will offer: [03-model-families](03-model-families.md) explains `family` and `tags`, which decide picker contents.

## TL;DR

```bash
# from: any git repo, e.g. ~/github/ohanaverse/local-ai-setup
claude-wt                    # TUI: pick worktree/branch + model
claude-wt -W my-feature      # skip worktree picker; a new name is created and said so
claude-wt -W my-feature --new  # --new is accepted and ignored (kept for old scripts)
claude-wt -W my-feature -A claude -M ollama/qwen3.8:27b-mlx   # skip everything (example id — use one of yours)
```

`-W` (uppercase) is the real worktree flag — confirmed by `wt --help` (both the installed binary and repo source, which agree).

`-W <name>` opens what the name matches: an existing worktree (one `launching in …` line when nothing warns, no question), an existing branch with no worktree (`creating worktree <p> for branch <b>`), or a remote-tracking name (a new local branch for it: `creating worktree <p> for branch <b> tracking <r>`). A name that matches neither is created and announced before it exists — `creating branch <b> and worktree <p>`; the base is not announced. Only a launch warning (the root itself, a drifted root, a session holding it, or a merged PR for the branch, which `-W`, `--root` and `--cwd` look up with `gh` and report only when `gh` answers within about a second) asks anything first: one default-yes `continue?` question on a terminal. With no terminal the warnings print and the launch proceeds; `--new` is accepted and ignored. The picker's "+ New worktree…" prompt never asks, and its bare-branch and remote-only rows announce their creation the same way before the launch commits.

**When the agent exits**, wt asks nothing about the worktree: there is no exit prompt, and an untouched session worktree (or anything else) is cleaned up when you ask — `wt worktrees` ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md) §9) lists what can go and `wt remove` removes named targets. The agent's exit status is unchanged. The `[launch] exit_prompt` key in `~/.config/agent-wt/config.toml` that once controlled the prompt is tolerated and ignored: no value is validated or consulted and any value survives wt's saves; a non-empty value prints one note per process: `wt: note: config.toml: [launch] exit_prompt is no longer used and is ignored`.

## Steps

### 1. Shims and agent support matrix

Every `*-wt` shim is one line forwarding to the unified binary (`exec wt --agent <name> "$@"`). Mapping and capabilities, copied verbatim from `~/github/ohanaverse/local-ai-setup/wt/README.md` (all 7 launcher binaries verified present in `~/.local/bin/`; matrix semantics verified against `internal/agents/` in source):

| Launcher | Agent | Model picker |
|---|---|---|
| `claude-wt` | Claude Code | Yes |
| `codex-wt` | OpenAI Codex CLI | Yes |
| `copilot-wt` | GitHub Copilot CLI | Yes |
| `opencode-wt` | OpenCode | Yes |
| `pi-wt` | pi-coding-agent | Yes |
| `agy-wt` | Antigravity CLI | No |
| `shell-wt` | Shell command | No |

Interactive behaviors (key feel) are not driven here — see the UNVERIFIED note in Verification.

### 2. TUI flow

Run a bare `wt` (from any git repo; shims pin the agent up front, skipping the agent screen). Phases in `internal/tui/app.go`: worktree list → agent/command picker (only when no `-A` was supplied) → agent+model screen → launch.

- **Worktree picker:** scroll with `j`/`k` (or arrows), `/` to filter, `n` or selecting `+ New worktree…` to create a branch, `enter` to select, `q` or `esc` to quit. A row whose branch has a GitHub pull request shows `#<number> <state>` (`open`, `draft`, `merged`, `closed`) after the branch and the PR title on its second line; the filter matches the number. The tags come from `gh pr list` and appear a moment after the picker; without `gh` or a GitHub remote there are none ([wt-agents/README.md](../../wt/docs/wt-agents/README.md)).
- **Agent+model screen header** shows the agent and the active tag slot (`internal/tui/layout.go`):

  ```
  agent : claude
  tag   : code
  ```

  The tag slot shows the **first** tag of the effective set — under `-T code,design` it reads `tag   : code` while the list holds the union of both tags — and falls back to `default_tag = "code"` (see §6) when no `-T` was given; `-T` narrows the list itself to models carrying any of its tags. Models are one table row each, with the header rendered as the list title: `FAMILY  MODEL  TIER  LOC  STATUS  RUNNING  COST  1D  7D  30D`. TIER is the advisory capability tier `modeltiers run` wrote to `~/.config/local-ai/tiers.toml` (`MODELTIERS_OUT` overrides): the tier's letter (`M` marathon, `D` deep, `S` standard, `Q` quick) and the rounded aligned score, e.g. `S44`, with the digits right-aligned down the column; an unrated model that has an aligned score shows the score alone, and `-` means no score, not in the file, or no file ([12-model-tiers](12-model-tiers.md#see-tiers-in-the-wt-picker)); the score also orders the model rows (see below), but nothing is routed on a tier. Native models (the agent's own subscription model) always sort first, alphabetically; then any slot-profile rows ([slot-profiles](../../wt/docs/wt-agents/slot-profiles.md)), alphabetically; then alias rows, by tier score, highest first (an alias shows the score of its first-choice target; one with no score comes after the scored ones, by id); then local models that are running (not still loading); then every other model. Within the last two groups models are ordered by tier score, highest first; ties fall back to family, then id. A model with no score (say, one added since the last `modeltiers run`) sits next to the best-scored model of its family, with its TIER cell still `-`; with no scored family member, or no `tiers.toml`, it sorts after the scored ones. Cost and usage do not order anything. COST is the price in force when the picker opens: for a model with time-of-day prices (`cost.time_prices` rows, which `wt-model cloud-sync` writes for ollama's off-peak hours and for the models OpenRouter prices by time of day) that is the current window's price, marked `~` under the heading `COST (~ varies by time)`. The line under the table names the highlighted model's client sessions on the model server (when any) and its price (`cost~ <in>/<cached>/<out>`, the `~` when it depends on the time) and then, where there is room, its tier in full (`tier standard · score 44 (3 sources) · high confidence`, shortened part by part on a narrow terminal); the price is shown whole or not at all: on a terminal too narrow for both, the price takes the line ([wt-cloud-sync.md](../../wt/docs/wt-cloud-sync.md#the-price-the-picker-shows) has the rules, and what a row written by hand needs). STATUS is `ok`, `unknown` (the probe could not tell whether the model is on disk — a failed local probe, so wt lists the model rather than hide it), or `new` (discovered, not in the registry); RUNNING is `run` while a model is serving and `load` while omlx is still loading it (Enter waits for the load). On a terminal too narrow for the whole table, columns are dropped whole — 30D first, then 7D, 1D, COST, TIER, LOC and FAMILY — while MODEL, STATUS and RUNNING always stay; the `/` filter still matches the dropped columns' text. The `~` mark and its heading take a little room, so at a few widths a table with a time-priced model in it shows one column fewer than it would without one; the line under the table still names the highlighted model's price. A local model that is not on disk has no row, and a non-running `mlx_lm_server` pairing is not listed. A discovered model is routed like any other running local model — under its discovered id, `<family>/<artifact>` — so it stays selectable. Note that the list is **not** filtered by `default_tag` — only an explicit `-T`/`-F` narrows it, and only those hide discovered (unregistered) rows. After `/`, typing a family name (or any part of a model ID) narrows the list. The cursor starts on the `-M` pin; else on the row the last launch picked, the row the `> ` marker sits on (`last-picked.state`, §5), when this table shows it and it is not blocked, so a bare Enter repeats the previous selection; else on the first row that is not blocked in the order above (a stopped local model or alias counts: Enter on it starts it). With the sort above the last case is the native row when one is not blocked, else a slot profile, an alias or a running local model, in that order; the first launch ever, and any launch whose remembered row is gone, blocked or not offered to this agent, gets this row. The cursor can therefore open on a Claude-subscription row (an alias over a subscription target, or a `claude-subscription` model), which rotation used to skip: after a launch of such a row it reopens there (while that row is not blocked) as your own previous choice, and otherwise only when nothing was launched before or the remembered row is unusable; Enter on it is an explicit choice of a visible row, and the subscription guard still runs before the launch (opt-in `[model-server] subscription`, an https or loopback endpoint, a claude.ai login). A launch with no pin and no terminal still never chooses an alias or a subscription row. The standalone pickers `wt smoke` and `wt start` use when no model id is given start on their first row, which under native-first is the **native** model; they have no alias group, so an alias there sorts among the other models by its family.
- **On the model screen:** `j`/`k`/arrows navigate (with wrap-around), `enter` launches a cloud/already-running row and **starts** a non-running local one (below), `q` quits, `esc` pops back. Footer reads `[↑/↓] navigate   [enter] launch or start   [q] quit`.
- **Starting a non-running local model:** `enter` asks the model server to start it (a start job; the server runs it through its own engine) instead of launching straight away. A progress screen names the job's step with elapsed time, or `queued behind another start or stop on the model server`; `ctrl+c` detaches from the job (it keeps running on the server; the table says so) and `c` cancels it. If the start would displace a running model — mtplx's one occupant, or on omlx the models it is expected to unload to make room — or load beside other providers' models, the picker asks, naming them; Cancel is the default. On success wt goes on to launch the agent; on failure the picker returns with the server's message on its status.
- **Rows that cannot be used** say why, in the model server's words: for example a cloud model whose provider has no LiteLLM mapping. A non-running row of a local provider wt cannot start would say `local model "<id>" is not running, and wt cannot start provider "<provider>" — start it with that provider's own tool` (for an `mlx_lm_server` pairing: ``… and wt cannot start an mlx_lm_server pairing — start it with `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>` ``), but no such row occurs for the probed providers today (ollama, omlx and mtplx are startable, and a stopped `mlx_lm_server` pairing has no row) — that message is reached by pinning the pairing. A discovered (unregistered) local model is never refused: wt routes it under its discovered id. A registry model that is not on disk has no row; pinning it (`-M`, `wt start`, `wt smoke`) says `<id> is not on disk — pull or download it first` (starting it would just wait out the warmup timeout), and pinning a stopped `mlx_lm_server` pairing gives the `llmbench provider isolate` command above.
- **Starting or stopping without launching:** `wt start [model]` and `wt stop [model]` do the same start/stop from the command line, and `wt-model stop <provider>` (or `--all`) on the server's machine stops a whole provider (see [`wt/docs/wt-start-stop.md`](../../wt/docs/wt-start-stop.md)).
- **Sessions are the agent's own:** wt always starts the agent fresh — it never looks up a session, asks about one, or adds a resume flag, in the picker or anywhere else. To continue a conversation, pass the agent's own flags after `--`; wt hands them over unchanged on every launch path, the picker included:

  | | Continue the latest | A specific session | Fork it |
  |---|---|---|---|
  | claude | `claude-wt -- --continue` | `claude-wt -- --resume <id>` | `claude-wt -- --continue --fork-session` |
  | opencode | `opencode-wt -- --continue` | `opencode-wt -- --session <id>` | `opencode-wt -- --continue --fork` |

  claude's `--continue` means the most recent conversation in the current directory, and wt starts the agent in the worktree (or, with `--cwd`, the directory the command was typed in), so it finds the sessions for that directory. wt used to resume the newest session by itself, which appended a one-shot run (`-- -p "..."`) to whichever conversation was newest, including one another process was using (#204), and failed an opencode launch whose resumed session had stored a different model (#198).
- **There is no `d` tag-toggle key.** Use `-T code` / `-T design` instead; picker footer is `[↑/↓] navigate [enter] launch or start [q] quit`.

### 3. Model selection

Non-interactive pinning and filtering (all verified against live `wt --help` and `cmd/wt-session/main.go`):

| Flag | Effect |
|---|---|
| `-M, --model <provider>/<name>` | Pin the exact model, e.g. `ollama/qwen3.8:27b-mlx`. A cloud or running-local model launches; a non-running local model is **started first** (timestamped progress on stderr, Ctrl+C cancels); a model that cannot run (a registry model that is not on disk, or one whose provider wt has no start engine for) errors with the reason. The id may be a discovered model's (`<family>/<artifact>`, no registry entry) — but not together with `-T`/`-F`, which hide discovered models |
| `--replace` | With `-M`, start the model even if it means stopping a running one — skips the TTY `y/N` confirmation a non-interactive run would otherwise refuse |
| `-T, --tags a,b` | Keep only models with ANY of these tags (OR within the flag) |
| `-F, --family f1,f2` | Keep only models whose family matches any listed (OR within the flag) |

Precedence when `-M` is combined with `-T`/`-F`: the tags/family filters define the *eligible list*, and the pin must be inside it — otherwise the launch errors ("model \"…\" is not in the eligible list for agent \"…\"") instead of silently ignoring the filters. So `wt -W x -T design -M <some only-code-tagged model>` fails, while a pin that satisfies the filters wins. With no `-M`: one launchable model → that model; several → the picker opens, its cursor on the row launched last when it is shown and not blocked, else the first row that is not blocked (§2); without a terminal the launch refuses, naming `-M`. Launchable = the model server's catalog rows it marks `launch`: cloud rows plus local rows its live probe reports running — a non-running local model is a **start** row, never auto-selected; if models matched but none is launchable, the error names the fix ("no cloud or running local model for agent … — start one with `wt -M <id>`"). This is all in `cmd/wt-session/resolve_remote.go` (`resolveModelRemote`, over the server's catalog) and `cmd/wt-session/launch_remote.go` (source-verified). The `-T` precedence case only shows up once your models' tags actually differ — compare `grep -c '^tags = \[\]' ~/.config/local-ai/registry.toml` (untagged models) vs `grep -c '^\[\[models\]\]' ~/.config/local-ai/registry.toml` (all models).

**Where the models come from:** `~/.config/local-ai/registry.toml` (`wt-model model init` creates it and adds provider rows; `wt-model model add|edit|rm` and the Models tab of `wt-model model` manage its models) and `~/.config/agent-wt/config.toml` (wt-owned) are joined in memory by `internal/config/config.go` `Load()`. Registry providers/models are loaded **last and overwrite anything pre-existing in config.toml — registry.toml is the source of truth**, and wt never writes them back into `config.toml` (see §6). Local rows are then built from the live inventory: a registry local model appears only when it is on disk or running, and an on-disk model with no entry appears under its discovered id `<family>/<artifact>`. To see what wt will load: `grep -c '^\[\[models\]\]' ~/.config/local-ai/registry.toml` prints the model count, and `grep -c '^tags = \[\]' ~/.config/local-ai/registry.toml` how many of those are untagged.

**Old-binary delta:** a `wt` built before the registry-consumer merge (2026-08-28) serves its catalog from `~/.config/agent-wt/config.toml` `[[models]]` blocks (which still sit on disk as one-time migration output), not from `registry.toml`, and its picker misses registry-only entries. Same flags, different catalog; rebuild (Prerequisites) before trusting the picker.

### 4. LiteLLM routing

Every session reaches its non-native models through the model server
(`wt-model serve`, configured under `[model-server]` in
`~/.config/agent-wt/config.toml`; see [wt/docs/wt-session.md](../../wt/docs/wt-session.md)),
whose gateway forwards to the LiteLLM proxy (`localhost:4000`). Choose how a
machine reaches the server (`native`, `local` or `remote`) on the Mode tab of
`wt config`, which writes that table (every change is a plan you confirm
first); `local` there only points at `wt-model setup`, which sets up the
`local.wt-model.serve` LaunchAgent and a token file on the serving machine
(the tab offers to run it when `wt-model` is on `PATH`). With no mode chosen a
launch that needs a model refuses, or on a terminal opens that tab first,
once, before anything is created.
`WT_MODEL_MODE` overrides the stored mode for one run. Editing the table by
hand and starting `wt-model serve` yourself still work. There is no
routing switch and no direct route: a launch never dials a provider or the
proxy itself. The proxy's URL and key live in the `wt-model` config home
(`~/.config/wt-model/`), which the model server reads; the `[litellm]` table
in wt's `config.toml` is its read-only fallback:

```bash
wt-model litellm set --url URL --api-key KEY   # the proxy URL/key the model server uses (the ~/.config/wt-model/ home)
wt-model litellm status                        # url + api_key_set (key never printed)
```

Its `enabled` key is retired: kept on save, read by nothing, and a true
value prints one note per run. `wt litellm on` / `wt litellm off` are hidden
stubs that exit 1. Route management is separate and also wt-owned:
`wt-model litellm sync` writes the routes (every configured cloud model, plus
the running local models — an ollama model while it is pulled; `--dry-run`
previews it), `wt-model litellm list` reads it back, and the model server's start
and stop jobs add and remove local-model routes automatically (see
[04-litellm-config](04-litellm-config.md) §2 and §6).

- **No model is hidden for want of a route; the route is what makes it reachable.**
  The picker has no routing column (#179): every native and cloud model the
  agent is offered is in the list, and local models are listed from what is on
  disk or running on the model server's host, registered or not. A registry
  local model that is not on disk, and a stopped `mlx_lm_server` pairing,
  have no row. What decides whether a model is reachable through LiteLLM is
  its route (a LiteLLM database row, or a `config.yaml` row where database routes are off) — `wt-model litellm list` shows the set; a cloud model is
  routed by `wt-model litellm sync` as soon as it is in the registry, and a local
  model when the model server starts it.

The model name passed to agents is the gateway id — the registry id (e.g.
`ollama/qwen3.8:27b-mlx`), or the discovered id for a local model with no
registry entry — matching LiteLLM's `model_list` entries.

### 5. Launch history

- Every launch that records (a model launch or a slot-profile launch, through the picker or not) writes `~/.config/agent-wt/last-picked.state` and appends to `usage.jsonl` (§8) in one call (`(*usage.StoreImpl).RecordLaunch`). `esc` and canceled prompts write nothing. Nothing advances or rotates: the picker's cursor opens on the `-M` pin, else on the row the last launch recorded (the `> ` marker's row) while it is shown and not blocked, else on the first row that is not blocked (§2, which also covers a subscription row).
- The launcher picker's `> ` marker is read from `last-picked.state`: the row launched last, which can be a slot profile (§10). It marks a row, and the picker's cursor opens on it (after the `-M` pin) when it is shown and not blocked (§2); with no file there is no marker and the cursor opens on the first row that is not blocked, until the next launch. The standalone pickers of `wt smoke` and `wt start` do not follow it: they always open on their first row.

  ```bash
  cat ~/.config/agent-wt/last-picked.state   # from: any dir, read-only — one line, a row id
  ```

- Files named `rotation.state` and `rotation-*.state` in `~/.config/agent-wt/` come from earlier wt versions. wt no longer reads, writes or deletes them; leftovers stay on disk unread, and you can delete them by hand.

### 6. `wt config`

`wt config --help` lists two subcommands (plus an interactive agent editor when run bare — see [wt-config.md](../../wt/docs/wt-config.md)):

```text
Available Commands:
  path        Print the config directory
  theme       Manage the active color theme
```

- `wt config theme` → `list` / `show [name]` / `set <name>` / `unset`; stored in `~/.config/agent-wt/themes.toml`, effective on next launch.
- `wt config path` → prints `/Users/user_name/.config/agent-wt`.

`~/.config/agent-wt/config.toml` shape (wt-owned): a `default_tag = "code"` line plus `[[agents]]` entries (the agents wt is configured for). **wt never writes providers or models into this file** — the Agents tab of `wt config` touches only agents and the default tag; `registry.toml` is written by the Models tab of `wt-model model` and by `wt-model model add|edit|rm`; registry data is overwritten in-memory on every load and never persisted back here (the `Load()` comment in `internal/config/config.go`: "registry.toml is the source of truth"). The `[[providers]]`/`[[models]]` blocks still present on disk are inert migration exchange output (see [00-config-map](00-config-map.md)).

**An agent is offered a model by a rule, not by a list.** Every agent but agy sees every non-native row, sushi and paid OpenRouter rows included, so a new provider such as `rec/flint` ([02-providers-and-models](02-providers-and-models.md) Step 4) needs nothing added for an agent. Three cases are narrower: an agent's own native row (the provider named for it) is offered to that agent alone, never to another; agy sees only its own native row and nothing else; a `claude-subscription` row reaches claude only. `[[agents]]` in `config.toml` marks an agent as configured and nothing else: its `supported_providers` and `default_provider` keys are still parsed and written back untouched (older binaries need them) and ignored, the Agents tab of `wt config` edits the name only, and a list a person edited prints one `wt: note:` line per agent per process saying so. Every non-native model reaches an agent through the model server's gateway (§4): rec and sushi serve OpenAI chat completions only (their provider rows declare no `protocols`), and LiteLLM maps each agent's own protocol onto that. An agent added in the Agents tab is written without a list, which a `wt-model` older than this change cannot load, so run `make install` on every machine before adding one.

**Sushi** (`sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw`, [02-providers-and-models](02-providers-and-models.md) Step 4) works for every agent but agy, however the pack was registered; `wt-model model add sushi ...` changes no agent setting. To check that the model really calls tools from an agent:

```bash
wt smoke --tools sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw --only claude,pi
```

<!-- UNVERIFIED — this smoke run was not made for this guide: it loads the 46 GB pack. The flags are from wt/docs/wt-smoke.md (`--tools`, `--only`); `--only` must be a subset of the agents the model is eligible for, and every agent but agy is eligible for the sushi row. -->

Start the model first (`wt start sushi/…`) or let the smoke run do it; either way the pack must fit the GPU wired limit (guide 02, Step 4). The context window Sushi serves is the 131072 wt starts it with.

### 7. Extras

- `--init` — seed `AGENTS.md` (if missing) plus an agent-specific pointer file, install the `block-main-commit` hook in a git repo (a first-time install is announced, below), then exit (`internal/initseed/`).
- `--check-guard` / `--no-guard` — inspect/remove the `block-main-commit` pre-commit hook that every launch auto-installs in a git repo (blocks commits to `main`/`master`, and commits on a named branch in the primary checkout; `git commit --no-verify` or `WT_SKIP_MAIN_BLOCK=1` is the emergency bypass). A first-time install prints one line on stderr — `wt: installed the block-main-commit hook (--no-guard to skip)` — on every install path (a launch inside a git repo and `wt --init` alike); a hook already present is left alone and prints nothing. Live read-only run in `wt`-managed repo:

  ```bash
  wt --check-guard        # from: ~/github/ohanaverse/local-ai-setup
  ```

  Observed: `wt: main guard is installed in this repo.`
- `--yolo` — exists in 0.1.0, verified in `wt --help` and `cmd/wt-session/main.go`; prepends the agent's skip-permissions flag (claude: `--dangerously-skip-permissions`). Source-verified caveat: pi has no such flag, so `--yolo` is a no-op there (`internal/agents/agents_test.go`: "Pi has no yolo flag").
- `--cwd` — launch in the current directory, inside a git repository or not, with no pickers (the one launch that stays where you typed it; `-W` and the picker start at a worktree's root). The agent starts in that directory, so its own continue flag after `--` (`claude-wt --cwd -- --continue`) finds the sessions of that directory; wt itself never resumes one. Run from a repository's root, `--cwd` warns and proceeds (a terminal with any warning is asked one default-yes question first; a nested launch under an agent that already holds the root carries that consent, so the root warning is not shown — though root drift and any other warning still ask). `-W <name>` naming the root's checked-out branch launches in the root with the root warning.
- `--root` — launch in the repository's primary checkout from anywhere; the flag consents to the root warning alone, so a root another session holds (or any other warning) is still asked the one question. Exclusive with `--cwd` and `-W`; an error outside a repository.
- `--debug-worktrees` — test helper printing worktrees/branches; not for daily use, one line, moving on.
- Removed subcommands now fail loudly instead of passing the word to the agent as its first argument: `wt models` → error "wt models is removed; use wt config to view models", same shape for `wt agents` (source-verified guard in `cmd/wt-session/main.go`). A word after `--` is a passthrough command and is not refused: `wt -A shell -- models` runs a command named `models`.

### 8. Launch records

Every model launch appends one line to `~/.config/agent-wt/usage.jsonl` (`(*usage.StoreImpl).RecordLaunch()`, which also writes `last-picked.state`). Example record (`tail -1 ~/.config/agent-wt/usage.jsonl` shows your latest; the id will differ):

```json
{"model_id":"ollama/glm-5.3-flash:cloud","agent":"claude","timestamp":"2026-08-29T18:33:05.297099Z"}
```

The picker reads per-model (and per-agent-pair) 1d/7d/30d counts from this file at query time via `Store.Counts` / `CountsForAgent`. A line without an `agent` field counts per model only, and each write drops lines older than 30 days. See `internal/usage`.

### 9. Local-model launch profiles (pi + little-coder)

`internal/profiles` overlays agent-specific tweaks onto any local-model launch — resolved by the same agent × model location computation wt already does. Config: `~/.config/agent-wt/profiles.toml`. **Nothing is enabled out of the box** — the file starts absent, and an absent file means every launch is unprofiled.

**Setup step 1 — install little-coder** (`npm install -g little-coder`). It is the wrapper binary the pi profile runs: pi plus ~27 extensions + 30 skill files tuned for small local models (see `docs/minimal-agent-harnesses/optimizing-coding-agents-for-local-models.md`). It needs **Node ≥ 22.19** (`node --version`; this machine runs node via mise), bundles its own pinned pi (`--no-extensions` + curated set), and works from any directory:

<!-- UNVERIFIED — not installed on the 2026-09-30 rebuild yet; commands from the little-coder README (github.com/itayinbarr/little-coder). -->

```bash
npm install -g little-coder
little-coder --version
command -v little-coder   # must resolve — see below
```

(The `npm warn install-scripts` line naming `@google/genai`/`protobufjs` is expected and inert: little-coder has shipped **no install scripts** since v1.12.0 — its launcher does that work at launch time — and the flagged packages are transitive dependencies. Silence it globally with `npm config set allow-scripts=@google/genai,protobufjs --location=user` if it annoys.)

**The wrapper binary must be on PATH — a missing wrapper is the one *fatal* profile error.** Every other profile problem degrades to an unprofiled launch with a stderr warning; this one aborts the launch.

No `~/.config/little-coder/models.json` override is needed for wt launches: wt's pi driver syncs `~/.pi/agent/models.json` (a wt-owned `litellm` provider block pointing at the model server's gateway — its origin and the client token, never the LiteLLM key) and passes `--model litellm/<gateway id>` (the gateway id is the registry id unless the row names a `gateway_id`); little-coder forwards unrecognized args to pi and does not inject its own default model when one is supplied.

**Setup step 2 — write the profile.** The verified pi snippet from `wt/docs/wt-agents/profiles.md`:

```toml
enabled = true

[[profiles]]
agent = "pi"
match = "location"
location = "local"
wrapper = { binary = "little-coder", args_template = ["{{args}}"] }
env = { LITTLE_CODER_PERMISSION_MODE = "accept-all" }
```

(`wrapper` swaps the launched binary — wt's own `--model …` argv is spliced into `{{args}}`; `env` lands on the wrapper process, so `LITTLE_CODER_PERMISSION_MODE=accept-all` is little-coder's lever — it skips its shell whitelist, a deliberate spec-approved trade-off for local launches. Use `manual` to keep the gate. `--pi-args` is not a little-coder flag — `args_template = ["{{args}}"]` is correct.)

**Inspect/toggle** (`cmd/wt-session/profile.go`):

```bash
wt profile list                       # every defined profile
wt profile show -A pi -M <local-id>   # dry-run resolution, no launch
wt profile status                     # on/off
wt profile on / wt profile off        # global kill switch
```

An interactive launch that resolves a non-empty profile **asks before applying** (default yes on Enter; in the picker's popup `n` launches this once without the profile and `esc` goes back); non-interactive launches apply automatically. `wt smoke` applies profiles too (`cmd/wt-session/smoke.go` resolves per agent × model and uses the same `applyResolvedProfile` as a real launch) — so with this profile on, `wt smoke -A pi -M <local-id>` runs `little-coder`, and the row fails if `little-coder` isn't on PATH.

Full reference — all four agents' mechanisms, file-safety/self-heal rules, claude/codex/opencode working entries: `wt/docs/wt-agents/profiles.md`.

### 10. Slot profiles (Claude Code)

A slot may name an alias (`alias/<name>`, guide 04 step 7) as well as a model. For the profile's one-local-model rule an alias and its own local target count as one model (an alias may name several local models of one provider, which `wt-model alias set` allows unless two share an exclusive family such as mtplx; a start starts each in turn, and the profile counts that set as one local group), and the alias's window (the smallest of its targets') is what claude declares.

A launch names one model, and Claude Code uses several tiers (background work, subagents, a `/model` switch). A single-model launch sends every tier to the one model it names (wt sets `ANTHROPIC_DEFAULT_<SLOT>_MODEL` for all four; see `wt/docs/wt-agents/claude-wt.md`), so none of the four keeps an Anthropic id for the endpoint to reject. A **slot profile** gives the tiers different models: it maps each of Claude Code's four slots (`fable`, `opus`, `sonnet`, `haiku`) to a registry model, in one launch, always through the LiteLLM proxy. It is a different thing from the automatic profiles of §9: you write it, name it and choose it.

**Step 1 — write the file.** `~/.config/agent-wt/slot-profiles.toml`, by hand, or with `wt profile slots add <name> --slot <slot>=<id> …` (see [08 §8](08-maintenance-and-troubleshooting.md)). Take the ids from `wt-model model list`; the ones below are placeholders:

```toml
[[profile]]
name    = "<name>"          # lowercase letters, digits, hyphens
agent   = "claude"
default = "sonnet"          # the slot the session starts on

[profile.slots]             # slots you leave out use the default slot's model
opus   = "<registry id>"
sonnet = "<registry id>"
haiku  = "<registry id>"
```

A profile may mix cloud and local models and providers. It may name one distinct local model.

**Step 2 — check it.**

```bash
wt profile list                                # from: any dir, read-only — the file's own errors and unknown keys
wt profile show -A claude -M profile/<name>    # from: any dir — launch, start or blocked, and what claude would be given
```

`show` starts no model, writes no route and restarts nothing. A blocked profile prints its reason and exits non-zero.

**Step 3 — launch it.** Pick the `profile/<name>` row in the claude model picker (profile rows sit after the native rows), or pin it:

```bash
claude-wt -M profile/<name>
```

- A profile whose local model is not running starts it first, then launches.
- The launch writes any missing routes and restarts the proxy at most once; with every route in place it restarts nothing.
- A profile that cannot be fully applied does not launch. The picker shows the reason; a pin without the picker exits non-zero with it.
- `--settings` and `--model` after `--` are refused: either would replace the slot mapping.
- wt writes no Claude Code settings file for the launch.

Slot profiles are reached from the picker and the pin only. `wt start` and `wt smoke` ignore them, and `-T`/`-F` hide them.

Full reference — every field, every blocked reason, what Claude Code is given: [wt/docs/wt-agents/slot-profiles.md](../../wt/docs/wt-agents/slot-profiles.md).

### 11. Claude subscription models (claude only)

A model on the `claude-subscription` provider runs Claude Code on your own Claude subscription through the model server's gateway (#597), so one session can mix it with other models (subscription `fable` and `opus` slots beside alias `sonnet` and `haiku`, say). It is off by default on both sides:

- the host sets `[gateway] subscription_passthrough = true` in `~/.config/wt-model/config.toml` and restarts serve, and adds the models with `wt-model model add claude-subscription <claude id> --family <family> --context-window <tokens>` ([02-providers-and-models](02-providers-and-models.md) Step 2);
- each session machine that may send its login sets `[model-server] subscription = true` in `~/.config/agent-wt/config.toml`.

See what is offered with `wt model list --server` (the rows show the `claude-subscription` provider and only claude can launch them). wt never picks one by itself: pin it with `-M`, pick it in the picker, or name it in a slot profile. Every such launch first checks the opt-in, an `https://` or loopback url, the server's mode, the launch profile, Claude Code's version and a claude.ai login, and refuses before anything starts when one fails ([wt-session.md](../../wt/docs/wt-session.md#claude-subscription-header-mode)). `wt smoke` runs one only with `wt smoke <id> --subscription`.

- **Your own login only.** Never share or pool a subscription through a model server.
- **Usage credits.** Depending on your plan and seat tier, a Fable row and other large models may need usage credits or count against the plan's limits (Claude Code's `/model` can show "Requires usage credits"). Check your plan before pointing a slot at one.
- A header-mode session's shell carries the wt token (in `ANTHROPIC_CUSTOM_HEADERS`), as a token-mode session's carries it in `ANTHROPIC_AUTH_TOKEN`.

## Verification

Live-ran (2026-08-29, all read-only):

- `wt --version` → `wt 0.1.0`
- `wt --help` → full flags: `-A/-W/-M/-T/-F`, `--yolo`, `--cwd`, `--init`, `--check-guard`, `--no-guard`, `--debug-*` (output quoted throughout this guide). A dated capture: later builds add `--new`, `--root`, `--others` and `--replace`, so read the current list from `wt --help`
- `wt --check-guard` → `wt: main guard is installed in this repo.`
- `wt model list --server` → the model server's catalog as the picker lists it (MODEL, STATE, LOC, TIER, CLIENT SESSIONS), exit 0 (re-run 2026-11-27)
- `wt -w foo` → `Error: -w is removed; use -W or --worktree` (followed by the usage text), exit 1, and `git worktree list` confirmed nothing was created
- `wt config --help` / `wt config theme --help` → subcommands as listed in §6
- Registry models present: `grep -c '^\[\[models\]\]' ~/.config/local-ai/registry.toml` → a non-zero count

Model pin dry explanation (no agent launch required): suppose `ollama/qwen3.8:27b-mlx` (example — substitute any id from your `registry.toml`) is a `[[models]]` id. `-M` matches that exact id; the join in `internal/config/config.go` makes an ollama-provider model eligible for claude/codex/copilot/pi/opencode, so `claude-wt -W my-feature -M <that id>` resolves deterministically and skips the model screen.

<!-- UNVERIFIED — interactive only; I did not launch an agent or drive the TUI during this guide. Specifically: screen-to-screen key feel, `--yolo` argv actually reaching the agent, `theme set` persisting a launch, and a usage.jsonl line appended by *my* launch are code-referenced but not exercised. -->

## Gotchas

- **Cloud models, and every model's family, tags and cost, are changed in `registry.toml`, not here.** The Agents tab of `wt config` never writes providers or models — add, retag or retire models with `wt-model model add|edit|rm`, or on the Models tab of `wt-model model`, which write `registry.toml` and sync the routes ([02-providers-and-models](02-providers-and-models.md) Step 1). Which *local* models are offered is what wt's probes find on disk or running, with or without a registry entry.
- **`agy-wt` and `shell-wt` have no model layer** (no model picker at all); `--yolo` is a no-op for pi.
- **wt never resumes a session, for any agent** — there is no resume/fresh prompt. Continue a conversation with the agent's own flags after `--` (§2, "Sessions are the agent's own"); whether a resumed session's stored model overrides the model you picked is the agent's behaviour, and wt does not guard against it.
- **Tag filtering is latent while models are untagged:** a model with `tags = []` is not narrowed by tag, and a `-T`-less launch lists every model — the header's tag slot shows `code` without meaning a filter is active. Check `grep -c '^tags = \[\]' ~/.config/local-ai/registry.toml` (untagged models) vs `grep -c '^\[\[models\]\]' ~/.config/local-ai/registry.toml` (all models); set tags with `wt-model model edit <id> --tags code` or in the Tags field of the Models tab's form ([03-model-families](03-model-families.md) Step 3).
- **Stale PATH binary:** a `wt` on PATH that predates the registry-consumer merge (2026-08-28) reads its model catalog from `config.toml` and misses registry-only models; `wt --help` lacking a flag this guide names is the quick tell. Rebuild with the Prerequisites command.
- **Three hand-installed `dsh-*` shims sit alongside the wt ones:** `~/.local/bin/` also has `dsh-headless-wt`, `dsh-tui-wt`, and `dsh-webui-wt` — unknown to wt 0.1.0, so launching one errors with `unknown agent "dsh-…"`. Present on disk but not wt-documented.
- **`-W` is the real flag.** The `-w` short form was removed and now errors with `-w is removed; use -W or --worktree`.
- **`last-picked.state` is written by every recorded launch** (a model or a slot profile, `RecordLaunch()`); `esc` and canceled prompts never touch it. One line, a row id (§5).

## Going deeper

- `~/github/ohanaverse/local-ai-setup/wt/README.md` — agents, install, guard, copilot passthrough env vars
- `~/github/ohanaverse/local-ai-setup/wt/docs/configuration.md` and `~/github/ohanaverse/local-ai-setup/wt/docs/wt-config.md` — config.toml and themes.toml reference
- Specs (all verified present): `~/github/ohanaverse/local-ai-setup/wt/docs/superpowers/specs/2026-08-28-wt-registry-consumer-design.md`, `~/github/ohanaverse/local-ai-setup/wt/docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md`, `~/github/ohanaverse/local-ai-setup/wt/docs/superpowers/specs/2026-08-28-native-unification-design.md`
- [00-config-map](00-config-map.md) — who owns which `~/.config` file; [02-providers-and-models](02-providers-and-models.md) — registry content; [03-model-families](03-model-families.md) — families/tags that feed the picker
- Launch-log consumers and spend: [07-usage-and-spend](07-usage-and-spend.md)
