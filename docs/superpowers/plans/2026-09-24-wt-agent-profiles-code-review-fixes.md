# wt agent-profiles: code review fixes

Source: `/code-review` (high effort) pass over the `wt-agent-profiles-design`
branch, 2026-09-24. 5 findings, all verified against the current code before
implementation (2 were empirically reproduced by building the branch).

## Items

1. **Race condition in config_content snapshot/restore**
   (`internal/profiles/configcontent.go`). `snapshotAndWrite`/
   `restoreIfBackedUp` have no cross-process locking, so two concurrent
   profiled launches targeting the same file (codex's target is a fixed
   global path, not worktree-scoped) can corrupt each other's backup/target.
   Fix: serialize both operations per-target with an flock-based lock file
   in `backupDir()`, mirroring `internal/litellm/configfile.go`'s
   `WithLock` pattern. Restructure `restoreIfBackedUp` into a lock-free
   core (`restoreIfBackedUpLocked`) called both by the locked public
   wrapper and by `snapshotAndWrite`'s internal self-heal, so the two never
   deadlock by nesting lock acquisitions.

2. **`wt profile show -A <agent>` without `-M` always errors**
   (`cmd/wt/profile.go`). `findModelByID` is called unconditionally even
   when `-M` is empty, and no registry model has empty ID, so it always
   fails. Fix: skip model resolution when `-M` is omitted and resolve with
   a zero `config.Model{}` — `profiles.Resolve` degrades gracefully (no
   tier can match without a model) instead of erroring, matching the
   documented optional `-M` usage.

3. **Invalid `match` values in profiles.toml fail silently**
   (`internal/profiles/validate.go`). `Validate` never checks `p.Match` is
   one of `"location"/"provider"/"model"`; a typo passes validation, then
   `Resolve`'s tier lookup silently skips the profile forever with no
   error anywhere. Fix: add a match-tier check to `Validate` alongside the
   existing mechanism check, reusing the existing per-profile error
   collection.

4. **`restoreIfBackedUp` hardcodes file mode 0o644**
   (`internal/profiles/configcontent.go`). The restore path discards the
   target's original permission bits. Fix: capture the original file's
   mode in `snapshotAndWrite` alongside its content, persist it next to
   the backup, and use it (falling back to 0o644 only when no stored mode
   is found, e.g. a backup left by an older wt version) in
   `restoreIfBackedUpLocked`.

5. **profiles.toml loaded/validated twice per launch**
   (`cmd/wt/launch.go`, `cmd/wt/app.go`). `newApp()` already loads and
   validates profiles.toml into `a.profiles`/`a.profilesLoadErr`/
   `a.profilesValidateErr`; `applyProfileForLaunch` (reached via
   `launchFiltered`/`launchPassthrough`, both called from `main.go` where
   `a` is already in scope) reloads and re-validates independently. Fix:
   thread the already-loaded store/errors through `launchFiltered`/
   `launchPassthrough`/`runAgentCmd`/`applyProfileForLaunch` instead of
   calling `loadProfileStore()`/`profiles.Validate` again. (Scope check:
   the TUI launch path does not call `applyProfileForLaunch` at all today,
   so this is a `cmd/wt`-only change — not touching `internal/tui`.)

## Verification

`go build ./...`, `go vet ./...`, `go test ./...` from `wt/` after each
item; `make check` before considering the branch done.

## Commits

One commit per item, referencing "completes plan item #N".
