# Session claims

A claim records that a `wt` session runs in a repository's root checkout. `wt` (Go, `wt/internal/sessionclaim`) writes and removes claims; `repo-cleanup` (bash 3.2, in the repo-cleanup-skill repository) only reads them. Neither side may depend on the other being installed, so this file format is the whole contract.

A claim is one file per live `wt` process, in the repository's common git directory:

```
<git-common-dir>/wt-sessions/<pid>
```

Contents: plain `key=value` lines, one per line, no quoting:

```
pid=<wt process id>
started=<process start time, seconds since epoch>
path=<absolute path of the checkout the session runs in>
branch=<branch checked out when the session started, or "(detached)">
agent=<agent name, e.g. claude>
```

Rules:

- `wt` writes the claim after the check passes (or the person chose continue) and before it starts the agent. It writes to a temp name and renames, so a reader never sees half a file. Temp names are not all digits; readers consider only files named by a pid.
- `wt` removes its claim when it exits. A killed `wt` leaves a stale claim; readers must tolerate that.
- A claim is **live** when `pid` is running **and** that process's start time equals `started`. This guards against a reused pid. A claim that fails this test is stale: readers ignore it, and `wt` deletes it when it finds it.
- A claim counts as holding the root when `path` equals the root path. `wt` writes claims for root launches only, so every claim file is a root claim. A reader still compares `path`, so the format can later carry worktree claims.
- `path` is the physical path of the root: symlinks resolved (so `/var` and `/private/var` agree on macOS), no trailing slash. Both sides derive it from the primary worktree's top level, not from the current directory. `wt` uses `filepath.EvalSymlinks`; `repo-cleanup` uses `pwd -P`.
- `pid` is the `wt` process. `wt` runs the agent as a child and stays alive until it exits, so the claim lasts exactly as long as the session.
- `started` is epoch seconds. A reader treats it as equal to the process's actual start time within 2 seconds, because `wt` and `repo-cleanup` read it by different methods. Both compute it as now minus `ps -o etime= -p <pid>` (format `[[dd-]hh:]mm:ss`).
- Readers must be plain POSIX tools. `repo-cleanup` must not call `wt`, so the format stays line-oriented `key=value`.
- Unknown keys are ignored. A file missing `pid`, `started` or `path` is treated as stale.
- Under `wt-sessions/`, `.lock` is the flock `wt` takes around reading and writing claims; readers ignore it.
