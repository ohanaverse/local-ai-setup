# Open wt bug fixes: design (2026-10-20)

Covers issues #398, #401, #402 and #388. Out of scope for now: #399, #393, #403 (upstream
investigation), #405 (design decision), #380 (closeable), and the picker follow-ups #387, #389
and #390, which follow #388. Findings come from a read-only recon of the code on `main`; every
issue was confirmed still present.

## Intent

Remove three correctness problems in wt that cost the user data or show wrong information, in the
order that unblocks the rest: a dead-code trap (#398), a settings-file data-loss bug and the
override that rides on the same code path (#401, #402), and a wrong-PR tag in the picker (#388).

## Goals

- A session's own writes to `settings.local.json` survive a launch that pins a nested key
  (the `permissions.allow` grant in #401).
- A user's own `permissions.defaultMode` wins over the claude local profile's pin (#402).
- Remote picker rows show the PR for their own remote only (#388).
- No production code path can pick the wrong `FilledOrder` (#398).

## Non-goals and anti-goals

- No change to codex (TOML, whole-file restore) or opencode (env merge, no restore).
- Do not change `mergeInto`'s default semantics; `resolve.go` and opencode rely on them.
- Do not read `.claude/settings.json` or `~/.claude/settings.json` for #402; "absent" means absent
  from the worktree's `settings.local.json` only. Document this.
- Failure to fix must never be silent data loss: when in doubt, keep the session's value.

## Delivery: three PRs

1. **#398** (tiny). Delete `SlotProfile.FilledOrder` (`wt/internal/profiles/slot.go`). Keep
   `filledOrderFrom` and move the comment onto it. Repoint `TestFilledOrder` and
   `slot_fixture_test.go:69` to `SlotCheck.FilledOrder()` (the seven cases pass unchanged because
   `RestSlot` falls back to `Profile.Default`). Leave the archival plans under `docs/superpowers/`.
2. **#401**, then **#402** as a second stacked PR.
3. **#388**, with the head-repo approach below.

## #401: leaf-path restore (PR 2a)

Decided by the owner: **when the session changed a pinned leaf after the merge, keep the session's
value.**

- `ApplyConfigContent` (`wt/internal/profiles/configcontent.go`) records one entry per leaf the
  profile wrote: a path array (not a dotted string) and a hash of the canonical JSON of the value
  wt wrote. A leaf is any value that is not a non-empty object. The hash, not the value, goes in the
  `.keys` marker, so config values that may hold env secrets are not copied to a second file.
- `surgicalRestoreJSON` and `surgicalDeleteJSON` act per leaf: skip a missing path or a
  non-object intermediate (the session replaced it); skip an absent leaf (do not resurrect); skip a
  leaf whose current hash differs from the recorded one (the session wins); otherwise restore the
  original leaf, or delete it if the original lacked it. Prune an emptied ancestor only if the
  original did not contain it; never prune the root. If the root ends up empty in the new-file case,
  the file is deleted, as today.
- Arrays and null are whole-value leaves. Presence is checked separately from value so a `null`
  leaf is distinct from an absent one.
- **Type conflict.** If the original holds a non-object at an ancestor of a profile leaf, the merge
  replaces that ancestor. Restore must not keep the whole subtree just because the session added a
  sibling: fall back to leaf-by-leaf removal inside the subtree, and restore the original scalar
  only when the container ends up empty. The ancestor walk stops at the first non-object at any
  depth.
- **Legacy marker.** `.keys` written by an older wt is a JSON array of top-level key names. The
  reader detects it and puts each key back by the original document, with no hash to compare
  against (a leaf the session deleted stays deleted, as for a new record), so self-heal after a
  crash that straddles an upgrade still works. Check what an older wt does with the new format (fail
  open to whole-file restore, or fall back) and record the answer in the tests.
- The empty-object leaf is stated by case: merging `{}` into an existing object is a no-op and is
  not recorded.
- Both restore call sites in `restoreIfBackedUpLocked` (absent and present branches) read the
  marker; both change.
- An existing `settings.local.json` holding bare JSON `null` is refused like invalid JSON, not
  treated as an empty object: it is the one payload that decodes without an error into a nil map
  (`mergeInto` used to panic on it), so the launch degrades to an unprofiled one. A test pins the
  refusal and that the file is left untouched.

**Tests:** the issue's repro; the new-file case beside `configcontent_test.go:901`; a
session-changed leaf kept (existing and new-file cases); a session-deleted leaf not resurrected;
unchanged leaf restored or deleted; emptied containers; type conflict with a session sibling;
arrays; null; numbers (one decoder on both sides); keys containing dots, slashes or empty; legacy
marker self-heal; a second launch refused while the first is live. Recheck the existing tests that
assert the old behavior: `configcontent_test.go:236-263, 441, 511, 682, 854, 901`.

**Docs:** rewrite `wt/docs/wt-agents/profiles.md` lines 84-95 and 231-237 (the limitation is gone);
fix the "restores both" wording near 169 and 196; fix the top-level wording in the doc comments in
`configcontent.go`; check `wt/docs/internals/launch-flow.md`; add a `wt/CHANGELOG.md` entry.

## #402: `config_defaults` (PR 2b)

Decided by the owner: **a new `config_defaults` table, same shape as `config_content`.**

- Applied after `config_content` with a merge-if-absent helper in `wt/internal/profiles/leaves.go` (`collectAbsentLeaves`, `setLeaves`). A leaf that already
  exists in the file is neither written nor recorded, so it is never reverted. A leaf that is absent
  is written and recorded, then restored like any other.
- Move `permissions = { defaultMode = "default" }` in the claude local example to
  `config_defaults`; keep `env.CLAUDE_CODE_ATTRIBUTION_HEADER` in `config_content` (a hard
  requirement that should still override).
- A pre-existing `permissions` that is not an object is left alone and not recorded.
- Tier merge in `resolve.go`: a later tier's value wins per leaf. `validate.go` counts the new field
  as a config-file mechanism and rejects it for agents with no file target. Slot profiles: reject
  or document (their `CheckConfigContent` path ignores it).
- Known limit, to document: a user's existing `defaultMode = "auto"` is respected, which brings
  back the #393 classifier slowdown for that user; and `defaultMode` set in `settings.json` files
  still gets overridden.
- **Tests:** absent default written and restored; user value kept at launch and at exit; value equal
  to the default; non-object `permissions`; tier merge; validation per agent.
- **Docs:** `profiles.md` example and cost note, File safety section, the "What each entry changes"
  table; `wt/docs/internals/agents.md` and `launch-flow.md`; CHANGELOG.

## #388: tag remote rows by the PR's head repo (PR 3)

Decided by the owner: **key by PR head repository, not an extra `gh repo view` call.**

- `gh pr list --json` also returns `headRepository` and `headRepositoryOwner`. The PR map is
  `PRSet{Repo, ByBranch}`; remote rows look up by `Entry.RemoteRepo`/`Entry.RemoteBranch`. Resolve each remote row's remote URL (via `git remote get-url`, which
  applies `insteadOf`) to owner/repo, normalized across https, ssh and scp-style URLs, `.git`
  stripped, case-insensitive. Tag a remote row only on a match; an unknown or removed remote leaves
  the row untagged (never a first-slash guess).
- Rows from a second remote no longer show the base repo's PR. Local and worktree rows keep the
  name-only lookup (out of scope; documented).
- `PRBranch` is not split: it stays the display rule (`baseTitle`, `worktree_list.go:96`) and the
  local-row rule; remote rows use `Entry.RemoteBranch`, so a fork row's title does not go empty.
- Thread the key through `applyPRs` **and** `prsCurrent` (the filter-staleness check at
  `app.go:641`) so the `#<num>` filter stays consistent. Remote rows stay untagged until the data
  arrives, never briefly mis-tagged.
- Keeping cross-repository PRs (today `parsePRs` drops them at `prs.go:175`) is #387's job; this
  design keeps the map shape ready for it, so the PR/MergePR/preferPR signatures change once.
- Failure modes degrade to untagged rows, never wrong tags: a remote URL that does not match
  (ssh aliases, Enterprise hosts, renamed repos).
- **Docs:** `wt/CLAUDE.md:344`, `wt/docs/wt-agents/README.md:178-187`,
  `wt/docs/internals/packages.md:50`.

## Left to the builder

Names of the new helpers and the marker's exact on-disk shape; the hash function; test layout;
where in `slot_test.go` the repointed test lives.

## Calls made by the author (not the owner)

- Store a hash, not the value, in `.keys` (the skeptic's secrets point).
- Do not read the user-level and project-level `settings.json` for "absent".
- Leave local-branch rows on the name-only lookup in #388.
- No separate builder-check subagent: the recon's skeptic pass already challenged each proposal.

## Amendments after implementation (2026-10-20)

- #401: the `.keys` marker is written when the claude leaf records are non-nil (an empty `[]` when
  the merge wrote no leaf), so cleanup does not fall back to whole-file restore and lose grants.
- #401: bare JSON `null` in an existing `settings.local.json` is refused like invalid JSON (the one
  payload that decodes cleanly into a nil map), pinned by a test, not treated as an empty object.
- #402: the merge-if-absent helpers are in `wt/internal/profiles/leaves.go`; `merge.go` is untouched.
- #388: `PRBranch` is not split; remote rows key on `Entry.RemoteBranch`/`Entry.RemoteRepo` and the
  PR map is `PRSet{Repo, ByBranch}`.
- #388: `RemoteRepos` gives an empty map when `git remote` fails and omits only a remote whose
  `get-url` fails; `repoFromURL` returns "" for `file://` URLs and any URL with an empty host.
- Commit trailers name the model that wrote each commit.
