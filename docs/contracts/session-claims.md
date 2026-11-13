# Session claims

A claim records that a `wt` session runs in a checkout of a repository — the root or a linked worktree, the worktree that launch's agent runs in. `wt` (Go, `wt/internal/sessionclaim`) writes and removes claims; `repo-cleanup` (bash 3.2, in the repo-cleanup-skill repository) and `wt cleanup` (which marks a row held, and reports `claims_unavailable` when the claims cannot be read) only read them. Neither side may depend on the other being installed, so this file format is the whole contract.

A claim is one file per live `wt` process, in the repository's common git directory:

```
<git-common-dir>/wt-sessions/<pid>
```

Contents: plain `key=value` lines, one per line, no quoting:

```
pid=<wt process id>
started=<process start time, seconds since epoch>
path=<absolute path of the worktree top level the session runs in>
branch=<the branch that worktree checked out when the session started>
agent=<agent name, e.g. claude>
```

`branch` spells a detached HEAD in two ways, by writer. A launch claim (what every launch writes through `wt`'s `HoldLaunch`) writes the key with an **empty value** — a line `branch=` — because a real branch may be named `(detached)` and a launch claim must not say so falsely. The root-claim flavor (`wt`'s `holdRoot`, a root launch) writes the literal `(detached)`. A reader treats an empty value as detached, and a non-empty one as a branch name that may be `(detached)` in this older flavor. Both spellings render as `started on (detached)` in `Describe` output (the held warning/label text — including `wt restore-root`'s root-held warning — and `--json` `reasons[].detail`).

Rules:

- `wt` writes the claim after the check passes (or the person chose continue) and before it starts the agent. It writes to a temp name and renames, so a reader never sees half a file. Temp names are not all digits; readers consider only files named by a pid. Every launch claims the worktree the agent runs in — the root or a linked one — and a root launch writes the root-claim flavor and the launch flavor into the one claim file of its pid, twice-safe.
- `wt` removes its claim when it exits. A killed `wt` leaves a stale claim; readers must tolerate that, and never delete it (a reader that tidied up could delete a claim a concurrent writer just wrote).
- A claim is **live** when `pid` is running **and** that process's start time equals `started`. This guards against a reused pid. A claim that fails this test is stale: readers ignore it. `wt` deletes stale claims in exactly two places — the locked prune its claim writers (`Hold` and `Join`) run before writing a new claim, and `sessionclaim.Prune`, which `wt worktrees` calls on the list's `F` refresh and on a fetch-first `--json` run (not on the list's open, not with `--no-fetch`, not on `--plain`). A running pid whose start time a reader cannot read is neither counted nor deleted.
- A claim counts as holding the checkout `path` names — the root or a linked worktree. `wt` writes a claim for every launch, so `path` also names a linked worktree's top level; a reader that cares about the root compares `path` to the root path.
- `path` is the physical path of the worktree top level: symlinks resolved (so `/var` and `/private/var` agree on macOS), no trailing slash. Both sides derive it from the worktree's top level, not from the current directory (a launch in a subdirectory claims the top level's path). `wt` reads it from git — `rev-parse --path-format=absolute --show-toplevel` for a launch claim, the primary checkout's top level with symlinks resolved for a root claim — and `repo-cleanup` uses `pwd -P`.
- `pid` is the `wt` process. `wt` runs the agent as a child and stays alive until it exits, so the claim lasts as long as `wt` does. That is the session's lifetime unless `wt` itself is killed (SIGTERM, SIGKILL) while the agent still runs: then the claim's pid is gone and the checkout looks free.
- `started` is epoch seconds. A reader treats it as equal to the process's actual start time within 5 seconds, because `wt` and `repo-cleanup` read it by different methods. Both compute it as now minus `ps -o etime= -p <pid>` (format `[[dd-]hh:]mm:ss`); each side subtracts a whole-second reading, so the two reads can differ by up to 2 seconds from rounding alone and a 2-second window had no margin. Liveness therefore compares wall-clock elapsed time: a wall-clock step of more than 5 seconds after launch (an NTP step) makes a live claim read as stale on both sides.
- A reader ignores a live claim whose `pid` is its own process or one of its ancestors: that is a nested launch (an agent running `wt` in that checkout), not another session. `wt` walks the parent chain for this, as `repo-cleanup` does. An ancestor's claim exempts the launch only while it is live.
- Readers must be plain POSIX tools. `repo-cleanup` must not call `wt`, so the format stays line-oriented `key=value`.
- Unknown keys are ignored. A file missing `pid`, `started` or `path` is treated as stale.
- Under `wt-sessions/`, `.lock` is the flock `wt` takes around reading and writing claims; readers ignore it.
