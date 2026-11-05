# `wt cleanup`

Lists the repository's worktrees, local branches and `origin` branches in four
groups, by how sure wt is that each can go, with the evidence for every row.
Read-only in this release: it removes nothing (`repo-cleanup` still deletes).

```bash
wt cleanup                  # interactive tick list on a terminal
wt cleanup --plain          # text list, also used when output is a pipe
wt cleanup --json           # one JSON document (printed on failure too)
wt cleanup --no-fetch       # skip `git fetch --prune origin`
wt cleanup --min-age 30m    # raise the fresh-branch threshold (default 10m)
```

## Groups

| Group | Meaning |
|---|---|
| Proven merged | A rung of the evidence ladder holds: same commit, ancestor of the base, trees match, no added changes, or `merge-tree` adds nothing. |
| Looks merged (less sure) | Only a weak signal (`git cherry`, patch-id), no proof, or a fact wt could not read. |
| Has work that exists nowhere else | Commits not in the base and not on any remote-tracking ref. |
| In use / locked | A primary, current, locked, nested or mid-operation worktree, or a held session claim. |

Weak signals are never proofs. Any fact wt cannot establish makes the row
less sure, never proven.

## Flags

| Flag | Effect |
|---|---|
| `--json` | Machine-readable report: [cleanup-list.schema.json](../../docs/contracts/cleanup-list.schema.json), sample [cleanup-list.sample.json](../../docs/contracts/cleanup-list.sample.json). |
| `--no-fetch` | Skip the fetch. A repository with no `origin` remote skips it anyway; the header says how it went. |
| `--min-age <duration>` | A branch younger than this that sits at or behind the base is never proven merged (a brand-new branch looks merged). At least `1m` (less is a usage error, exit 1); a value below the default `10m` is named in the header. |
| `--plain` | Text output on a terminal. |

## Exit codes

0: a list was produced. 1: the repository could not be classified (not a
repository, no default branch). A failed fetch, a missing `gh` and a row wt
could not judge are shown in the output, never an exit status. So is a
registry wt could not read (or a `local_path` in it that cannot be resolved):
the list still shows, the header says artifact protection is OFF, and
registered model artifacts are not marked in use.

Guide: [08 §9](../../docs/guides/08-maintenance-and-troubleshooting.md#9-cleaning-up-worktrees-and-branches).
Design: [the spec](../../docs/superpowers/specs/2026-11-04-wt-cleanup-design.md).
