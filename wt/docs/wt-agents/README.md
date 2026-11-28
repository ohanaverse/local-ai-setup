# wt-agents reference

Per-agent reference docs for the agents launched by `wt` (via the `*-wt` shims in [`bin/`](../../bin/)). Each file documents how the underlying agent is configured on disk and how it authenticates. The `*-wt` files are now thin shims that forward to `wt` (e.g. `claude-wt` → `wt --agent claude`); the launch logic lives in the Go tool (`cmd/wt-session/`, `internal/agents/`).

## LiteLLM routes are wt-owned

`wt` owns everything LiteLLM-side: the `~/.config/litellm/config.yaml` routes
(`model_list` rows and the settings wt enforces) and the proxy restart. A
session never dials LiteLLM or a provider itself: every non-native model an
agent uses goes through the model server's gateway (`wt-model serve`,
[wt-session.md](../wt-session.md)), which forwards to LiteLLM with the
host's LiteLLM key (the `~/.config/wt-model/` home's key file; the
`[litellm]` key of wt's `config.toml` is its read-only fallback). There are
no direct routes and no routing switch. Every configured cloud and native model is in the
picker (#179; see the root `CLAUDE.md`'s *Routing is derived, not stored*);
local models are listed from live
provider probes — with or without a registry entry (an unregistered model is
routed under its discovered id, `<family>/<artifact>`) — and their routes
are added and removed automatically by
`wt start` / `wt stop` (and the TUI start flow and `wt smoke`).

```bash
wt-model litellm set --url URL --api-key KEY   # the proxy pair the gateway forwards with (the ~/.config/wt-model/ home)
wt litellm status                              # url + api_key_set
wt-model litellm sync [--dry-run]              # route every registry cloud model + every running (or pulled ollama) local model
wt-model litellm list                          # routed ids: STORE file|db, OWNER wt|hand-written
```

Native models (`claude/native`, `copilot/native`) are always shown and do not
need a route.

## LiteLLM proxy lifecycle

Every session reaches its non-native models through the model server's
gateway, which forwards to the LiteLLM proxy at `:4000`. The proxy loads its model list from
`~/.config/litellm/config.yaml`
**only at startup** — editing that file does not take effect until the proxy is
restarted; until then it serves a stale model list and returns
`400 Invalid model name passed in model=…` for any newly added model.

**There is no routing switch.** The proxy's `url` and `api_key` live in the
`wt-model` config home — `~/.config/wt-model/config.toml` and the 0600 file
its `key_file` names — set with `wt-model litellm set --url URL --api-key KEY`
and shown by `wt-model litellm status`, which `wt-model serve` reads; a
session reads neither. The `[litellm]` table of wt's own
`~/.config/agent-wt/config.toml` is a read-only fallback (used only when the
home holds no `[litellm]` table, never written). Its `enabled` key is retired (2026-11-15):
still parsed and kept on save, read by nothing, and a true value prints one
note per process. `wt litellm on` / `wt litellm off` are hidden stubs that
exit 1.

**wt owns reconciliation.** `wt-model litellm sync` (and the
automatic route updates from `wt start`/`wt stop`) write `config.yaml` and then
restart the proxy after a change. Every one of them then waits up to 30s for
`/health/liveliness` before it is done, when a LiteLLM URL is configured and a
pre-probe before the restart was not refused: `wt-model litellm sync` and the
commands that run it after a registry write (`wt-model cloud-sync`, `wt-model model
add|edit|rm|init`, the Models tab of `wt-model model`) before they return (#382),
and the automatic route updates from the host's `wt-model stop <provider>`/`--all`
and the model server's start and stop jobs, before wt exits or hands the
proxy to a client. A proxy that does not answer in time is a
warning, never a failed command: the routes were written.

**The restart lets a request in flight finish.** With no restart command
configured, wt asks launchd for the proxy's pid, sends it one SIGTERM, and
waits for it to exit, for up to three minutes. The proxy stops accepting
connections at once and exits when its open responses have ended; launchd
(`KeepAlive`) then starts it again, and wt runs a plain `launchctl kickstart
gui/$(id -u)/local.litellm.proxy` in case it did not. What this costs and
what it prints:

- An idle proxy exits in a fraction of a second and wt prints nothing new.
- While the proxy drains, every new request to it is refused (connection
  refused), in every session, until the slowest request in flight has ended
  and the proxy has booted again. Every wt command that restarts the proxy
  blocks for that long too.
- After 2 seconds of waiting wt prints `wt: the LiteLLM proxy is finishing a
  request in flight; it restarts when the request ends (up to 3m0s)`.
- A proxy still serving after three minutes is killed (SIGKILL) and started
  again, and wt prints `the LiteLLM proxy was still serving a request after
  3m0s; it was restarted anyway and that request was cut off`.
- When launchd reports no pid for the job (it is not running, or there is no
  `launchctl`), wt runs the forced command directly.

`wt-model litellm restart` makes this restart on its own: the same drain, then the
wait for the proxy to answer again (up to 30s). It exits 1 when the restart
fails or the proxy does not answer, and `bin/update/litellm` runs it after an
upgrade.

The forced command is `launchctl kickstart -k
gui/$(id -u)/local.litellm.proxy`: launchd sends SIGTERM and kills the proxy 5
seconds later, cutting off any request still in flight. It is the manual
restart for a proxy that is stuck, and what `bin/update/litellm` falls back to
with no wt-model on PATH that has `litellm restart`. A command set in
`WT_LITELLM_RESTART_CMD` (legacy alias `MODELMAN_LITELLM_RESTART_CMD`) replaces
all of the above: wt runs it as given, with a 30s limit, and neither looks up
nor signals the proxy.

A model id that two registry rows carry has one route at most: sync builds it
from the row of the provider that is serving the model, and when more than
one of the rows is to be routed — or a cloud row shares the id with a local
row whose provider's probe did not succeed — it leaves the id's route as it is and warns
(`model "<id>" is in the registry twice (providers A, B); its route is left
as it is — fix the entry in <registry path>`).
If a model was added by hand or the restart failed, restart the proxy
manually. `wt-model litellm restart` lets a request in flight finish first; the
forced restart cuts it off after 5 seconds, and is for a proxy that does not
exit on its own:

```bash
wt-model litellm restart
launchctl kickstart -k gui/$(id -u)/local.litellm.proxy   # forced
```

This affects every agent routed through the proxy (claude, codex, copilot,
opencode, pi), not just one launcher — and the failure modes differ per driver: model-id
grammar mismatches, strict `ollama_chat` param mapping, and litellm bridge
bugs all surface as *agent-side* errors ("Invalid model name", "high demand",
"Model not found", …). Per-driver causes, fixes, and a debugging playbook
live in [litellm-troubleshooting.md](litellm-troubleshooting.md).

## Scope

These docs cover the agents launched by `claude-wt`, `codex-wt`, `copilot-wt`, `pi-wt`, `agy-wt`, `opencode-wt`, and `shell-wt`. The launcher contract (flags, model picking, install) lives in the Go tool — see the root [`CLAUDE.md`](../../CLAUDE.md). These per-agent docs add per-agent context (config files, auth, model selection) that does not fit there.

LiteLLM-routed launches have their own failure modes (model-id grammar
mismatches, litellm bridge bugs) — see [litellm-troubleshooting.md](litellm-troubleshooting.md) for the driver matrix, known litellm issues, and the debugging playbook.

## Agents

Quick reference (homepage, GitHub, install): [supported-agents.md](supported-agents.md)

| Agent | File | Verification status |
|---|---|---|
| Claude Code | [claude-wt.md](claude-wt.md) | Verified on this machine, 2026-06-01 |
| OpenAI Codex CLI | [codex-wt.md](codex-wt.md) | Convention only (used on other machines) |
| GitHub Copilot CLI | [copilot-wt.md](copilot-wt.md) | Convention only (not installed on this machine) |
| pi-coding-agent | [pi-wt.md](pi-wt.md) | Verified on this machine, 2026-06-01 — includes NYT LiteLLM worked example |
| Antigravity CLI | [agy-wt.md](agy-wt.md) | Convention only (not installed on this machine) |
| OpenCode | [opencode-wt.md](opencode-wt.md) | Verified on this machine, 2026-09-02 |
| Shell command | [shell-wt.md](shell-wt.md) | Verified on this machine, 2026-07-22 |

Claude Code can also be launched with a **slot profile**, which maps each of
its model slots to a registry model in one launch: [slot-profiles.md](slot-profiles.md).
Automatic launch profiles, which adjust a single-model launch, are in
[profiles.md](profiles.md).

## Verification convention

Each per-agent file ends with a "Verified on this machine" section. Verified files carry a `Verified on this machine, YYYY-MM-DD` stamp inside that section; convention-only files state non-verification explicitly. Re-verifying a file is a single-file edit, not a rename.

## Common launcher flags

The `wt` tool (and therefore every `*-wt` shim) needs three inputs to launch:
a directory, an agent or command, and (for agents) a model. Each can be
supplied via flag or picked from a TUI screen; only the agent is never
defaulted (it always comes from `-A` or the agent+command picker).

| Flag | Description |
|------|-------------|
| `-W <name>`, `--worktree <name>` | Use or create a worktree for the given branch name. An existing worktree is used with no question of its own (one `launching in …` line when nothing warns). A name that creates anything — a new branch and worktree, a worktree for an existing local branch with none, or a local branch tracking a remote-tracking name — is announced first by its creation line (`creating branch <b> and worktree <p>` for a new branch, `creating worktree <p> for branch <b>` for a worktree on an existing branch, or `creating worktree <p> for branch <b> tracking <r>` for a local branch tracking a remote-tracking name; the base is not announced); on a terminal, a launch warning (the root itself, a drifted root, a session holding it, a merged PR for the branch) also asks one default-yes `continue?` question before anything is created, and answering no ends the launch with exit 3 (declined) and creates nothing. With no terminal the warnings print and the launch proceeds. `-W <the root's branch>` (the branch checked out in the primary checkout) resolves to the root and launches there with the root warning. For branches with slashes (e.g., `feature/my-branch`), the directory name is the full branch name flattened — path-hostile characters like `/` become `-` (`.worktrees/feature-my-branch`) — while a remote tracking name uses its local branch name (`origin/feature` → `feature` → `.worktrees/feature`; `origin/feature/x` → `feature/x` → `.worktrees/feature-x`). Remote tracking branches are checked out as new local branches. A name whose directory another branch already holds errors `already in use by branch …`, and a flattened name over 255 bytes errors with a request to shorten the branch. Skips the worktree picker. The agent+command picker still appears when `-A` is omitted. With `-A` and no `-M`, wt launches directly when exactly one model is launchable; otherwise the model picker appears. |
| `-A <name>`, `--agent <name>` | Pin the agent (`claude`, `codex`, `copilot`, `pi`, `agy`, `opencode`) or command (`shell`) to launch. The agent is never defaulted: when `-A` is omitted the agent+command picker is always shown. Supplying `-A` skips the picker. |
| `-M <id>`, `--model <id>` | Pin the model as `<provider>/<name>` (e.g. `claude/opus`, `ollama/gemma4:9b`). A pinned local model that is not running is started first. Errors if the model is not in the eligible list or its row is blocked. Skips the model picker. Without `-A`, the agent+command picker is shown first, then the pin is validated against the chosen agent. `-M profile/<name>` pins a slot profile instead of a model ([slot-profiles.md](slot-profiles.md)). |
| `--replace` | With `-M`, start the model even if it means stopping a running one. |
| `-T <tags>`, `--tags <tags>` | Filter the model list by tag (comma-delimited, OR within flag). |
| `-F <family>`, `--family <family>` | Filter the model list by model family (comma-delimited, OR within flag). |
| `--cwd` | Launch in the current directory, inside a git repository or not; skip the worktree picker. Standing in the repository root it warns and proceeds (a terminal with any warning is asked one default-yes question first; a no ends the run with exit 3). The agent+command picker still appears when `-A` is omitted. With `-A` and no `-M`, wt launches directly when exactly one model is launchable; otherwise the model picker appears. It takes no value: with `--cwd`, a first argument before `--` that names an existing directory (`claude-wt --cwd ~/proj`) is refused, since it would reach the agent as its first argument (claude's prompt) while the launch ran in the current directory. `cd` into the directory first, or pass the path after `--`. A `shell` command's bare name found on `$PATH` is not refused (`shell-wt --cwd wt worktrees --plain` beside a `wt/` directory). |
| `--new` | Accepted and ignored (kept for old scripts): a name that does not exist yet is created and said so either way. A no-op for an existing name; a usage error without `-W` or with `--cwd`/`--root`. |
| `--root` | Launch in the repository's primary checkout, from anywhere (also from a linked worktree or subdirectory); skips the worktree picker and accepts the root warning, though any other warning still asks. Exclusive with `--cwd` and `-W`. |
| `--others keep\|stop` | When a local start loads a model beside other providers' models: keep them, or stop the ones wt can stop (default: ask; when stdin is not a terminal keep and warn). Read by `wt start`, `wt smoke` and a `-M` launch. |
| `--yolo` | Skip permission prompts (agent-specific). |
| `--init` | Seed agent instruction files (AGENTS.md + agent-specific pointer if applicable) and exit. |

With no flags, `wt` presents the worktree picker, then the agent+command
picker, then the model picker (for agents). The model picker's
cursor opens on the `-M` pin, else on the row the last launch picked (the row the `> `
marker shows) when this table shows it and it is not blocked, else on the first row
that is not blocked. Each picker is
skipped only when its selection is already resolved: `-W`/`--cwd` skip the
worktree picker, `-A` skips the agent+command picker, and `-M` skips the
model picker. With `-W` or `--cwd` and `-A` but no `-M`, wt launches
directly when exactly one model is launchable for the agent (a cloud model,
or a local model already running), whatever stopped or blocked models the
agent also has. So `-W foo -A pi` (no `-M`) shows the model picker only when
several models, or none, are launchable. When the worktree picker or the
agent+command picker was shown, the model picker is always shown, even for a
single launchable row.
`-W foo -M claude/opus` (no `-A`) shows the agent+command picker first and
then validates the pin against the chosen agent.

The worktree picker tags each worktree or branch that has a pull request on
GitHub with its number and state after the branch name — `#383 open`, or
`draft`, `merged`, `closed` — and puts the PR's title on the row's second line,
before the path. Typing the number (`/383`) in the filter finds the row. The
tags come from two `gh pr list` calls per picker open — open PRs, and PRs
of every state, so an old open PR is never crowded out by newer merged ones —
and appear as each answers (within five seconds), the open PRs' usually
first; the picker does not wait for them. Each PR belongs to the
repository and branch it was opened from, a fork's included, and a branch
with several PRs shows its open one, else the highest-numbered. A remote
branch shows the PR opened from that branch of its own remote's
repository: with `origin` your fork and `upstream` the repository `gh`
lists, `origin/feat-x` shows the PR you opened from your fork's `feat-x`,
`upstream/feat-y` shows upstream's own PR from `feat-y`, and
`upstream/feat-x` shows nothing (#387, #388). A local branch or worktree
shows the PR opened from its own name in the repository it pushes to
(its `pushRemote`) or, without one, tracks (its upstream's remote), and
nothing from any other remote: upstream's later PR from a same-named
branch, or a collaborator's fork you added as a remote to review their
work, does not tag it. A branch that neither pushes nor tracks anywhere
shows a PR opened from a branch of its name in this repository or in a
repository one of your remotes points at, never a stranger's fork; if two
of those repositories both have one, the open one wins, else the higher
number. The host in a
remote's URL is ignored, so an Enterprise host with the same owner/repo
counts as that repository; a remote that is a local path or a `file://`
URL, or one wt cannot find, is never tagged.
Without `gh`, outside a GitHub repository, or with `gh` not logged in, the
picker simply shows no tags.

The agent+command picker lists every registered agent and command. An
agent with no binary on PATH is marked "not installed", and selecting it is
blocked with a clear error rather than advancing to a model screen that can
never succeed. Every model-driven agent goes to the model picker, configured
in `config.toml` or not (its models come from the model server); the old bare
launch of an unconfigured agent is gone.

### Arguments after `--`

Everything after `--` is handed to the agent unchanged
(`claude-wt -- --verbose`, `opencode-wt -- /path/to/project`).

**A relative path there is resolved by the agent, from the directory the
agent starts in**, which is not always the one you typed the command in:

| How you launch | Where the agent starts |
|---|---|
| `--cwd` (inside a git repo or not) | the directory you typed the command in |
| `-W <name>`, or the worktree picker | the root of that worktree |
| `--root` | the repository's primary checkout, wherever you typed the command |
| outside a git repo, no `--cwd` | the directory you typed the command in |

**What needs a git repository.** Only choosing or creating a worktree does:
`-W <name>` outside a repository stops with `wt: not in a git repo: …`, and
the worktree picker is never shown there. `--cwd` needs none. Outside a
repository it is the same launch as inside one (agent and model pickers,
`-M` pins and slot profiles, the model server's gateway, automatic profiles,
the launch record (`last-picked.state` and `usage.jsonl`), the summary afterwards) with one difference:
the [main guard](../../README.md#main-guard) is a git hook, so it is installed
only when the directory is inside a checkout. Nothing wt records about a
launch is keyed by a repository or a branch (the one directory-keyed item,
claude's `config_content` backup, uses the absolute settings path, which
cannot collide), so a launch outside
a repository shares no state with one inside it beyond what every launch
shares (which model was launched last, and how often).
Without `--cwd`, wt outside a repository already launches in the directory
you typed the command in; `--cwd` there says the same thing explicitly, and
a script can pass it without knowing where it runs.

So with `--cwd` a relative path means what you meant. With `-W` or the picker
it is read from the worktree's root: run from `<repo>/wt` and pick the main
checkout,

```
opencode-wt -- ../../other-project
```

reaches opencode in `<repo>`, where `../../other-project` names a different
place, and fails with `Failed to change directory`. Pass an absolute path, or
check where you ended up.

`wt` never resumes a session; that is the agent's to do, with its own flags
after `--` (`claude-wt -- --continue`, `opencode-wt -- --continue`). The agent
looks for sessions in the directory it starts in, so the table above also
says which directory's conversations a `--continue` finds.

`wt` does not rewrite the argument: it cannot tell a path from any other word,
and a prompt may contain something that only looks like one. When an argument
names something that exists from your directory but is missing, or is a
different file, from the agent's, it prints a note with the absolute path to
use:

```
wt: note: "../../other-project" is a relative path. opencode starts in <repo>, where it does not exist. From <repo>/wt it is <parent>/other-project; pass that absolute path.
```

Launched from the picker, the note is printed when the agent takes the
terminal, above the agent's own output. Flags (anything starting with `-`,
including `--file=../x`), absolute paths, words that name nothing from your
directory, and the bare program name `shell-wt` runs (`make` in
`shell-wt -- make docs`) are never flagged. Neither is a path that names the
same place in the worktree you launched: `shell-wt -W feat -- cat README.md`
from the repo root reads `.worktrees/feat/README.md`, the checkout you asked
for, so there is no note — unless the file is missing there (untracked, or not
on that branch). Paths in the note have their symbolic links resolved.

## Post-run summary line

After the launched subprocess exits, `wt` prints a single summary line to
**stdout** so the user can see what just ran and how long it took. The
summary fires on both success and non-zero exit; it is informational and
never changes the exit code. Both the TUI and non-TUI launch paths emit it
through a shared `agents.Summary` formatter so the two paths can't drift.

```
wt: <agent> · <model-id> · <duration>
```

- `<agent>` — the `-A` value (`claude`, `codex`, `copilot`, `pi`, `agy`,
  `opencode`, `shell`, …).
- `<model-id>` — the model's full ID (e.g. `claude/sonnet`,
  `ollama/gemma4:9b`). Omitted for command agents (`shell`), which have no
  model layer.
- `<duration>` — subprocess wall-clock time, rounded to seconds when ≥1s
  (e.g. `3m42s`, `850ms` for sub-second runs).

Examples:

```
wt: claude · claude/sonnet · 3m42s
wt: shell · 1s
```

On the TUI path the summary prints **after** the alt-screen is restored,
so the line lands on a clean line in the parent terminal rather than inside
the Bubble Tea frame.

### Post-exit order

The post-run steps run in a fixed order — identical in the TUI and non-TUI
paths, with the user's interactive steps first and the informational output
last so the prompts cannot scroll it away:

> end the client session → survey → summary line → after-survey stats →
> pricing notice

The survey therefore runs **before** the summary line. There is no stop
picker: the model server stops a model no client session holds once its
idle grace has passed (`wt stop` stops one by hand). A slot-profile session runs the same order without the
survey, the after-survey stats and the pricing notice.

**Exit status.** Once these steps have run, wt exits with the agent's own
exit status — 3 when the agent exited 3, 255 when a signal killed it — on
both paths, a launch from the picker included (#412), and prints nothing
more about it. An agent that exited 0 leaves wt at 0. An agent binary that
could not be started is printed after `wt:` and wt exits 1, on both paths.
A launch that never starts the agent because a profile could not be applied
takes the same path in the picker as outside it, and differs only by profile
kind. A **single-model** launch prints `wt: <error>` once (from `main`, not
from the launch goroutine) and exits 1, exactly as the non-TUI launch does. A
**slot profile** prints `wt: <error>` and returns to the picker with
`launch failed: <error>` on its status line — wt then exits with whatever the
user does next (0 on quitting), since no agent status exists to report.

- **Survey — currently switched off.** wt does not ask it (`survey.Enabled`
  is `false` since 2026-10-05, #136), so a session goes straight to the
  summary and there is no after-survey block. The code is kept and the survey
  may return; when on, it behaves as follows. Prompts up to four questions on the parent terminal (did it
  work? speed 1-5? quality 1-5? — and on non-skip answers, what task were
  you doing), each answerable with Enter to skip. It silently does nothing
  when stdin is not a TTY, when the launch had no model (command agents
  like `shell`), or when the model is **native** — a native launch has no
  priced model to survey (issue #116).
- **No worktree prompt** (`exitprompt.Offer`, #521, **deleted**). Nothing is
  asked when the agent exits and no untouched session worktree is offered for
  removal — cleanup is user-driven (`wt worktrees`, `wt remove`). A
  `[launch] exit_prompt` key in `~/.config/agent-wt/config.toml` is tolerated
  and ignored: no value is validated or consulted and any value survives wt's
  saves; a non-empty value prints one note per process: `wt: note: config.toml:
  [launch] exit_prompt is no longer used and is ignored`.
- **The client session ends first.** wt deletes this session's registration
  on the model server (bounded at 3 s, on a context no Ctrl-C reaches), so
  the server's idle scan sees the model free. A failure is a `wt: note:`; the
  server forgets an unrenewed session within a minute anyway.
- **No stop picker.** The exit-flow stop picker (`survey.Picker`, #115) and
  the local refcount store it read were removed with the model server: a
  session has no local model of its own to stop. `wt stop` with no argument
  is a numbered picker over the model server's running local models.
- **Pricing notice.** From the model server's catalog `pricing`, aged on the
  server's clock: printed when the registry's OpenRouter prices are more than
  7 days old (or never refreshed), and only when the catalog has an
  OpenRouter-priced model. A failed fetch prints nothing.
- **After a slot-profile session** the order is shorter: end the client
  session → summary line. The summary names the profile (`wt: claude · profile/<name> · <duration>`).
  There is no survey and no pricing notice, since a profile is not one
  priced model.

See `docs/wt-stats.md` for how the collected survey data is reported.

### Legacy bash flags

The original bash launchers supported `-w`/`--worktree`, `--code`,
`--design`, and `--native`. The short flag `-w` has been removed in favor
of `-W`; the others are not supported by `wt`. The `--no-guard` and
`--check-guard` flags ARE supported by `wt` (see
[shell-wt.md](./shell-wt.md#supported-flags)) — they were bash-only originally
and now work via the Go binary. Model rotation was retired: the
picker's cursor opens on the row the last launch picked when it is shown and not
blocked, else on the first row that is not blocked, and the debug
subcommand is gone. The earlier per-slot `(agent, tag, family)` scheme was
retired before that; the main guard is managed by `internal/guard`. The pi
`models.json` auto-sync behavior described in earlier versions of this doc has
been ported to Go.
