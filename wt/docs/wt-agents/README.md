# wt-agents reference

Per-agent reference docs for the agents launched by `wt` (via the `*-wt` shims in [`bin/`](../../bin/)). Each file documents how the underlying agent is configured on disk and how it authenticates. The `*-wt` files are now thin shims that forward to `wt` (e.g. `claude-wt` → `wt --agent claude`); the launch logic lives in the Go tool (`cmd/wt/`, `internal/agents/`).

## LiteLLM routes are wt-owned

`wt` owns everything LiteLLM-side: the `~/.config/litellm/config.yaml` routes
(`model_list` rows and the settings wt enforces), the proxy restart, and the
routing on/off switch. Every configured cloud and native model is in the
picker (#179; see the root `CLAUDE.md`'s *Routing is derived, not stored*);
local models are listed from live
provider probes — with or without a registry entry (an unregistered model is
routed under its discovered id, `<family>/<artifact>`) — and their routes
are added and removed automatically by
`wt start` / `wt stop` (and the TUI start flow and `wt smoke`).

```bash
wt litellm status                 # routing on/off + url + api_key_set
wt litellm sync [--dry-run]       # route every registry cloud model + every running (or pulled ollama) local model
wt litellm list                   # routed ids in config.yaml (hand-written rows marked)
```

Native models (`claude/native`, `copilot/native`) are always shown and do not
need a route.

## LiteLLM proxy lifecycle

With LiteLLM routing on (`wt litellm on`), `wt` routes non-native models
through the LiteLLM proxy at `:4000`. The proxy loads its model list from
`~/.config/litellm/config.yaml`
**only at startup** — editing that file does not take effect until the proxy is
restarted; until then it serves a stale model list and returns
`400 Invalid model name passed in model=…` for any newly added model.

**The on/off routing switch is wt-owned.** Whether agents route
through the proxy or dial providers directly lives in the `[litellm]` table
(`enabled`/`url`/`api_key`) of wt's own `~/.config/agent-wt/config.toml` and is
toggled with `wt litellm on` / `wt litellm off` (current state:
`wt litellm status`). Toggling is routing policy only — it never starts,
stops, or restarts the proxy; the proxy-process lifecycle below is a separate
concern.

**wt owns reconciliation.** `wt litellm sync` (and the
automatic route updates from `wt start`/`wt stop`) write `config.yaml` and then
restart the proxy after a change. Every one of them then waits up to 30s for
`/health/liveliness` before it is done, when a LiteLLM URL is configured and a
pre-probe before the restart was not refused: `wt litellm sync` and the
commands that run it after a registry write (`wt cloud-sync`, `wt model
add|edit|rm|init`, the Models tab of `wt config`) before they return (#382),
and the automatic route updates from `wt start`/`wt stop`/`wt smoke`/the TUI
start flow and stop picker, and the route check at launch, before wt exits or
hands the proxy to an agent. A proxy that does not answer in time is a
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

`wt litellm restart` makes this restart on its own: the same drain, then the
wait for the proxy to answer again (up to 30s). It exits 1 when the restart
fails or the proxy does not answer, and `bin/update/litellm` runs it after an
upgrade.

The forced command is `launchctl kickstart -k
gui/$(id -u)/local.litellm.proxy`: launchd sends SIGTERM and kills the proxy 5
seconds later, cutting off any request still in flight. It is the manual
restart for a proxy that is stuck, and what `bin/update/litellm` falls back to
with a wt too old to have `wt litellm restart`. A command set in
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
manually. `wt litellm restart` lets a request in flight finish first; the
forced restart cuts it off after 5 seconds, and is for a proxy that does not
exit on its own:

```bash
wt litellm restart
launchctl kickstart -k gui/$(id -u)/local.litellm.proxy   # forced
```

This affects every agent routed through the proxy (claude, codex, copilot,
opencode, pi), not just one launcher — and the failure modes differ per driver: model-id
grammar mismatches, strict `ollama_chat` param mapping, and litellm bridge
bugs all surface as *agent-side* errors ("Invalid model name", "high demand",
"Model not found", …). Per-driver causes, fixes, and a debugging playbook
live in [litellm-troubleshooting.md](litellm-troubleshooting.md).

## Scope

These docs cover the agents launched by `claude-wt`, `codex-wt`, `copilot-wt`, `pi-wt`, `agy-wt`, `opencode-wt`, and `shell-wt`. The launcher contract (flags, rotation, install) lives in the Go tool — see the root [`CLAUDE.md`](../../CLAUDE.md). These per-agent docs add per-agent context (config files, auth, model selection) that does not fit there.

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
| `-W <name>`, `--worktree <name>` | Use or create a worktree for the given branch name. For branches with slashes (e.g., `feature/my-branch`, `origin/feature`), the last path component is used as the worktree directory name (`.worktrees/my-branch`, `.worktrees/feature`). Remote tracking branches are checked out as new local branches. Skips the worktree picker. The agent+command picker still appears when `-A` is omitted. With `-A` and no `-M`, wt launches directly when exactly one model is launchable; otherwise the model picker appears. |
| `-A <name>`, `--agent <name>` | Pin the agent (`claude`, `codex`, `copilot`, `pi`, `agy`, `opencode`) or command (`shell`) to launch. The agent is never defaulted: when `-A` is omitted the agent+command picker is always shown. Supplying `-A` skips the picker. |
| `-M <id>`, `--model <id>` | Pin the model as `<provider>/<name>` (e.g. `claude/opus`, `ollama/gemma4:9b`). A pinned local model that is not running is started first. Errors if the model is not in the eligible list or its row is blocked. Skips the model picker. Without `-A`, the agent+command picker is shown first, then the pin is validated against the chosen agent. `-M profile/<name>` pins a slot profile instead of a model ([slot-profiles.md](slot-profiles.md)). |
| `--replace` | With `-M`, start the model even if it means stopping a running one. |
| `-T <tags>`, `--tags <tags>` | Filter the model list by tag (comma-delimited, OR within flag). |
| `-F <family>`, `--family <family>` | Filter the model list by model family (comma-delimited, OR within flag). |
| `--cwd` | Launch in the current directory, inside a git repository or not; skip the worktree picker. The agent+command picker still appears when `-A` is omitted. With `-A` and no `-M`, wt launches directly when exactly one model is launchable; otherwise the model picker appears. It takes no value: with `--cwd`, a first argument before `--` that names an existing directory (`claude-wt --cwd ~/proj`) is refused, since it would reach the agent as its first argument (claude's prompt) while the launch ran in the current directory. `cd` into the directory first, or pass the path after `--`. A `shell` command's bare name found on `$PATH` is not refused (`shell-wt --cwd wt model list` beside a `wt/` directory). |
| `--yolo` | Skip permission prompts (agent-specific). |
| `--init` | Seed agent instruction files (AGENTS.md + agent-specific pointer if applicable) and exit. |

With no flags, `wt` presents the worktree picker, then the agent+command
picker, then the model picker (for agents). Multiple eligible models
rotate on successive launches via a single global rotation state file. Each picker is
skipped only when its selection is already resolved: `-W`/`--cwd` skip the
worktree picker, `-A` skips the agent+command picker, and `-M` skips the
model picker. With `-W` or `--cwd` and `-A` but no `-M`, wt launches
directly when exactly one model is launchable for the agent (a cloud model,
or a local model already running), whatever stopped or blocked models the
agent also has. So `-W foo -A pi` (no `-M`) shows the model picker only when
several models, or none, are launchable. When the worktree picker or the
agent+command picker was shown, the model picker is skipped only when its
table holds a single row and that row is launchable: one launchable model
beside a stopped local model or a blocked row still shows the picker.
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
has no repository, so it shows a PR opened from a branch of its name in
this repository or in a repository one of your remotes points at (your
fork, when it is a remote), never a stranger's fork: someone's PR from
their fork's `main` does not tag your `main`. Every remote counts as
yours, a collaborator's fork you added as a remote to review their work
included, so that fork's PR from its `main` does tag your `main`. When two of those
repositories both have one (upstream's `feat-x` and your fork's), the open
one wins, else the higher number. The host in a
remote's URL is ignored, so an Enterprise host with the same owner/repo
counts as that repository; a remote that is a local path or a `file://`
URL, or one wt cannot find, is never tagged.
Without `gh`, outside a GitHub repository, or with `gh` not logged in, the
picker simply shows no tags.

The agent+command picker lists every registered agent and command. An
agent with no binary on PATH is marked "not installed", and selecting it is
blocked with a clear error rather than advancing to a model screen that can
never succeed. An installed agent with no `config.toml` entry is marked
"not configured — launches directly with no model" and launches bare,
without the model layer; it is refused only when `-M` is also given.

### Arguments after `--`

Everything after `--` is handed to the agent unchanged
(`claude-wt -- --verbose`, `opencode-wt -- /path/to/project`).

**A relative path there is resolved by the agent, from the directory the
agent starts in**, which is not always the one you typed the command in:

| How you launch | Where the agent starts |
|---|---|
| `--cwd` (inside a git repo or not) | the directory you typed the command in |
| `-W <name>`, or the worktree picker | the root of that worktree |
| outside a git repo, no `--cwd` | the directory you typed the command in |

**What needs a git repository.** Only choosing or creating a worktree does:
`-W <name>` outside a repository stops with `wt: not in a git repo: …`, and
the worktree picker is never shown there. `--cwd` needs none. Outside a
repository it is the same launch as inside one (agent and model pickers,
`-M` pins and slot profiles, LiteLLM routes, automatic profiles, rotation and
usage records, the stop picker and summary afterwards) with one difference:
the [main guard](../../README.md#main-guard) is a git hook, so it is installed
only when the directory is inside a checkout. Nothing wt records about a
launch is keyed by a repository or a branch (the one directory-keyed item,
claude's `config_content` backup, uses the absolute settings path, which
cannot collide), so a launch outside
a repository shares no state with one inside it beyond what every launch
shares (which model was launched last, how often, and which are in use).
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

> release this session's refcount entry → survey → stop picker → summary
> line → after-survey stats → pricing notice

Both the survey and the stop picker therefore run **before** the summary
line.

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
  is `false` since 2026-10-05, #136), so a session goes straight to the stop
  picker and there is no after-survey block. The code is kept and the survey
  may return; when on, it behaves as follows. Prompts up to four questions on the parent terminal (did it
  work? speed 1-5? quality 1-5? — and on non-skip answers, what task were
  you doing), each answerable with Enter to skip. It silently does nothing
  when stdin is not a TTY, when the launch had no model (command agents
  like `shell`), or when the model is **native** — a native launch has no
  priced model to survey (issue #116).
- **Stop picker** (`survey.Picker`, issue #115). Offers to stop running
  local models that no live wt session is using (refcount zero, probe
  trusted, stop backend exists). One line, applied on Enter: space-separated
  numbers stop those models, `a` or `all` stops every listed one, and a
  bare Enter (or `q`/`esc`) stops nothing. A line it cannot read stops
  nothing and asks again. Anything left running can be stopped later with
  `wt stop`. It is silent
  when nothing qualifies, when stdin is not a TTY, for command agents, and
  after a native-model session (a native launch runs no local server of its
  own).
  wt releases its own refcount entry first, or the model this session just
  used would always count as in use.
  Its stops run on a context of their own, cancelled by Ctrl+C or SIGTERM:
  the stop in flight is abandoned, the remaining models are skipped, and wt
  still prints its summary. Nothing above the picker is watching for those
  signals any more — the agent has already exited — so this is the only
  Ctrl+C handling left on the path.
- **After a slot-profile session** the order is shorter: release the
  session's refcount entries (one per slot model) → stop picker → summary
  line. The summary names the profile (`wt: claude · profile/<name> · <duration>`).
  There is no survey and no pricing notice, since a profile is not one
  priced model. The stop picker is the same one: it offers every running
  local model no live session uses, not only the profile's.

See `docs/wt-stats.md` for how the collected survey data is reported.

### Legacy bash flags

The original bash launchers supported `-w`/`--worktree`, `--code`,
`--design`, and `--native`. The short flag `-w` has been removed in favor
of `-W`; the others are not supported by `wt`. The `--no-guard` and
`--check-guard` flags ARE supported by `wt` (see
[shell-wt.md](./shell-wt.md#key-flags)) — they were bash-only originally
and now work via the Go binary. Model rotation is now a single global
sequence (implicit on launch in the TUI, or `wt rotate <tag>` for
debugging) — the earlier per-slot `(agent, tag, family)` scheme was
retired; the main guard is managed by `internal/guard`. The bash
model-rotation and pi `models.json` auto-sync behavior described in
earlier versions of this doc has been ported to Go.
